package geo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/store"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

var exit = netip.MustParseAddr("203.0.113.7")

// fakeSource is a geolocation service on a local server. It answers about the
// address in ip, like the real services answer about their caller.
type fakeSource struct {
	src    providers.GeoSource
	calls  atomic.Int32
	mu     sync.Mutex
	status int
	header map[string]string
	ip     string
	gate   chan struct{}
}

func newFakeSource(t *testing.T, id string) *fakeSource {
	t.Helper()
	f := &fakeSource{status: 200, ip: exit.String()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.gate != nil {
			<-f.gate
		}
		f.mu.Lock()
		status, hdr, ip := f.status, f.header, f.ip
		f.mu.Unlock()
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		if status == 200 {
			fmt.Fprintf(w, `{"ip":%q,"country_code":"DE","country":"Germany","city":"Berlin","asn":64500,"organization_name":"%s Example"}`, ip, id)
		}
	}))
	t.Cleanup(srv.Close)
	f.src = providers.GeoSource{ID: id, Name: id, URL: srv.URL}
	return f
}

func (f *fakeSource) fail(status int, hdr map[string]string) {
	f.mu.Lock()
	f.status, f.header = status, hdr
	f.mu.Unlock()
}

func (f *fakeSource) answerAbout(ip string) {
	f.mu.Lock()
	f.ip = ip
	f.mu.Unlock()
}

type rig struct {
	t     *testing.T
	db    *store.Store
	clock *testkit.Clock
	a, b  *fakeSource
	svc   *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, db: db, clock: testkit.NewClock(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)),
		a: newFakeSource(t, "alpha"), b: newFakeSource(t, "beta")}
	r.svc = NewService(db, []providers.GeoSource{r.a.src, r.b.src}, r.clock)
	return r
}

