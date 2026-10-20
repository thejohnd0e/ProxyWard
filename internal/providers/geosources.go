package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// GeoSource is a public service that describes the caller's own address, with
// no account or key. ProxyWard asks it through the proxy being checked, so the
// service sees only the proxy's exit address and never the address of the
// machine running ProxyWard.
type GeoSource struct {
	ID   string // stable identifier stored in the settings
	Name string // shown to the user
	Site string // home page, shown to the user
	URL  string // endpoint that answers about the caller
}

const (
	maxGeoBody    = 64 << 10
	maxGeoRunes   = 128
	maxASNInRange = 1<<32 - 1
)

// GeoSources returns the built-in sources in the order "auto" tries them.
func GeoSources() []GeoSource {
	return []GeoSource{
		{ID: "geojs", Name: "GeoJS", Site: "https://www.geojs.io/", URL: "https://get.geojs.io/v1/ip/geo.json"},
		{ID: "ipwhois", Name: "ipwho.is", Site: "https://ipwho.is/", URL: "https://ipwho.is/"},
		{ID: "ipapico", Name: "ipapi.co", Site: "https://ipapi.co/", URL: "https://ipapi.co/json/"},
		{ID: "ipsb", Name: "ip.sb", Site: "https://ip.sb/", URL: "https://api.ip.sb/geoip"},
		{ID: "ipinfo", Name: "ipinfo.io", Site: "https://ipinfo.io/", URL: "https://ipinfo.io/json"},
	}
}

// Lookup asks the source through client, which must route through the proxy
// whose exit address is want. A reply about any other address is rejected.
// Errors never contain response bodies.
func (g GeoSource) Lookup(ctx context.Context, client *http.Client, want netip.Addr) (domain.Geo, error) {
	want = want.Unmap()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.URL, nil)
	if err != nil {
		return domain.Geo{}, errors.New("build geolocation request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "proxyward")

	resp, err := client.Do(req)
	if err != nil {
		return domain.Geo{}, err
	}
	defer resp.Body.Close()

	invalid := func() error { return &Error{Provider: g.ID, Kind: KindInvalidResponse, Status: resp.StatusCode} }
	switch s := resp.StatusCode; {
	case s == http.StatusOK:
	case s == http.StatusTooManyRequests:
		return domain.Geo{}, &Error{Provider: g.ID, Kind: KindRateLimited, Status: s,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return domain.Geo{}, &Error{Provider: g.ID, Kind: KindForbidden, Status: s}
	case s >= 400:
		return domain.Geo{}, &Error{Provider: g.ID, Kind: KindUnavailable, Status: s}
	default:
		return domain.Geo{}, invalid()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGeoBody+1))
	if err != nil {
		return domain.Geo{}, err
	}
	if len(body) > maxGeoBody {
		return domain.Geo{}, invalid()
	}
	geo, err := ParseSelfGeo(body, want)
	if err != nil {
		return domain.Geo{}, invalid()
	}
	geo.FetchedAt = time.Now().UTC()
	return geo, nil
}

var asnText = regexp.MustCompile(`(?i)^AS(\d+)\s*(.*)$`)

// ParseSelfGeo reads the reply of any supported source. The services name
// their fields differently (country as a code or a name, ASN as a number or as
// "AS123 Name", nested or flat), so one tolerant reader covers all of them.
// The reply must be about want.
func ParseSelfGeo(body []byte, want netip.Addr) (domain.Geo, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return domain.Geo{}, err
	}
	if ok, present := m["success"].(bool); present && !ok {
		return domain.Geo{}, errors.New("the service reported a failure")
	}
	got, err := netip.ParseAddr(strings.TrimSpace(str(m["ip"])))
	if err != nil || got.Unmap() != want {
		return domain.Geo{}, errors.New("response is for a different address")
	}
	g := domain.Geo{IP: want.String()}

	country := cleanGeo(str(m["country"]))
	for _, key := range []string{"country_code", "countryCode"} {
		if cc := strings.ToUpper(strings.TrimSpace(str(m[key]))); len(cc) == 2 && isLetters(cc) {
			g.CountryCode = cc
		}
	}
	// Some services put the two-letter code in "country".
	if cc := strings.ToUpper(country); g.CountryCode == "" && len(cc) == 2 && isLetters(cc) {
		g.CountryCode = cc
		country = ""
	}
	g.Country = country
	if name := cleanGeo(str(m["country_name"])); name != "" {
		g.Country = name
	}
	g.City = cleanGeo(str(m["city"]))

	conn, _ := m["connection"].(map[string]any)
	if n := asnNumber(m["asn"]); n != nil {
		g.ASN = n
	} else if conn != nil {
		g.ASN = asnNumber(conn["asn"])
	}
	for _, v := range []any{m["asn"], m["organization"], m["org"]} {
		if s, ok := v.(string); ok {
			if mm := asnText.FindStringSubmatch(strings.TrimSpace(s)); mm != nil {
				if g.ASN == nil {
					g.ASN = asnNumber(mm[1])
				}
			}
		}
	}
	candidates := []string{str(m["organization_name"]), str(m["asn_organization"])}
	if conn != nil {
		candidates = append(candidates, str(conn["org"]), str(conn["isp"]))
	}
	candidates = append(candidates, stripASN(str(m["org"])), stripASN(str(m["organization"])), str(m["isp"]), stripASN(str(m["asn"])))
	for _, c := range candidates {
		if c = cleanGeo(c); c != "" {
			g.Organization = c
			break
		}
	}
	return g, nil
}

// stripASN removes a leading "AS123" from "AS123 Name"; a bare "AS123" gives "".
func stripASN(s string) string {
	if m := asnText.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return strings.TrimSpace(m[2])
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// asnNumber accepts a JSON number, "123" or "AS123 Name".
func asnNumber(v any) *uint32 {
	var text string
	switch x := v.(type) {
	case float64:
		text = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		text = strings.TrimSpace(x)
		if m := asnText.FindStringSubmatch(text); m != nil {
			text = m[1]
		}
	default:
		return nil
	}
	n, err := strconv.ParseUint(text, 10, 64)
	if err != nil || n == 0 || n > maxASNInRange {
		return nil
	}
	out := uint32(n)
	return &out
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// cleanGeo removes control characters, trims space and bounds the length.
func cleanGeo(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > maxGeoRunes {
		s = string(r[:maxGeoRunes])
	}
	return s
}

// String describes the source without any network access.
func (g GeoSource) String() string { return fmt.Sprintf("%s (%s)", g.Name, g.ID) }
