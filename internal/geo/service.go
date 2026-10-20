// Package geo provides cached geolocation and ASN data for observed exit IPs.
// The data comes from public sources that describe the caller, asked through
// the proxy itself, so the machine running ProxyWard is never revealed and no
// account or key is needed. Geo problems are reported through the view and
// never affect availability.
package geo

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// Status describes how trustworthy a GeoView is.
const (
	StatusFresh       = "fresh"       // cached within the TTL, or just fetched
	StatusStale       = "stale"       // older than the TTL; the refresh failed or is not allowed
	StatusOff         = "off"         // lookups are switched off in the settings
	StatusUnavailable = "unavailable" // every lookup failed, no data
	StatusUnknown     = "unknown"     // Peek found nothing
)

// Error codes shown with a view.
const (
	CodeOff         = "off"
	CodeRateLimited = "provider_rate_limited"
	CodeForbidden   = "provider_forbidden"
	CodeUnavailable = "provider_unavailable"
	CodeInvalid     = "invalid_response"
)

// DefaultTTL is how long a cached record counts as fresh.
const DefaultTTL = 24 * time.Hour

const (
	sourceTimeout = 6 * time.Second
	maxTracked    = 1024
)

// ErrNotPublic is returned for addresses that must not be looked up.
var ErrNotPublic = errors.New("address is not a public IP")

// GeoView is what callers show: data (possibly stale) plus its state.
type GeoView struct {
	Geo       domain.Geo
	HasGeo    bool
	Stale     bool
	Status    string
	ErrorCode string
	RetryAt   time.Time // when a refresh may be attempted again, if known
}

// Store is the persistence the service needs (implemented by *store.Store).
type Store interface {
	GetGeoCache(ctx context.Context, ip string) (domain.Geo, error)
	PutGeoCache(ctx context.Context, g domain.Geo) error
	GetSettings(ctx context.Context) (domain.Settings, error)
}

// Service looks up and caches geolocation data.
type Service struct {
	TTL time.Duration

	store   Store
	sources []providers.GeoSource
	clock   domain.Clock
	logger  *slog.Logger

	mu       sync.Mutex
	inflight map[netip.Addr]*call
	failures map[netip.Addr]failure
}

type call struct {
	done chan struct{}
	view GeoView
	err  error
}

// failure remembers that every source failed for an address, so a broken
// route is not retried on every check.
type failure struct {
	count int
	until time.Time
	code  string
}

// Option customizes a Service.
type Option func(*Service)

