package geo

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

const secretKey = "SECRET-GEO-KEY-123"

type fakeProvider struct {
	calls atomic.Int32
	mu    sync.Mutex
	fn    func(ip netip.Addr) (domain.Geo, error)
	gate  chan struct{} // when set, lookups wait for it to be closed
}

func (f *fakeProvider) LookupGeo(ctx context.Context, ip netip.Addr) (domain.Geo, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ip)
	}
	asn := uint32(64500)
	return domain.Geo{IP: ip.String(), CountryCode: "DE", Country: "Germany", City: "Berlin", Organization: "Example GmbH", ASN: &asn}, nil
}

func (f *fakeProvider) set(fn func(netip.Addr) (domain.Geo, error)) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

type rig struct {
	t     *testing.T
	db    *store.Store
	clock *testkit.Clock
	prov  *fakeProvider
	svc   *Service
	logs  *bytes.Buffer
	path  string
}

func newRig(t *testing.T, withKey bool) *rig {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	r := &rig{t: t, path: path, clock: testkit.NewClock(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)),
		prov: &fakeProvider{}, logs: &bytes.Buffer{}}
	r.open()
	if withKey {
		if err := r.db.SetGeoKey(ctx, secretKey, r.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// open (re)opens the database and builds a new service, like a restart.
func (r *rig) open() {
	ctx := context.Background()
	if r.db != nil {
		r.db.Close()
	}
	db, err := store.Open(ctx, r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		r.t.Fatal(err)
	}
	cipher, err := secrets.Load(filepath.Dir(r.path))
	if err != nil {
		r.t.Fatal(err)
	}
	db.UseSecrets(cipher)
	r.t.Cleanup(func() { db.Close() })
	r.db = db
	logger := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.svc = NewService(db, r.prov, r.clock, WithLogger(logger))
}

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func (r *rig) get(s string) GeoView {
	r.t.Helper()
	v, err := r.svc.Get(context.Background(), ip(s))
	if err != nil {
		r.t.Fatalf("Get(%s): %v", s, err)
	}
	return v
}

func TestFreshLookupIsCachedFor24Hours(t *testing.T) {
	r := newRig(t, true)
	v := r.get("203.0.113.5")
	if v.Status != StatusFresh || v.Stale || !v.HasGeo || v.Geo.Country != "Germany" || v.Geo.ASN == nil || *v.Geo.ASN != 64500 ||
		!v.Geo.FetchedAt.Equal(r.clock.Now()) || v.ErrorCode != "" {
		t.Fatalf("%+v", v)
	}
	r.clock.Advance(23*time.Hour + 59*time.Minute)
	if v := r.get("203.0.113.5"); v.Status != StatusFresh || r.prov.calls.Load() != 1 {
		t.Fatalf("cache not used within 24h: %+v calls=%d", v, r.prov.calls.Load())
	}
	r.clock.Advance(2 * time.Minute)
	if v := r.get("203.0.113.5"); v.Status != StatusFresh || r.prov.calls.Load() != 2 || !v.Geo.FetchedAt.Equal(r.clock.Now()) {
		t.Fatalf("expired entry not refreshed: %+v calls=%d", v, r.prov.calls.Load())
	}
}

func TestSharedExitIPIsLookedUpOnce(t *testing.T) {
	r := newRig(t, true)
	for i := 0; i < 5; i++ { // five proxies behind one exit address
		r.get("203.0.113.9")
	}
	if n := r.prov.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d", n)
	}
	// IPv4-mapped spelling is the same address.
	if v, err := r.svc.Get(context.Background(), ip("::ffff:203.0.113.9")); err != nil || v.Status != StatusFresh || r.prov.calls.Load() != 1 {
		t.Fatalf("mapped address: %+v %v calls=%d", v, err, r.prov.calls.Load())
	}
}

func TestConcurrentLookupsAreCoalesced(t *testing.T) {
	r := newRig(t, true)
	r.prov.gate = make(chan struct{})
	var wg sync.WaitGroup
	views := make([]GeoView, 20)
	for i := range views {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := r.svc.Get(context.Background(), ip("203.0.113.20"))
			if err != nil {
				t.Error(err)
			}
			views[i] = v
		}()
	}
	for r.prov.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the other callers pile up behind the leader
	close(r.prov.gate)
	wg.Wait()
	if n := r.prov.calls.Load(); n != 1 {
		t.Fatalf("lookups = %d, want 1", n)
	}
	for i, v := range views {
		if v.Status != StatusFresh || v.Geo.City != "Berlin" {
			t.Fatalf("caller %d got %+v", i, v)
		}
	}
	st, _ := r.db.GeoState(context.Background())
	if st.Used != 1 {
		t.Fatalf("budget used = %d, want 1", st.Used)
	}
}

