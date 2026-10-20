// Package geo provides cached geolocation and ASN data for observed exit IPs.
// Geo problems are reported through the view and never affect availability.
package geo

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/providers"
	"proxy-checker/internal/store"
)

// Status describes how trustworthy a GeoView is.
const (
	StatusFresh          = "fresh"           // cached within the TTL, or just fetched
	StatusStale          = "stale"           // older than the TTL; the refresh failed or is not allowed
	StatusNotConfigured  = "not_configured"  // no API key, no data
	StatusPaused         = "paused"          // provider rate limit or rejected key, no data
	StatusQuotaExhausted = "quota_exhausted" // local daily budget used up, no data
	StatusUnavailable    = "unavailable"     // lookup failed, no data
	StatusUnknown        = "unknown"         // Peek found nothing
)

// Error codes shown with a view.
const (
	CodeNotConfigured = "not_configured"
	CodeQuota         = "quota_exhausted"
	CodeRateLimited   = "provider_rate_limited"
	CodeForbidden     = "provider_forbidden"
	CodeUnavailable   = "provider_unavailable"
	CodeInvalid       = "invalid_response"
)

// Defaults from the specification.
const (
	DefaultTTL        = 24 * time.Hour
	DefaultDailyLimit = 1000
)

// ErrNotPublic is returned for addresses that must not be sent to the provider.
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

// Provider fetches geolocation for one address.
type Provider interface {
	LookupGeo(ctx context.Context, ip netip.Addr) (domain.Geo, error)
}

// Store is the persistence the service needs (implemented by *store.Store).
type Store interface {
	GetGeoCache(ctx context.Context, ip string) (domain.Geo, error)
	PutGeoCache(ctx context.Context, g domain.Geo) error
	GeoKey(ctx context.Context) (string, bool, error)
	GeoKeyFingerprint(key string) string
	GeoState(ctx context.Context) (store.GeoState, error)
	GeoBeginAttempt(ctx context.Context, now time.Time, keyFP string, limit int) (store.GeoGate, error)
	GeoSetPause(ctx context.Context, now time.Time, reason string, until time.Time, keyFP string) error
	GeoClearPause(ctx context.Context) error
}

// Service looks up and caches geolocation data.
type Service struct {
	TTL        time.Duration
	DailyLimit int

	store    Store
	provider Provider
	clock    domain.Clock
	logger   *slog.Logger

	mu       sync.Mutex
	inflight map[netip.Addr]*call
}

type call struct {
	done chan struct{}
	view GeoView
	err  error
}

// Option customizes a Service.
type Option func(*Service)

// WithLogger sets the logger. Only reasons and times are logged, never keys.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.logger = l } }

// NewService returns a Service with the 24 hour TTL and 1000 lookups per UTC day.
func NewService(st Store, p Provider, clock domain.Clock, opts ...Option) *Service {
	s := &Service{
		TTL: DefaultTTL, DailyLimit: DefaultDailyLimit,
		store: st, provider: p, clock: clock, logger: slog.Default(),
		inflight: map[netip.Addr]*call{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func normalize(ip netip.Addr) (netip.Addr, error) {
	ip = ip.Unmap()
	if !providers.IsPublic(ip) {
		return netip.Addr{}, ErrNotPublic
	}
	return ip, nil
}

// Peek returns cached data without any network access or budget use.
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
// otherwise refreshing it if the key, budget and provider pauses allow.
// Concurrent calls for one address share a single lookup. The returned error
// is only for unusable input, cancellation and local storage failures;
// provider problems are described by the view.
func (s *Service) Get(ctx context.Context, ip netip.Addr) (GeoView, error) {
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
	c.view, c.err = s.refresh(context.WithoutCancel(ctx), ip, cached)
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

func (s *Service) refresh(ctx context.Context, ip netip.Addr, cached *domain.Geo) (GeoView, error) {
	key, ok, err := s.store.GeoKey(ctx)
	if err != nil {
		return GeoView{}, err
	}
	if !ok {
		return degraded(cached, StatusNotConfigured, CodeNotConfigured, time.Time{}), nil
	}
	fp := s.store.GeoKeyFingerprint(key)
	now := s.clock.Now()

	gate, err := s.store.GeoBeginAttempt(ctx, now, fp, s.DailyLimit)
	if err != nil {
		return GeoView{}, err
	}
	if !gate.Allowed {
		switch gate.Reason {
		case store.GateQuota:
			return degraded(cached, StatusQuotaExhausted, CodeQuota, gate.RetryAt), nil
		case store.GateForbidden:
			return degraded(cached, StatusPaused, CodeForbidden, time.Time{}), nil
		case store.GateRateLimited:
			return degraded(cached, StatusPaused, CodeRateLimited, gate.RetryAt), nil
		}
		return degraded(cached, StatusUnavailable, CodeUnavailable, gate.RetryAt), nil
	}

	geo, err := s.provider.LookupGeo(ctx, ip)
	if err == nil {
		geo.IP = ip.String()
		geo.FetchedAt = s.clock.Now().UTC()
		if err := s.store.PutGeoCache(ctx, geo); err != nil {
			return GeoView{}, err
		}
		if err := s.store.GeoClearPause(ctx); err != nil {
			return GeoView{}, err
		}
		return GeoView{Geo: geo, HasGeo: true, Status: StatusFresh}, nil
	}
	return s.failed(ctx, err, now, fp, cached)
}

// failed records the pause that follows a provider failure and builds the view.
func (s *Service) failed(ctx context.Context, err error, now time.Time, fp string, cached *domain.Geo) (GeoView, error) {
	if errors.Is(err, providers.ErrNotConfigured) {
		return degraded(cached, StatusNotConfigured, CodeNotConfigured, time.Time{}), nil
	}
	st, stErr := s.store.GeoState(ctx)
	if stErr != nil {
		return GeoView{}, stErr
	}
	backoff := providers.Backoff(st.BackoffAttempt + 1)

	var (
		reason, code, status string
		until                time.Time
	)
	var pe *providers.Error
	switch {
	case errors.As(err, &pe) && pe.Kind == providers.KindRateLimited:
		reason, code, status = store.GateRateLimited, CodeRateLimited, StatusPaused
		until = now.Add(backoff)
		if pe.RetryAfter > 0 {
			until = now.Add(pe.RetryAfter)
		}
	case errors.As(err, &pe) && pe.Kind == providers.KindForbidden:
		reason, code, status = store.GateForbidden, CodeForbidden, StatusPaused
	default:
		reason, code, status = store.GateUnavailable, CodeUnavailable, StatusUnavailable
		until = now.Add(backoff)
		if pe != nil && pe.Kind == providers.KindInvalidResponse {
			code = CodeInvalid
		}
	}
	if err := s.store.GeoSetPause(ctx, now, reason, until, fp); err != nil {
		return GeoView{}, err
	}
	s.logger.Warn("geolocation lookups paused", "reason", reason, "until", until)
	return degraded(cached, status, code, until), nil
}
