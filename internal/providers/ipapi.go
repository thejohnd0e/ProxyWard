package providers

import (
	"bytes"
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

// IPAPIURL is the ipapi.is endpoint. Lookups are POSTs so the key travels in
// the request body, never in a URL.
const IPAPIURL = "https://api.ipapi.is/"

const (
	ipapiTimeout  = 5 * time.Second
	ipapiMaxBody  = 64 << 10
	maxGeoRunes   = 128
	geoSource     = "ipapi"
	maxASNInRange = 1<<32 - 1
)

// ErrNotConfigured is returned when no geolocation key is available. No
// request is sent in that case: anonymous quota is never spent silently.
var ErrNotConfigured = errors.New("geolocation key is not configured")

// KeySource returns the API key, or ok=false when none is configured.
type KeySource func(ctx context.Context) (key string, ok bool, err error)

// IPAPI looks up geolocation and ASN data at ipapi.is. It talks to the
// service directly (never through a checked proxy) so a lookup can never be
// confused with, or influence, a proxy check.
type IPAPI struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration
	Key     KeySource
}

// NewIPAPI returns a client with production defaults: direct connection,
// environment proxies ignored, redirects not followed, 5 s timeout.
func NewIPAPI(key KeySource) *IPAPI {
	return &IPAPI{
		URL:     IPAPIURL,
		Timeout: ipapiTimeout,
		Key:     key,
		Client: &http.Client{
			Transport: &http.Transport{
				Proxy:                  nil,
				TLSHandshakeTimeout:    ipapiTimeout,
				ResponseHeaderTimeout:  ipapiTimeout,
				MaxResponseHeaderBytes: 16 << 10,
				DisableKeepAlives:      true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (g *IPAPI) invalid(status int) error {
	return &Error{Provider: geoSource, Kind: KindInvalidResponse, Status: status}
}

// LookupGeo returns geolocation for a public address. Errors never contain
// the key or response bodies.
func (g *IPAPI) LookupGeo(ctx context.Context, addr netip.Addr) (domain.Geo, error) {
	if g.Key == nil {
		return domain.Geo{}, ErrNotConfigured
	}
	key, ok, err := g.Key(ctx)
	if err != nil {
		return domain.Geo{}, fmt.Errorf("read geolocation key: %w", err)
	}
	if !ok || strings.TrimSpace(key) == "" {
		return domain.Geo{}, ErrNotConfigured
	}
	addr = addr.Unmap()

	timeout := g.Timeout
	if timeout <= 0 {
		timeout = ipapiTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]string{"q": addr.String(), "key": strings.TrimSpace(key)})
	if err != nil {
		return domain.Geo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL, bytes.NewReader(payload))
	if err != nil {
		return domain.Geo{}, errors.New("build geolocation request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "proxyward")

	client := g.Client
	if client == nil {
		client = NewIPAPI(nil).Client
	}
	resp, err := client.Do(req)
	if err != nil {
		return domain.Geo{}, err
	}
	defer resp.Body.Close()

	switch s := resp.StatusCode; {
	case s == http.StatusOK:
	case s == http.StatusTooManyRequests:
		return domain.Geo{}, &Error{Provider: geoSource, Kind: KindRateLimited, Status: s,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return domain.Geo{}, &Error{Provider: geoSource, Kind: KindForbidden, Status: s}
	case s == http.StatusBadRequest:
		return domain.Geo{}, g.invalid(s)
	case s >= 400:
		return domain.Geo{}, &Error{Provider: geoSource, Kind: KindUnavailable, Status: s}
	default:
		return domain.Geo{}, g.invalid(s)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ipapiMaxBody+1))
	if err != nil {
		return domain.Geo{}, err
	}
	if len(body) > ipapiMaxBody {
		return domain.Geo{}, g.invalid(resp.StatusCode)
	}
	geo, err := parseGeo(body, addr)
	if err != nil {
		return domain.Geo{}, g.invalid(resp.StatusCode)
	}
	geo.FetchedAt = time.Now().UTC()
	return geo, nil
}

var asnText = regexp.MustCompile(`(?i)^AS(\d+)\s*(.*)$`)

func parseGeo(body []byte, want netip.Addr) (domain.Geo, error) {
	var v struct {
		IP       string          `json:"ip"`
		ASN      json.RawMessage `json:"asn"`
		Location *struct {
			Country     *string `json:"country"`
			CountryCode *string `json:"country_code"`
			City        *string `json:"city"`
		} `json:"location"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return domain.Geo{}, err
	}
	got, err := netip.ParseAddr(strings.TrimSpace(v.IP))
	if err != nil || got.Unmap() != want {
		return domain.Geo{}, errors.New("response is for a different address")
	}
	g := domain.Geo{IP: want.String()}
	if v.Location != nil {
		g.Country = cleanGeo(deref(v.Location.Country))
		g.City = cleanGeo(deref(v.Location.City))
		if cc := strings.ToUpper(strings.TrimSpace(deref(v.Location.CountryCode))); len(cc) == 2 && isLetters(cc) {
			g.CountryCode = cc
		}
	}
	raw := bytes.TrimSpace(v.ASN)
	switch {
	case len(raw) == 0 || string(raw) == "null":
	case raw[0] == '{':
		var a struct {
			ASN json.Number `json:"asn"`
			Org *string     `json:"org"`
		}
		if err := json.Unmarshal(raw, &a); err == nil {
			g.ASN = asnNumber(a.ASN.String())
			g.Organization = cleanGeo(deref(a.Org))
		}
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if m := asnText.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
				g.ASN = asnNumber(m[1])
				g.Organization = cleanGeo(m[2])
			}
		}
	}
	return g, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func asnNumber(s string) *uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil || n > maxASNInRange {
		return nil
	}
	v := uint32(n)
	return &v
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