func TestWaiterCanGiveUp(t *testing.T) {
	r := newRig(t, true)
	r.prov.gate = make(chan struct{})
	go r.svc.Get(context.Background(), ip("203.0.113.30"))
	for r.prov.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.svc.Get(ctx, ip("203.0.113.30")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(r.prov.gate)
}

func TestStaleDataIsRetainedWhenRefreshFails(t *testing.T) {
	r := newRig(t, true)
	r.get("203.0.113.40")
	first := r.clock.Now()
	r.clock.Advance(30 * time.Hour)
	r.prov.set(func(netip.Addr) (domain.Geo, error) {
		return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: providers.KindUnavailable, Status: 503}
	})
	v := r.get("203.0.113.40")
	if v.Status != StatusStale || !v.Stale || !v.HasGeo || v.Geo.City != "Berlin" || !v.Geo.FetchedAt.Equal(first) ||
		v.ErrorCode != "provider_unavailable" {
		t.Fatalf("%+v", v)
	}
	if got, _ := r.db.GetGeoCache(context.Background(), "203.0.113.40"); !got.FetchedAt.Equal(first) || got.City != "Berlin" {
		t.Fatalf("failed refresh overwrote the cache: %+v", got)
	}
}

func TestNoKeyMeansNotConfiguredAndNoRequests(t *testing.T) {
	r := newRig(t, false)
	v := r.get("203.0.113.50")
	if v.Status != StatusNotConfigured || v.ErrorCode != "not_configured" || v.HasGeo || r.prov.calls.Load() != 0 {
		t.Fatalf("%+v calls=%d", v, r.prov.calls.Load())
	}
	if st, _ := r.db.GeoState(context.Background()); st.Used != 0 {
		t.Fatalf("not-configured lookups consumed budget: %+v", st)
	}
	// Fresh cached data stays usable without a key; expired data is shown as stale.
	if err := r.db.PutGeoCache(context.Background(), domain.Geo{IP: "203.0.113.51", Country: "France", FetchedAt: r.clock.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if v := r.get("203.0.113.51"); v.Status != StatusFresh || v.Geo.Country != "France" {
		t.Fatalf("%+v", v)
	}
	if err := r.db.PutGeoCache(context.Background(), domain.Geo{IP: "203.0.113.52", Country: "Spain", FetchedAt: r.clock.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if v := r.get("203.0.113.52"); v.Status != StatusStale || v.ErrorCode != "not_configured" || v.Geo.Country != "Spain" {
		t.Fatalf("%+v", v)
	}
	if r.prov.calls.Load() != 0 {
		t.Fatal("provider called without a key")
	}
}

func TestPrivateAddressesAreNeverLookedUp(t *testing.T) {
	r := newRig(t, true)
	for _, s := range []string{"10.0.0.1", "192.168.1.1", "127.0.0.1", "::1", "100.64.0.1", "0.0.0.0"} {
		if _, err := r.svc.Get(context.Background(), ip(s)); !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: err = %v", s, err)
		}
	}
	if _, err := r.svc.Get(context.Background(), netip.Addr{}); !errors.Is(err, ErrNotPublic) {
		t.Errorf("zero address: %v", err)
	}
	if r.prov.calls.Load() != 0 {
		t.Fatal("provider called for a non-public address")
	}
}

func TestDailyBudgetIsPersistentAndResetsAtUTCMidnight(t *testing.T) {
	r := newRig(t, true)
	r.svc.DailyLimit = 3
	for i := 1; i <= 3; i++ {
		if v := r.get("203.0.113." + strconv.Itoa(60+i)); v.Status != StatusFresh {
			t.Fatalf("lookup %d: %+v", i, v)
		}
	}
	v := r.get("203.0.113.70")
	if v.Status != StatusQuotaExhausted || v.ErrorCode != "quota_exhausted" || v.HasGeo || v.RetryAt.IsZero() || r.prov.calls.Load() != 3 {
		t.Fatalf("%+v calls=%d", v, r.prov.calls.Load())
	}
	r.open() // restart
	r.svc.DailyLimit = 3
	if v := r.get("203.0.113.71"); v.Status != StatusQuotaExhausted {
		t.Fatalf("budget forgotten after restart: %+v", v)
	}
	// Cached entries keep working while the budget is exhausted.
	if v := r.get("203.0.113.61"); v.Status != StatusFresh {
		t.Fatalf("cache must not need budget: %+v", v)
	}
	r.clock.Advance(15 * time.Hour) // 2026-10-20 09:00 + 15h = 2026-10-21 00:00 UTC
	if v := r.get("203.0.113.72"); v.Status != StatusFresh {
		t.Fatalf("budget not reset at UTC midnight: %+v", v)
	}
}

func TestDefaultDailyLimitIs1000(t *testing.T) {
	r := newRig(t, true)
	if r.svc.DailyLimit != 1000 || r.svc.TTL != 24*time.Hour {
		t.Fatalf("defaults: limit=%d ttl=%v", r.svc.DailyLimit, r.svc.TTL)
	}
}

func TestRateLimitPausesUntilRetryAfter(t *testing.T) {
	r := newRig(t, true)
	r.prov.set(func(netip.Addr) (domain.Geo, error) {
		return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: providers.KindRateLimited, Status: 429, RetryAfter: 5 * time.Minute}
	})
	v := r.get("203.0.113.80")
	if v.Status != StatusPaused || v.ErrorCode != "provider_rate_limited" || !v.RetryAt.Equal(r.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("%+v", v)
	}
	calls := r.prov.calls.Load()
	r.clock.Advance(4 * time.Minute)
	if v := r.get("203.0.113.81"); v.Status != StatusPaused || r.prov.calls.Load() != calls {
		t.Fatalf("pause ignored: %+v", v)
	}
	r.prov.set(nil)
	r.clock.Advance(2 * time.Minute)
	if v := r.get("203.0.113.82"); v.Status != StatusFresh {
		t.Fatalf("pause did not end: %+v", v)
	}
	if st, _ := r.db.GeoState(context.Background()); st.PauseReason != "" || st.BackoffAttempt != 0 {
		t.Fatalf("success must clear the pause: %+v", st)
	}
}

func TestRateLimitWithoutRetryAfterBacksOffExponentially(t *testing.T) {
	r := newRig(t, true)
	r.prov.set(func(netip.Addr) (domain.Geo, error) {
		return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: providers.KindRateLimited, Status: 429}
	})
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, d := range want {
		v := r.get("203.0.113.9" + strconv.Itoa(i))
		if v.Status != StatusPaused || !v.RetryAt.Equal(r.clock.Now().Add(d)) {
			t.Fatalf("attempt %d: %+v, want pause of %v", i+1, v, d)
		}
		r.clock.Advance(d)
	}
}

func TestForbiddenPausesUntilTheKeyChanges(t *testing.T) {
	r := newRig(t, true)
	r.prov.set(func(netip.Addr) (domain.Geo, error) {
		return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: providers.KindForbidden, Status: 403}
	})
	if v := r.get("203.0.113.100"); v.Status != StatusPaused || v.ErrorCode != "provider_forbidden" {
		t.Fatalf("%+v", v)
	}
	calls := r.prov.calls.Load()
	usedBefore, _ := r.db.GeoState(context.Background())
	r.clock.Advance(13 * time.Hour) // still the same UTC day, so usage stays comparable
	if v := r.get("203.0.113.101"); v.Status != StatusPaused || r.prov.calls.Load() != calls {
		t.Fatalf("a forbidden pause ended with time: %+v", v)
	}
	r.open() // survives restart
	if v := r.get("203.0.113.102"); v.Status != StatusPaused {
		t.Fatalf("pause lost on restart: %+v", v)
	}
	r.prov.set(nil)
	if err := r.db.SetGeoKey(context.Background(), "A-NEW-KEY", r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if v := r.get("203.0.113.103"); v.Status != StatusFresh {
		t.Fatalf("pause not reset by a changed key: %+v", v)
	}
	st, _ := r.db.GeoState(context.Background())
	if st.Used != usedBefore.Used+1 {
		t.Fatalf("usage must survive a key change (before %d, after %d)", usedBefore.Used, st.Used)
	}
}

func TestOtherFailuresBackOffToo(t *testing.T) {
	r := newRig(t, true)
	r.prov.set(func(netip.Addr) (domain.Geo, error) { return domain.Geo{}, errors.New("connection reset") })
	v := r.get("203.0.113.110")
	if v.Status != StatusUnavailable || v.ErrorCode != "provider_unavailable" || !v.RetryAt.Equal(r.clock.Now().Add(time.Minute)) || v.HasGeo {
		t.Fatalf("%+v", v)
	}
	calls := r.prov.calls.Load()
	r.get("203.0.113.111")
	if r.prov.calls.Load() != calls {
		t.Fatal("a failing provider is being hammered during its backoff")
	}
}

func TestInvalidResponsesAreReportedWithoutCaching(t *testing.T) {
	r := newRig(t, true)
	r.prov.set(func(netip.Addr) (domain.Geo, error) {
		return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: providers.KindInvalidResponse, Status: 200}
	})
	if v := r.get("203.0.113.120"); v.Status != StatusUnavailable || v.ErrorCode != "invalid_response" {
		t.Fatalf("%+v", v)
	}
	if _, err := r.db.GetGeoCache(context.Background(), "203.0.113.120"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid data was cached: %v", err)
	}
}

