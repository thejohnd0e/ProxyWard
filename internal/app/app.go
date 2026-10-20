// Package app wires the services together and manages their lifecycle.
package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/check"
	"github.com/thejohnd0e/ProxyWard/internal/config"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/geo"
	"github.com/thejohnd0e/ProxyWard/internal/httpapi"
	"github.com/thejohnd0e/ProxyWard/internal/jobs"
	"github.com/thejohnd0e/ProxyWard/internal/notify"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/proxy"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
	"github.com/thejohnd0e/ProxyWard/internal/webui"
)

const shutdownTimeout = 10 * time.Second

// Options replaces production dependencies. The zero value uses the real
// public services; the end-to-end test server substitutes local fixtures.
type Options struct {
	IPProviders  []providers.IPProvider // default: ipify, then AWS checkip
	SpeedService *providers.Cloudflare  // default: Cloudflare
	GeoSources   []providers.GeoSource  // default: the built-in keyless sources
	TelegramURL  string                 // default: api.telegram.org
	// RootCAs, when set, is trusted for proxy, target, geolocation and Telegram
	// TLS. It exists for local certificate authorities in tests.
	RootCAs *x509.CertPool
}

// Run migrates the database, then serves until ctx is canceled.
func Run(ctx context.Context, cfg config.Config) error {
	return RunWith(ctx, cfg, Options{})
}

// RunWith is Run with replaceable dependencies.
func RunWith(ctx context.Context, cfg config.Config, opts Options) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	cipher, err := loadCipher(ctx, cfg.DataDir, db)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	svc, geoSvc, waitGeo := newJobService(ctx, db, opts)
	svcDone := make(chan struct{})
	go func() {
		defer close(svcDone)
		if err := svc.Run(ctx); err != nil {
			slog.Error("job service stopped", "error", err)
		}
	}()
	// Notifications are delivered by their own worker so that a slow or broken
	// Telegram can never hold up checks.
	telegram := notify.NewTelegram(notify.StoreToken(db))
	if opts.TelegramURL != "" {
		telegram.BaseURL = opts.TelegramURL
	}
	trustRoots(telegram.Client, opts.RootCAs)
	notifier := &notify.Worker{Store: db, Telegram: telegram, Clock: domain.SystemClock{}}
	notifyDone := make(chan struct{})
	go func() {
		defer close(notifyDone)
		_ = notifier.Run(ctx)
	}()
	retentionDone := make(chan struct{})
	go func() {
		defer close(retentionDone)
		runRetention(ctx, db, domain.SystemClock{}, time.Hour)
	}()
	handler := NewHandler(db, cipher, httpapi.WithSecureCookies(cfg.SessionSecure),
		httpapi.WithJobs(svc.Queue), httpapi.WithReconcile(svc.Reconcile), httpapi.WithGeo(geoSvc))
	err = serve(ctx, ln, handler)
	<-svcDone // running checks are interrupted and recorded before the database closes
	<-notifyDone
	<-retentionDone
	waitGeo()
	return err
}

// newJobService wires the production checkers. Geolocation of a new exit
// address is fetched in the background and never affects the check result.
func newJobService(ctx context.Context, db *store.Store, opts Options) (*jobs.Service, *geo.Service, func()) {
	var geoWG sync.WaitGroup
	clock := domain.SystemClock{}
	geoSources := opts.GeoSources
	if len(geoSources) == 0 {
		geoSources = providers.GeoSources()
	}
	geoSvc := geo.NewService(db, geoSources, clock)

	checker := check.NewChecker()
	if len(opts.IPProviders) > 0 {
		checker.Providers = opts.IPProviders
	}
	speedService := opts.SpeedService
	if speedService == nil {
		speedService = providers.NewCloudflare()
	}
	speed := &check.SpeedChecker{Service: speedService, Pauses: db, Clock: clock}
	newClient := func(p domain.Proxy, timeout time.Duration) (*http.Client, error) {
		if opts.RootCAs != nil {
			return proxy.NewClient(p, timeout, proxy.WithRootCAs(opts.RootCAs))
		}
		return proxy.NewClient(p, timeout)
	}
	svc := jobs.NewService(jobs.Config{
		Store: db, Clock: clock,
		Availability: checker, Speed: speed, NewClient: newClient,
		Incidents: &notify.IncidentTracker{Store: db},
	})
	// Geolocation of a new exit address is asked through the same proxy, so the
	// source sees the proxy's address and never this machine's.
	svc.Runner.OnAvailability = func(p domain.Proxy, a domain.Availability) {
		addr, err := netip.ParseAddr(a.ExitIP)
		if a.State != domain.StateUp || err != nil {
			return
		}
		geoWG.Add(1)
		go func() {
			defer geoWG.Done()
			gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			client, err := newClient(p, 10*time.Second)
			if err != nil {
				return
			}
			defer client.CloseIdleConnections()
			_, _ = geoSvc.Get(gctx, addr, client)
		}()
	}
	return svc, geoSvc, geoWG.Wait
}

// trustRoots makes a direct client trust roots (tests with local CAs only).
func trustRoots(c *http.Client, roots *x509.CertPool) {
	if roots == nil {
		return
	}
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
}

// loadCipher loads the persistent key. A missing key is only created when no
// encrypted record exists: regenerating it would orphan existing secrets.
func loadCipher(ctx context.Context, dataDir string, db *store.Store) (*secrets.Cipher, error) {
	present, err := secrets.Exists(dataDir)
	if err != nil {
		return nil, fmt.Errorf("check secrets key: %w", err)
	}
	if !present {
		encrypted, err := db.HasEncryptedRecords(ctx)
		if err != nil {
			return nil, err
		}
		if encrypted {
			return nil, fmt.Errorf("%s is missing but the database contains encrypted records; restore the key from a backup", secrets.KeyPath(dataDir))
		}
	}
	cipher, err := secrets.Load(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load secrets key: %w", err)
	}
	return cipher, nil
}

// SetAdminPassword sets or resets the administrator password and revokes all
// sessions. It is used by the admin set-password command.
func SetAdminPassword(ctx context.Context, cfg config.Config, password string) error {
	hash, err := auth.Hash(password)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	// Create the secrets key now (never over existing encrypted records), so a
	// fresh installation can be backed up before the server has ever run.
	if _, err := loadCipher(ctx, cfg.DataDir, db); err != nil {
		return err
	}
	return db.SetAdminPasswordHash(ctx, hash, time.Now())
}

// NewHandler builds the HTTP handler: the health endpoint plus the API.
func NewHandler(db *store.Store, cipher *secrets.Cipher, opts ...httpapi.Option) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", httpapi.New(db, cipher, opts...))
	mux.Handle("/", webui.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(pingCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return webui.SecurityHeaders(mux)
}

func serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	slog.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
