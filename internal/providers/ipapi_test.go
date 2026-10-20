package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

const testKey = "k3y-VALUE-should-never-leak"

type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (c *capture) handler(status int, resp string, hdr map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, r)
		c.body = append(c.body, string(b))
		c.mu.Unlock()
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, resp)
	}
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func geoRig(t *testing.T, status int, resp string, hdr map[string]string) (*IPAPI, *capture) {
	t.Helper()
	cap := &capture{}
	tg, client := serve(t, cap.handler(status, resp, hdr))
	return &IPAPI{URL: tg.URL + "/", Client: client, Key: func(context.Context) (string, bool, error) { return testKey, true, nil }}, cap
}

const keyed = `{"ip":"1.1.1.1","is_bogon":false,
 "asn":{"asn":13335,"route":"1.1.1.0/24","descr":"CLOUDFLARENET","org":"Cloudflare, Inc.","country":"US"},
 "location":{"country":"Australia","country_code":"AU","state":"Queensland","city":"Brisbane","latitude":-27.4}}`

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestLookupGeoParsesKeyedResponseAndSendsKeyInBodyOnly(t *testing.T) {
	g, cap := geoRig(t, 200, keyed, nil)
	got, err := g.LookupGeo(context.Background(), ip("1.1.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.IP != "1.1.1.1" || got.CountryCode != "AU" || got.Country != "Australia" || got.City != "Brisbane" ||
		got.Organization != "Cloudflare, Inc." || got.ASN == nil || *got.ASN != 13335 || got.FetchedAt.IsZero() {
		t.Fatalf("%+v", got)
	}
	if cap.count() != 1 {
		t.Fatalf("requests = %d", cap.count())
	}
	r := cap.reqs[0]
	if r.Method != "POST" || r.URL.RawQuery != "" || strings.Contains(r.URL.String(), testKey) {
		t.Fatalf("key leaked into the URL or wrong method: %s %s", r.Method, r.URL)
	}
	for k, v := range r.Header {
		if strings.Contains(strings.Join(v, ","), testKey) {
			t.Fatalf("key in header %s", k)
		}
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(cap.body[0]), &body); err != nil || body["q"] != "1.1.1.1" || body["key"] != testKey || len(body) != 2 {
		t.Fatalf("body = %q", cap.body[0])
	}
}

func TestLookupGeoToleratesNullsAndMissingFields(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want func(g string) bool
	}{
		"null asn":      {`{"ip":"8.8.8.8","asn":null,"location":{"country":"United States","country_code":"US","city":null}}`, nil},
		"missing asn":   {`{"ip":"8.8.8.8","location":{"country_code":"US"}}`, nil},
		"missing loc":   {`{"ip":"8.8.8.8","asn":{"asn":15169,"org":"Google LLC"}}`, nil},
		"empty objects": {`{"ip":"8.8.8.8","asn":{},"location":{}}`, nil},
		"string asn":    {`{"ip":"8.8.8.8","asn":"AS15169 Google LLC"}`, nil},
	} {
		g, _ := geoRig(t, 200, tc.body, nil)
		got, err := g.LookupGeo(context.Background(), ip("8.8.8.8"))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got.IP != "8.8.8.8" {
			t.Errorf("%s: %+v", name, got)
		}
		switch name {
		case "null asn", "missing asn", "empty objects":
			if got.ASN != nil {
				t.Errorf("%s: ASN = %v", name, *got.ASN)
			}
		case "missing loc":
			if got.Country != "" || got.CountryCode != "" || got.City != "" || got.ASN == nil || *got.ASN != 15169 {
				t.Errorf("%s: %+v", name, got)
			}
		case "string asn":
			if got.ASN == nil || *got.ASN != 15169 || got.Organization != "Google LLC" {
				t.Errorf("%s: %+v", name, got)
			}
		}
	}
}

func TestLookupGeoSanitizesFields(t *testing.T) {
	long := strings.Repeat("x", 500)
	g, _ := geoRig(t, 200, `{"ip":"8.8.8.8","asn":{"asn":4294967296,"org":"`+long+`"},"location":{"country":"A\u0000B\nC","country_code":"usa","city":"  Paris  "}}`, nil)
	got, err := g.LookupGeo(context.Background(), ip("8.8.8.8"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ASN != nil {
		t.Errorf("out of range ASN accepted: %d", *got.ASN)
	}
	if got.CountryCode != "" {
		t.Errorf("malformed country code kept: %q", got.CountryCode)
	}
	if got.City != "Paris" || strings.ContainsAny(got.Country, "\x00\n") || len([]rune(got.Organization)) > 128 {
		t.Errorf("%+v", got)
	}
}

func TestLookupGeoRejectsInvalidResponses(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"ip mismatch": {200, `{"ip":"9.9.9.9"}`},
		"no ip":       {200, `{"asn":null}`},
		"bad ip":      {200, `{"ip":"nope"}`},
		"not json":    {200, `<html>`},
		"empty":       {200, ``},
		"array":       {200, `[]`},
		"oversize":    {200, `{"ip":"8.8.8.8","pad":"` + strings.Repeat("x", 70*1024) + `"}`},
		"error body":  {400, `{"error":"ERR_INVALID_IP_OR_ASN"}`},
		"redirect":    {302, ``},
	} {
		g, _ := geoRig(t, tc.status, tc.body, map[string]string{"Location": "https://elsewhere.example/"})
		_, err := g.LookupGeo(context.Background(), ip("8.8.8.8"))
		assertKind(t, name, err, KindInvalidResponse)
	}
}

func TestLookupGeoStatusMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		kind   Kind
	}{
		"429": {429, KindRateLimited},
		"403": {403, KindForbidden},
		"401": {401, KindForbidden},
		"500": {500, KindUnavailable},
		"503": {503, KindUnavailable},
		"404": {404, KindUnavailable},
	} {
		g, _ := geoRig(t, tc.status, `{"error":"`+testKey+`"}`, map[string]string{"Retry-After": "90"})
		_, err := g.LookupGeo(context.Background(), ip("8.8.8.8"))
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != tc.kind || pe.Status != tc.status {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: error leaks upstream text", name)
		}
		if tc.status == 429 && pe.RetryAfter != 90*time.Second {
			t.Errorf("retry after = %v", pe.RetryAfter)
		}
	}
}