func TestPeekNeverCallsTheProvider(t *testing.T) {
	r := newRig(t, true)
	if v, err := r.svc.Peek(context.Background(), ip("203.0.113.130")); err != nil || v.HasGeo || v.Status != StatusUnknown {
		t.Fatalf("miss: %+v %v", v, err)
	}
	r.get("203.0.113.130")
	r.clock.Advance(25 * time.Hour)
	v, err := r.svc.Peek(context.Background(), ip("203.0.113.130"))
	if err != nil || !v.HasGeo || !v.Stale || v.Status != StatusStale {
		t.Fatalf("expired entry: %+v %v", v, err)
	}
	if r.prov.calls.Load() != 1 {
		t.Fatalf("Peek called the provider: %d", r.prov.calls.Load())
	}
}

func TestKeyNeverAppearsInLogsOrViews(t *testing.T) {
	r := newRig(t, true)
	for _, kind := range []providers.Kind{providers.KindRateLimited, providers.KindForbidden, providers.KindUnavailable} {
		kind := kind
		r.prov.set(func(netip.Addr) (domain.Geo, error) {
			return domain.Geo{}, &providers.Error{Provider: "ipapi", Kind: kind, Status: 500, RetryAfter: time.Minute}
		})
		v := r.get("203.0.113.14" + strconv.Itoa(int(kind)))
		if strings.Contains(v.ErrorCode+v.Status, secretKey) {
			t.Fatal("view leaks the key")
		}
		r.clock.Advance(2 * time.Hour)
	}
	if !strings.Contains(r.logs.String(), "geolocation") {
		t.Fatal("expected pause log entries")
	}
	if strings.Contains(r.logs.String(), secretKey) {
		t.Fatalf("logs leak the key: %s", r.logs.String())
	}
}

func TestGeoFailuresNeverTouchAvailability(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, true)
	id, err := r.db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: r.clock.Now(), State: domain.StateUp, ExitIP: "203.0.113.150"}); err != nil {
		t.Fatal(err)
	}
	r.prov.set(func(netip.Addr) (domain.Geo, error) { return domain.Geo{}, errors.New("geo down") })
	r.get("203.0.113.150")
	latest, err := r.db.LatestAvailability(ctx, id)
	if err != nil || latest.State != domain.StateUp || latest.Failure != nil {
		t.Fatalf("geo failure changed availability: %+v %v", latest, err)
	}
}
