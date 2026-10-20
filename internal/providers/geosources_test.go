package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

var exitIP = netip.MustParseAddr("194.50.111.34")

// Replies shaped like the real services answer (checked through a proxy on 2026-10-21).
var samples = map[string]string{
	"geojs":   `{"accuracy":20,"asn":23470,"city":"Miami","continent_code":"NA","country":"United States","country_code":"US","ip":"194.50.111.34","organization":"AS23470 ReliableSite.Net LLC","organization_name":"ReliableSite.Net LLC","region":"Florida"}`,
	"ipwhois": `{"ip":"194.50.111.34","success":true,"country":"United States","country_code":"US","region":"Florida","city":"Miami","connection":{"asn":23470,"org":"InterLIR GmbH","isp":"ReliableSite.Net LLC"}}`,
	"ipapico": `{"ip":"194.50.111.34","city":"Miami","country":"US","country_name":"United States","country_code":"US","asn":"AS23470","org":"ReliableSite.Net LLC"}`,
	"ipsb":    `{"organization":"ReliableSite.Net","city":"Miami","asn":23470,"ip":"194.50.111.34","country":"United States","country_code":"US","asn_organization":"ReliableSite.Net LLC"}`,
	"ipinfo":  `{"ip":"194.50.111.34","city":"Miami","region":"Florida","country":"US","org":"AS23470 ReliableSite.Net LLC"}`,
}

func TestEverySourceParsesToTheSameCountryCityAndNetwork(t *testing.T) {
	for id, body := range samples {
		g, err := ParseSelfGeo([]byte(body), exitIP)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if g.CountryCode != "US" || g.City != "Miami" || g.ASN == nil || *g.ASN != 23470 || g.Organization == "" {
			t.Errorf("%s: %+v", id, g)
		}
		if !strings.HasPrefix(g.Country, "United States") && id != "ipinfo" {
			t.Errorf("%s: country %q", id, g.Country)
		}
	}
}

func TestEverySourceIsKnownAndHasNoSecrets(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range GeoSources() {
		if seen[s.ID] {
			t.Fatalf("duplicate id %s", s.ID)
		}
		seen[s.ID] = true
		if _, ok := samples[s.ID]; !ok {
			t.Errorf("no parser sample for %s", s.ID)
		}
		if !strings.HasPrefix(s.URL, "https://") || strings.ContainsAny(s.URL, "?=") {
			t.Errorf("%s: url must be plain https without parameters: %s", s.ID, s.URL)
		}
	}
}

func TestReplyAboutAnotherAddressIsRejected(t *testing.T) {
	for id, body := range samples {
		if _, err := ParseSelfGeo([]byte(body), netip.MustParseAddr("203.0.113.9")); err == nil {
			t.Errorf("%s accepted a reply for a different address", id)
		}
	}
	if _, err := ParseSelfGeo([]byte(`{"ip":"194.50.111.34","success":false}`), exitIP); err == nil {
		t.Error("a failure reply was accepted")
	}
	for _, bad := range []string{``, `nope`, `[]`, `{}`, `{"ip":"not an ip"}`} {
		if _, err := ParseSelfGeo([]byte(bad), exitIP); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDualStackReplyOfTheOtherFamilyIsAccepted(t *testing.T) {
	g, err := ParseSelfGeo([]byte(`{"ip":"2a09:bac5:5166:505::80:187","country_code":"RU","city":"St Petersburg"}`), netip.MustParseAddr("104.28.196.105"))
	if err != nil || g.IP != "104.28.196.105" || g.CountryCode != "RU" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := ParseSelfGeo([]byte(`{"ip":"fd00::1","country_code":"RU"}`), exitIP); err == nil {
		t.Error("a private address of the other family was accepted")
	}
	if _, err := ParseSelfGeo([]byte(`{"ip":"198.51.100.1","country_code":"RU"}`), exitIP); err == nil {
		t.Error("another IPv4 address was accepted")
	}
}

func TestMissingFieldsStayEmptyInsteadOfInvented(t *testing.T) {
	g, err := ParseSelfGeo([]byte(`{"ip":"194.50.111.34","country_code":"us"}`), exitIP)
	if err != nil || g.CountryCode != "US" || g.City != "" || g.ASN != nil || g.Organization != "" || g.Country != "" {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestTextFromTheServiceIsCleaned(t *testing.T) {
	long := strings.Repeat("x", 500)
	g, err := ParseSelfGeo([]byte(`{"ip":"194.50.111.34","city":"Mi\u0007ami\n","country_code":"US","organization_name":"`+long+`"}`), exitIP)
	if err != nil || g.City != "Miami" || len([]rune(g.Organization)) != maxGeoRunes {
		t.Fatalf("%q %d %v", g.City, len([]rune(g.Organization)), err)
	}
}

func lookupAgainst(t *testing.T, status int, body string, hdr map[string]string) (error, string) {
	t.Helper()
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	src := GeoSource{ID: "test", URL: srv.URL}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	_, err := src.Lookup(context.Background(), client, exitIP)
	return err, gotUA
}

func TestLookupClassifiesProviderFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		hdr    map[string]string
		kind   Kind
	}{
		{"rate limit", 429, "", map[string]string{"Retry-After": "120"}, KindRateLimited},
		{"forbidden", 403, "", nil, KindForbidden},
		{"server error", 503, "", nil, KindUnavailable},
		{"redirect", 302, "", map[string]string{"Location": "/x"}, KindInvalidResponse},
		{"garbage", 200, "<html>", nil, KindInvalidResponse},
		{"other address", 200, `{"ip":"203.0.113.9"}`, nil, KindInvalidResponse},
	}
	for _, c := range cases {
		err, _ := lookupAgainst(t, c.status, c.body, c.hdr)
		var pe *Error
		if err == nil || !errors.As(err, &pe) || pe.Kind != c.kind {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && strings.Contains(err.Error(), "<html>") {
			t.Errorf("%s: the body leaked into the error", c.name)
		}
	}
	err, ua := lookupAgainst(t, 200, samples["geojs"], nil)
	if err != nil || ua != "proxyward" {
		t.Fatalf("ok lookup: %v ua=%q", err, ua)
	}
}

func TestGeoSourceRateLimitCarriesRetryAfter(t *testing.T) {
	err, _ := lookupAgainst(t, 429, "", map[string]string{"Retry-After": "120"})
	var pe *Error
	if !errors.As(err, &pe) || pe.RetryAfter.Seconds() != 120 {
		t.Fatalf("%v", err)
	}
}
