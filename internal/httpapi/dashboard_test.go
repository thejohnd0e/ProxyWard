package httpapi

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/geo"
)

type fakePeek struct {
	views map[netip.Addr]geo.GeoView
}

func (f fakePeek) Peek(_ context.Context, ip netip.Addr) (geo.GeoView, error) {
	if v, ok := f.views[ip]; ok {
		return v, nil
	}
	return geo.GeoView{Status: geo.StatusUnknown}, nil
}

func f64(v float64) *float64 { return &v }

func addResults(t *testing.T, s *session, id int64) {
	t.Helper()
	ctx := context.Background()
	now := s.e.clock.Now()
	pid := domain.ProxyID(id)
	states := []domain.State{domain.StateUp, domain.StateUp, domain.StateDown, domain.StateIndeterminate, domain.StateUp}
	for i, st := range states {
		a := domain.Availability{ProxyID: pid, CheckedAt: now.Add(-time.Duration(len(states)-i) * time.Hour), State: st,
			Endpoint: "ipify", Duration: time.Duration(120+i) * time.Millisecond}
		switch st {
		case domain.StateUp:
			a.ExitIP = "198.51.100.7"
		case domain.StateDown:
			a.Failure = &domain.Failure{Code: "auth_failed", Message: "The proxy rejected the credentials."}
		}
		if err := s.e.db.AddAvailability(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		sp := domain.Speed{ProxyID: pid, CheckedAt: now.Add(-time.Duration(2-i) * time.Hour), Provider: "cloudflare", Method: "app-stream-v1",
			Download: domain.Transfer{Bytes: 8 << 20, Duration: 2 * time.Second, Mbps: f64(float64(30 + i))},
			Upload: domain.Transfer{Bytes: 100, Duration: 10 * time.Millisecond, LowConfidence: true,
				Failure: &domain.Failure{Code: "invalid_response", Message: "The response was not valid."}},
			TotalBytes: 9 << 20}
		if err := s.e.db.AddSpeed(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProxyListCarriesStatusSpeedGeoAndUptime(t *testing.T) {
	s := newSession(t)
	asn := uint32(64500)
	s.e.geoViews = map[netip.Addr]geo.GeoView{
		netip.MustParseAddr("198.51.100.7"): {HasGeo: true, Status: geo.StatusStale, Stale: true, Geo: domain.Geo{
			IP: "198.51.100.7", CountryCode: "DE", Country: "Germany", City: "Berlin", Organization: "Example GmbH", ASN: &asn,
			FetchedAt: s.e.clock.Now().Add(-30 * time.Hour)}},
	}
	p := s.create(m{"name": "A", "protocol": "http", "host": "a.lan", "port": 1})
	bare := s.create(m{"name": "B", "protocol": "http", "host": "b.lan", "port": 1})
	id := int64(p["id"].(float64))
	addResults(t, s, id)

	list := decode(t, s.call("GET", "/api/proxies", nil))
	if list["geo_configured"] != false {
		t.Fatalf("%v", list["geo_configured"])
	}
	var a, b m
	for _, it := range list["proxies"].([]any) {
		if it.(m)["id"] == p["id"] {
			a = it.(m)
		} else if it.(m)["id"] == bare["id"] {
			b = it.(m)
		}
	}
	st := a["status"].(m)
	if st["state"] != "up" || st["exit_ip"] != "198.51.100.7" || st["endpoint"] != "ipify" || st["duration_ms"] != float64(124) || st["failure"] != nil || st["checked_at"] == nil {
		t.Fatalf("status: %v", st)
	}
	sp := a["speed"].(m)
	dl, ul := sp["download"].(m), sp["upload"].(m)
	if dl["mbps"] != float64(31) || dl["low_confidence"] != false || dl["failure"] != nil || dl["bytes"] != float64(8<<20) || dl["duration_ms"] != float64(2000) {
		t.Fatalf("download: %v", dl)
	}
	if ul["mbps"] != nil || ul["low_confidence"] != true || sub(t, ul, "failure", "code") != "invalid_response" {
		t.Fatalf("a failed direction must have no rate: %v", ul)
	}
	g := a["geo"].(m)
	if g["country_code"] != "DE" || g["country"] != "Germany" || g["city"] != "Berlin" || g["asn"] != float64(64500) ||
		g["organization"] != "Example GmbH" || g["stale"] != true || g["status"] != "stale" || g["fetched_at"] == nil {
		t.Fatalf("geo: %v", g)
	}
	up := a["uptime"].(m)
	if up["percent"] != float64(75) || up["samples"] != float64(4) || up["window_hours"] != float64(24) {
		t.Fatalf("uptime: %v", up)
	}

	// A proxy that was never checked: unknown, with no invented values.
	bs := b["status"].(m)
	if bs["state"] != "unknown" || bs["checked_at"] != nil || bs["failure"] != nil || b["speed"] != nil || b["geo"] != nil {
		t.Fatalf("%v", b)
	}
	if bu := b["uptime"].(m); bu["percent"] != nil || bu["samples"] != float64(0) {
		t.Fatalf("%v", bu)
	}
	if jobs, ok := b["active_jobs"].([]any); !ok || len(jobs) != 0 {
		t.Fatalf("active_jobs must be an empty list: %v", b["active_jobs"])
	}
}

func TestDecoratedFieldsAreAlsoOnDetailAndNeverLeakSecrets(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"name": "A", "protocol": "http", "host": "a.lan", "port": 1, "auth_enabled": true, "username": "u", "password": "Dash-Secret-1"})
	addResults(t, s, int64(p["id"].(float64)))
	r := s.call("GET", idPath(p), nil)
	d := decode(t, r)
	if d["status"].(m)["state"] != "up" || d["speed"] == nil || d["uptime"] == nil || d["next_runs"] == nil {
		t.Fatalf("%v", d)
	}
	if strings.Contains(r.body, "Dash-Secret-1") {
		t.Fatal("secret in detail")
	}
}

func TestActiveJobsAndNextRunsAreShown(t *testing.T) {
	s, _, ids := jobSession(t)
	s.call("POST", "/api/jobs", m{"kind": "speed", "proxy_ids": ids[:1]})
	next := s.e.clock.Now().Add(time.Hour)
	_ = s.e.db.SetNextRun(context.Background(), domain.ProxyID(ids[0]), domain.CheckAvailability, &next)
	list := decode(t, s.call("GET", "/api/proxies", nil))["proxies"].([]any)
	first := list[0].(m)
	jobs := first["active_jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(m)["kind"] != "speed" || jobs[0].(m)["status"] != "pending" {
		t.Fatalf("%v", first["active_jobs"])
	}
	nr := first["next_runs"].(m)
	if nr["availability"] == nil || nr["speed"] != nil {
		t.Fatalf("%v", nr)
	}
	if other := list[1].(m)["active_jobs"].([]any); len(other) != 0 {
		t.Fatalf("%v", other)
	}
}

func TestGeoConfiguredFlag(t *testing.T) {
	s := newSession(t)
	s.call("PUT", "/api/settings", m{"geo": m{"api_key": "a-valid-geo-key-1"}})
	if decode(t, s.call("GET", "/api/proxies", nil))["geo_configured"] != true {
		t.Fatal("geo_configured not reported")
	}
}

func TestHistoryEndpoint(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"name": "H", "protocol": "http", "host": "h.lan", "port": 1})
	id := int64(p["id"].(float64))
	addResults(t, s, id)
	path := fmt.Sprintf("/api/proxies/%d/history", id)

	r := s.call("GET", path+"?limit=2", nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	pg := decode(t, r)
	items := pg["items"].([]any)
	if pg["kind"] != "availability" || len(items) != 2 || pg["next_cursor"] == nil {
		t.Fatalf("%s", r.body)
	}
	newest := items[0].(m)
	if newest["state"] != "up" || newest["exit_ip"] != "198.51.100.7" || newest["endpoint"] != "ipify" || newest["checked_at"] == nil {
		t.Fatalf("%v", newest)
	}
	var seen int
	cursor := ""
	for i := 0; i < 5; i++ {
		q := path + "?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		got := decode(t, s.call("GET", q, nil))
		seen += len(got["items"].([]any))
		if got["next_cursor"] == nil {
			break
		}
		cursor = got["next_cursor"].(string)
	}
	if seen != 5 {
		t.Fatalf("paging saw %d of 5 results", seen)
	}
	down := decode(t, s.call("GET", path+"?limit=10", nil))["items"].([]any)
	if sub(t, down[2].(m), "failure", "code") != "auth_failed" {
		t.Fatalf("%v", down[2])
	}

	sp := decode(t, s.call("GET", path+"?kind=speed", nil))
	item := sp["items"].([]any)[0].(m)
	if sp["kind"] != "speed" || item["provider"] != "cloudflare" || item["method"] != "app-stream-v1" || item["total_bytes"] != float64(9<<20) ||
		sub(t, item, "download", "mbps") != float64(31) || sub(t, item, "upload", "mbps") != nil || sub(t, item, "upload", "low_confidence") != true {
		t.Fatalf("%v", item)
	}

	now := s.e.clock.Now()
	from := now.Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	ranged := decode(t, s.call("GET", path+"?from="+from, nil))["items"].([]any)
	if len(ranged) != 3 {
		t.Fatalf("range filter: %d", len(ranged))
	}
}

func TestHistoryValidation(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"name": "H", "protocol": "http", "host": "h.lan", "port": 1})
	path := fmt.Sprintf("/api/proxies/%d/history", int64(p["id"].(float64)))
	for name, q := range map[string]string{
		"bad kind": "kind=ping", "limit 0": "limit=0", "limit 201": "limit=201", "limit text": "limit=x",
		"bad from": "from=yesterday", "bad to": "to=2026-13-45", "bad cursor": "cursor=zzz", "from after to": "from=2026-05-02T00:00:00Z&to=2026-05-01T00:00:00Z",
	} {
		if r := s.call("GET", path+"?"+q, nil); r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	for _, bad := range []string{"/api/proxies/999/history", "/api/proxies/abc/history", "/api/proxies/999/chart"} {
		if r := s.call("GET", bad, nil); r.status != 404 {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
	if r := s.call("GET", path+"?limit=200", nil); r.status != 200 {
		t.Errorf("limit 200: %d", r.status)
	}
}

func TestChartEndpointKeepsGapsAndBoundsPoints(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"name": "C", "protocol": "http", "host": "c.lan", "port": 1})
	id := int64(p["id"].(float64))
	addResults(t, s, id)
	path := fmt.Sprintf("/api/proxies/%d/chart", id)
	now := s.e.clock.Now()
	q := fmt.Sprintf("?from=%s&to=%s&points=6", now.Add(-6*time.Hour).UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))

	r := s.call("GET", path+q, nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	ch := decode(t, r)
	pts := ch["points"].([]any)
	if ch["kind"] != "availability" || len(pts) != 6 || ch["interval_seconds"] != float64(3600) {
		t.Fatalf("%s", r.body)
	}
	if first := pts[0].(m); first["samples"] != float64(0) || first["avg_duration_ms"] != nil || first["t"] == nil {
		t.Fatalf("a bucket without results is a gap: %v", first)
	}
	if second := pts[1].(m); second["up"] != float64(1) || second["samples"] != float64(1) {
		t.Fatalf("%v", second)
	}
	if down := pts[3].(m); down["down"] != float64(1) {
		t.Fatalf("%v", down)
	}

	sp := decode(t, s.call("GET", path+q+"&kind=speed", nil))["points"].([]any)
	if len(sp) != 6 {
		t.Fatalf("%d", len(sp))
	}
	var withDownload, withUpload int
	for _, pt := range sp {
		if pt.(m)["download_mbps"] != nil {
			withDownload++
		}
		if pt.(m)["upload_mbps"] != nil {
			withUpload++
		}
	}
	if withDownload != 2 || withUpload != 0 {
		t.Fatalf("download points %d (want 2), upload points %d (want 0: failed samples are gaps, not zeros)", withDownload, withUpload)
	}

	// Defaults: the last 24 hours in at most 500 points.
	def := decode(t, s.call("GET", path, nil))
	if n := len(def["points"].([]any)); n < 1 || n > 500 {
		t.Fatalf("%d points", n)
	}
	for name, bad := range map[string]string{
		"points 0": "points=0", "points 501": "points=501", "range too long": "from=2020-01-01T00:00:00Z&to=2026-05-01T00:00:00Z",
		"inverted": "from=2026-05-02T00:00:00Z&to=2026-05-01T00:00:00Z", "bad kind": "kind=x",
	} {
		if r := s.call("GET", path+"?"+bad, nil); r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := s.call("GET", path+"?points=500", nil); r.status != 200 {
		t.Errorf("500 points: %d", r.status)
	}
}

type peekFunc func(ctx context.Context, ip netip.Addr) (geo.GeoView, error)

func (f peekFunc) Peek(ctx context.Context, ip netip.Addr) (geo.GeoView, error) { return f(ctx, ip) }