func TestLookupGeoWithoutKeyMakesNoRequest(t *testing.T) {
	g, cap := geoRig(t, 200, keyed, nil)
	g.Key = func(context.Context) (string, bool, error) { return "", false, nil }
	if _, err := g.LookupGeo(context.Background(), ip("1.1.1.1")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	g.Key = func(context.Context) (string, bool, error) { return "  ", true, nil }
	if _, err := g.LookupGeo(context.Background(), ip("1.1.1.1")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("blank key err = %v", err)
	}
	g.Key = nil
	if _, err := g.LookupGeo(context.Background(), ip("1.1.1.1")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nil key source err = %v", err)
	}
	if cap.count() != 0 {
		t.Fatalf("%d requests were sent without a key", cap.count())
	}
}

func TestLookupGeoKeySourceErrorIsNotConfiguredFree(t *testing.T) {
	g, cap := geoRig(t, 200, keyed, nil)
	boom := errors.New("decrypt failed")
	g.Key = func(context.Context) (string, bool, error) { return "", false, boom }
	if _, err := g.LookupGeo(context.Background(), ip("1.1.1.1")); !errors.Is(err, boom) || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if cap.count() != 0 {
		t.Fatal("request sent despite a key error")
	}
}

func TestLookupGeoTimeout(t *testing.T) {
	tg, client := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) })
	g := &IPAPI{URL: tg.URL + "/", Client: client, Timeout: 100 * time.Millisecond,
		Key: func(context.Context) (string, bool, error) { return testKey, true, nil }}
	start := time.Now()
	_, err := g.LookupGeo(context.Background(), ip("8.8.8.8"))
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("timeout error leaks the key")
	}
}

func TestLookupGeoDefaultsAreSafe(t *testing.T) {
	g := NewIPAPI(func(context.Context) (string, bool, error) { return testKey, true, nil })
	if g.URL != IPAPIURL || !strings.HasPrefix(g.URL, "https://") || g.Timeout != 5*time.Second {
		t.Fatalf("%+v", g)
	}
	if g.Client == nil || g.Client.CheckRedirect == nil {
		t.Fatal("client must not follow redirects")
	}
	if tr, ok := g.Client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatalf("geo client must be direct and ignore environment proxies: %#v", g.Client.Transport)
	}
}

func TestRetryAtAndBackoff(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if got := RetryAt("Mon, 19 Oct 2026 12:01:00 GMT", now); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry=%v", got)
	}
	if got := RetryAt("120", now); !got.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("seconds retry=%v", got)
	}
	if got := RetryAt("garbage", now); !got.IsZero() {
		t.Fatalf("invalid header gave %v", got)
	}
	for attempt, want := range map[int]time.Duration{
		0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 8 * time.Minute,
		5: 16 * time.Minute, 6: 32 * time.Minute, 7: time.Hour, 20: time.Hour, 1000: time.Hour,
	} {
		if got := Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

var _ = testkit.TargetName