// WithLogger sets the logger. Only source IDs and reasons are logged.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService returns a Service using sources in order when the setting is
// "auto", with the 24 hour TTL.
func NewService(st Store, sources []providers.GeoSource, clock domain.Clock, opts ...Option) *Service {
	s := &Service{
		TTL: DefaultTTL, store: st, sources: sources, clock: clock, logger: slog.Default(),
		inflight: map[netip.Addr]*call{}, failures: map[netip.Addr]failure{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Sources returns the available sources, in the order "auto" tries them.
func (s *Service) Sources() []providers.GeoSource {
	return append([]providers.GeoSource(nil), s.sources...)
}

func normalize(ip netip.Addr) (netip.Addr, error) {
	ip = ip.Unmap()
	if !providers.IsPublic(ip) {
		return netip.Addr{}, ErrNotPublic
	}
	return ip, nil
}

// Peek returns cached data without any network access.
func (s *Service) Peek(ctx context.Context, ip netip.Addr) (GeoView, error) {
	ip, err := normalize(ip)
	if err != nil {
		return GeoView{}, err
	}
	cached, err := s.store.GetGeoCache(ctx, ip.String())
	if errors.Is(err, store.ErrNotFound) {
		return GeoView{Status: StatusUnknown}, nil
	}
	if err != nil {
		return GeoView{}, err
	}
	return s.cachedView(cached), nil
}

func (s *Service) cachedView(g domain.Geo) GeoView {
	v := GeoView{Geo: g, HasGeo: true, Status: StatusFresh}
	if s.clock.Now().Sub(g.FetchedAt) >= s.TTL {
		v.Stale, v.Status = true, StatusStale
	}
	return v
}

// Get returns geolocation for ip, using the cache while it is fresh and
// otherwise asking the selected source(s) through client, which must route
// through the proxy whose exit address is ip. Concurrent calls for one address
// share a single lookup. The returned error is only for unusable input,
// cancellation and local storage failures; source problems are described by
// the view.
func (s *Service) Get(ctx context.Context, ip netip.Addr, client *http.Client) (GeoView, error) {
	ip, err := normalize(ip)
	if err != nil {
		return GeoView{}, err
	}
	var cached *domain.Geo
	if g, err := s.store.GetGeoCache(ctx, ip.String()); err == nil {
		if v := s.cachedView(g); !v.Stale {
			return v, nil
		}
		cached = &g
	} else if !errors.Is(err, store.ErrNotFound) {
		return GeoView{}, err
	}

	s.mu.Lock()
	if c, ok := s.inflight[ip]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.view, c.err
		case <-ctx.Done():
			return GeoView{}, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	s.inflight[ip] = c
	s.mu.Unlock()

	// The lookup outlives the first caller's context so that cancelling it
	// cannot fail the callers waiting on the same address.
	c.view, c.err = s.refresh(context.WithoutCancel(ctx), ip, client, cached)
	s.mu.Lock()
	delete(s.inflight, ip)
	s.mu.Unlock()
	close(c.done)
	return c.view, c.err
}

// degraded builds the view for a failed or disallowed refresh.
func degraded(cached *domain.Geo, status, code string, retryAt time.Time) GeoView {
	if cached != nil {
		return GeoView{Geo: *cached, HasGeo: true, Stale: true, Status: StatusStale, ErrorCode: code, RetryAt: retryAt}
	}
	return GeoView{Status: status, ErrorCode: code, RetryAt: retryAt}
}

// chosen returns the sources to try for the stored setting: all of them in
// order for "auto" (or an unknown ID), otherwise the one selected.
func (s *Service) chosen(setting string) []providers.GeoSource {
	for _, src := range s.sources {
		if src.ID == setting {
			return []providers.GeoSource{src}
		}
	}
	return s.sources
}

func (s *Service) refresh(ctx context.Context, ip netip.Addr, client *http.Client, cached *domain.Geo) (GeoView, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return GeoView{}, err
	}
	if settings.GeoSource == domain.GeoSourceOff {
		return degraded(cached, StatusOff, CodeOff, time.Time{}), nil
	}
	now := s.clock.Now()
	s.mu.Lock()
	f, blocked := s.failures[ip]
	s.mu.Unlock()
	if blocked && now.Before(f.until) {
		return degraded(cached, StatusUnavailable, f.code, f.until), nil
	}

	var (
		worst   string
		retryAt time.Time
	)
	for _, src := range s.chosen(settings.GeoSource) {
		sctx, cancel := context.WithTimeout(ctx, sourceTimeout)
		geo, err := src.Lookup(sctx, client, ip)
		cancel()
		if err == nil {
			geo.IP = ip.String()
			geo.FetchedAt = s.clock.Now().UTC()
			if err := s.store.PutGeoCache(ctx, geo); err != nil {
				return GeoView{}, err
			}
			s.mu.Lock()
			delete(s.failures, ip)
			s.mu.Unlock()
			return GeoView{Geo: geo, HasGeo: true, Status: StatusFresh}, nil
		}
		code, at := classify(err, now)
		worst = moreSevere(worst, code)
		if at.After(retryAt) {
			retryAt = at
		}
		s.logger.Warn("geolocation source failed", "source", src.ID, "reason", code)
	}
	return s.remember(ip, now, worst, retryAt, cached), nil
}

// classify maps a source error to a view code and, for a rate limit, the time
// the source asked to wait until.
func classify(err error, now time.Time) (string, time.Time) {
	var pe *providers.Error
	if !errors.As(err, &pe) {
		return CodeUnavailable, time.Time{}
	}
	switch pe.Kind {
	case providers.KindRateLimited:
		if pe.RetryAfter > 0 {
			return CodeRateLimited, now.Add(pe.RetryAfter)
		}
		return CodeRateLimited, time.Time{}
	case providers.KindForbidden:
		return CodeForbidden, time.Time{}
	case providers.KindInvalidResponse:
		return CodeInvalid, time.Time{}
	}
	return CodeUnavailable, time.Time{}
}

var severity = map[string]int{"": 0, CodeUnavailable: 1, CodeInvalid: 2, CodeForbidden: 3, CodeRateLimited: 4}

func moreSevere(a, b string) string {
	if severity[b] > severity[a] {
		return b
	}
	return a
}

// remember starts or extends the back-off after every source failed.
func (s *Service) remember(ip netip.Addr, now time.Time, code string, retryAt time.Time, cached *domain.Geo) GeoView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failures) >= maxTracked {
		for k, f := range s.failures {
			if !now.Before(f.until) {
				delete(s.failures, k)
			}
		}
	}
	f := s.failures[ip]
	f.count++
	until := now.Add(providers.Backoff(f.count))
	if retryAt.After(until) {
		until = retryAt
	}
	f.until, f.code = until, code
	if len(s.failures) < maxTracked {
		s.failures[ip] = f
	}
	return degraded(cached, StatusUnavailable, code, until)
}