func (r *rig) choose(source string) {
	r.t.Helper()
	s := domain.DefaultSettings()
	s.GeoSource = source
	if err := r.db.PutSettings(context.Background(), s, r.clock.Now()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) get(ip netip.Addr) GeoView {
	r.t.Helper()
	v, err := r.svc.Get(context.Background(), ip, http.DefaultClient)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func TestLookupFillsAndCachesTheView(t *testing.T) {
	r := newRig(t)
	v := r.get(exit)
	if !v.HasGeo || v.Status != StatusFresh || v.Geo.CountryCode != "DE" || v.Geo.City != "Berlin" || v.Geo.ASN == nil || *v.Geo.ASN != 64500 {
		t.Fatalf("%+v", v)
	}
	if v.Geo.IP != exit.String() || !v.Geo.FetchedAt.Equal(r.clock.Now()) {
		t.Fatalf("%+v", v.Geo)
	}
	r.get(exit)
	if r.a.calls.Load() != 1 {
		t.Fatalf("the second call must come from the cache, got %d lookups", r.a.calls.Load())
	}
	peek, err := r.svc.Peek(context.Background(), exit)
	if err != nil || !peek.HasGeo || peek.Status != StatusFresh {
		t.Fatalf("%+v %v", peek, err)
	}
}

func TestAutoFallsThroughToTheNextSource(t *testing.T) {
	r := newRig(t)
	r.a.fail(503, nil)
	v := r.get(exit)
	if v.Status != StatusFresh || v.Geo.Organization != "beta Example" {
		t.Fatalf("%+v", v)
	}
	if r.a.calls.Load() != 1 || r.b.calls.Load() != 1 {
		t.Fatalf("calls a=%d b=%d", r.a.calls.Load(), r.b.calls.Load())
	}
}

func TestAChosenSourceIsTheOnlyOneAsked(t *testing.T) {
	r := newRig(t)
	r.choose("beta")
	if v := r.get(exit); v.Geo.Organization != "beta Example" {
		t.Fatalf("%+v", v)
	}
	if r.a.calls.Load() != 0 {
		t.Fatal("the source that was not chosen was asked")
	}
	r2 := newRig(t)
	r2.choose("beta")
	r2.b.fail(503, nil)
	if v := r2.get(exit); v.Status != StatusUnavailable || r2.a.calls.Load() != 0 {
		t.Fatalf("a chosen source must not fall back silently: %+v a=%d", v, r2.a.calls.Load())
	}
}

func TestAnUnknownChoiceBehavesLikeAuto(t *testing.T) {
	r := newRig(t)
	r.choose("removed-source")
	if v := r.get(exit); v.Status != StatusFresh || r.a.calls.Load() != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestOffMakesNoRequestAndKeepsOldData(t *testing.T) {
	r := newRig(t)
	r.choose(domain.GeoSourceOff)
	v := r.get(exit)
	if v.Status != StatusOff || v.HasGeo || v.ErrorCode != CodeOff || r.a.calls.Load() != 0 {
		t.Fatalf("%+v calls=%d", v, r.a.calls.Load())
	}
	r.choose(domain.GeoSourceAuto)
	r.get(exit)
	r.clock.Advance(25 * time.Hour)
	r.choose(domain.GeoSourceOff)
	v = r.get(exit)
	if !v.HasGeo || !v.Stale || v.ErrorCode != CodeOff || r.a.calls.Load() != 1 {
		t.Fatalf("stale data must stay visible while lookups are off: %+v", v)
	}
}

func TestEveryFailureBacksOffInsteadOfRetryingOnEachCheck(t *testing.T) {
	r := newRig(t)
	r.a.fail(503, nil)
	r.b.fail(429, map[string]string{"Retry-After": "600"})
	v := r.get(exit)
	if v.Status != StatusUnavailable || v.ErrorCode != CodeRateLimited || v.HasGeo {
		t.Fatalf("%+v", v)
	}
	if want := r.clock.Now().Add(10 * time.Minute); !v.RetryAt.Equal(want) {
		t.Fatalf("retry at %v, want the Retry-After of %v", v.RetryAt, want)
	}
	asked := r.a.calls.Load() + r.b.calls.Load()
	r.clock.Advance(5 * time.Minute)
	r.get(exit)
	if r.a.calls.Load()+r.b.calls.Load() != asked {
		t.Fatal("a source was asked again before the back-off ended")
	}
	r.clock.Advance(6 * time.Minute)
	r.a.fail(200, nil)
	if v := r.get(exit); v.Status != StatusFresh {
		t.Fatalf("recovery after the back-off: %+v", v)
	}
}

func TestBackoffGrowsWithRepeatedFailures(t *testing.T) {
	r := newRig(t)
	r.a.fail(503, nil)
	r.b.fail(503, nil)
	first := r.get(exit).RetryAt.Sub(r.clock.Now())
	r.clock.Advance(first + time.Second)
	second := r.get(exit).RetryAt.Sub(r.clock.Now())
	if second <= first {
		t.Fatalf("back-off %v then %v", first, second)
	}
}

func TestStaleDataSurvivesAFailedRefresh(t *testing.T) {
	r := newRig(t)
	r.get(exit)
	r.clock.Advance(25 * time.Hour)
	r.a.fail(503, nil)
	r.b.fail(503, nil)
	v := r.get(exit)
	if !v.HasGeo || !v.Stale || v.Status != StatusStale || v.ErrorCode != CodeUnavailable {
		t.Fatalf("%+v", v)
	}
}

func TestAReplyAboutAnotherAddressIsNotStored(t *testing.T) {
	r := newRig(t)
	r.a.answerAbout("198.51.100.1")
	r.b.answerAbout("198.51.100.1")
	v := r.get(exit)
	if v.HasGeo || v.ErrorCode != CodeInvalid {
		t.Fatalf("%+v", v)
	}
	if _, err := r.db.GetGeoCache(context.Background(), "198.51.100.1"); err == nil {
		t.Fatal("a record for a different address was cached")
	}
	if _, err := r.db.GetGeoCache(context.Background(), exit.String()); err == nil {
		t.Fatal("a record was cached for the wrong address")
	}
}

func TestConcurrentCallersShareOneLookup(t *testing.T) {
	r := newRig(t)
	r.a.gate = make(chan struct{})
	var wg sync.WaitGroup
	views := make([]GeoView, 20)
	for i := range views {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := r.svc.Get(context.Background(), exit, http.DefaultClient)
			if err != nil {
				t.Error(err)
			}
			views[i] = v
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(r.a.gate)
	wg.Wait()
	if r.a.calls.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", r.a.calls.Load())
	}
	for i, v := range views {
		if v.Status != StatusFresh {
			t.Fatalf("caller %d: %+v", i, v)
		}
	}
}

func TestCancelingTheFirstCallerDoesNotFailTheOthers(t *testing.T) {
	r := newRig(t)
	r.a.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := r.svc.Get(ctx, exit, http.DefaultClient); first <- err }()
	time.Sleep(50 * time.Millisecond)
	second := make(chan GeoView, 1)
	go func() { v, _ := r.svc.Get(context.Background(), exit, http.DefaultClient); second <- v }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(r.a.gate)
	<-first
	if v := <-second; v.Status != StatusFresh {
		t.Fatalf("%+v", v)
	}
}

func TestPrivateAddressesAreNeverLookedUp(t *testing.T) {
	r := newRig(t)
	for _, ip := range []string{"10.0.0.1", "192.168.1.1", "127.0.0.1", "169.254.1.1", "::1", "fe80::1"} {
		if _, err := r.svc.Get(context.Background(), netip.MustParseAddr(ip), http.DefaultClient); err == nil {
			t.Errorf("%s was accepted", ip)
		}
	}
	if r.a.calls.Load() != 0 {
		t.Fatal("a private address reached a source")
	}
}

func TestPeekNeverUsesTheNetwork(t *testing.T) {
	r := newRig(t)
	v, err := r.svc.Peek(context.Background(), exit)
	if err != nil || v.Status != StatusUnknown || v.HasGeo || r.a.calls.Load() != 0 {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestSourcesAreListedInTryOrder(t *testing.T) {
	r := newRig(t)
	got := r.svc.Sources()
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "beta" {
		t.Fatalf("%v", got)
	}
}
