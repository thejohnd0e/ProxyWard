// Package providers talks to public services used by the checks.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Default endpoints (verified in docs/research/2026-10-19-public-services.md).
const (
	// IPifyURL and AWSURL are IPv4-only hosts, so the exit address is an IPv4
	// one whenever the proxy has any.
	IPifyURL = "https://api.ipify.org?format=json"
	AWSURL   = "https://checkip.amazonaws.com/"
	// IPifyDualURL answers over IPv6 when the proxy prefers it. It is the last
	// resort, for proxies that can only reach the internet over IPv6.
	IPifyDualURL = "https://api64.ipify.org?format=json"
)

// maxBodyBytes caps the response body read from IP providers.
const maxBodyBytes = 4096

// IPProvider reports the public address a request appears to come from. The
// client decides how the request travels (through the proxy under test).
type IPProvider interface {
	Name() string
	Lookup(ctx context.Context, client *http.Client) (netip.Addr, error)
}

// Kind classifies provider-side failures.
type Kind int

const (
	// KindRateLimited is an HTTP 429.
	KindRateLimited Kind = iota + 1
	// KindUnavailable is a 4xx/5xx status other than 429.
	KindUnavailable
	// KindInvalidResponse is an unexpected status, body or exit address.
	KindInvalidResponse
	// KindForbidden is an HTTP 401 or 403: the credentials or key are rejected.
	KindForbidden
)

func (k Kind) String() string {
	switch k {
	case KindRateLimited:
		return "rate limited"
	case KindUnavailable:
		return "unavailable"
	case KindInvalidResponse:
		return "invalid response"
	case KindForbidden:
		return "forbidden"
	}
	return "unknown"
}

// Error is a failure reported by, or attributable to, a provider. It carries
// no response body text.
type Error struct {
	Provider   string
	Kind       Kind
	Status     int           // HTTP status when known
	RetryAfter time.Duration // from Retry-After on 429
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d)", e.Provider, e.Kind, e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Provider, e.Kind)
}

type provider struct {
	name  string
	url   string
	parse func(body []byte) (string, error)
}

func (p *provider) Name() string { return p.name }

// NewIPify returns a provider for the ipify JSON API at url.
func NewIPify(url string) IPProvider { return &provider{name: "ipify", url: url, parse: parseIPify} }

// NewAWS returns a provider for the AWS checkip text API at url.
func NewAWS(url string) IPProvider { return &provider{name: "aws", url: url, parse: parseAWS} }

// DefaultIPify uses the production ipify endpoint.
func DefaultIPify() IPProvider { return NewIPify(IPifyURL) }

// DefaultIPifyDual uses the dual-stack ipify endpoint.
func DefaultIPifyDual() IPProvider {
	return &provider{name: "ipify-dual", url: IPifyDualURL, parse: parseIPify}
}

// DefaultAWS uses the production AWS checkip endpoint.
func DefaultAWS() IPProvider { return NewAWS(AWSURL) }

func (p *provider) invalid(status int) error {
	return &Error{Provider: p.name, Kind: KindInvalidResponse, Status: status}
}

func (p *provider) Lookup(ctx context.Context, client *http.Client) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	req.Header.Set("User-Agent", "proxyward")
	req.Header.Set("Accept", "application/json, text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err // transport errors keep their type for classification
	}
	defer resp.Body.Close()

	switch status := resp.StatusCode; {
	case status == http.StatusOK:
	case status == http.StatusTooManyRequests:
		return netip.Addr{}, &Error{Provider: p.name, Kind: KindRateLimited, Status: status,
			RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case status >= 400:
		return netip.Addr{}, &Error{Provider: p.name, Kind: KindUnavailable, Status: status}
	default:
		return netip.Addr{}, p.invalid(status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return netip.Addr{}, err
	}
	if len(body) > maxBodyBytes {
		return netip.Addr{}, p.invalid(resp.StatusCode)
	}
	text, err := p.parse(body)
	if err != nil {
		return netip.Addr{}, p.invalid(resp.StatusCode)
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil || !IsPublic(addr) {
		return netip.Addr{}, p.invalid(resp.StatusCode)
	}
	return addr.Unmap(), nil
}

func parseIPify(body []byte) (string, error) {
	var v struct {
		IP *string `json:"ip"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", errors.New("trailing data")
	}
	if v.IP == nil {
		return "", errors.New("missing ip")
	}
	return *v.IP, nil
}

func parseAWS(body []byte) (string, error) {
	s := strings.TrimSpace(string(body))
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", errors.New("not a single address")
	}
	return s, nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPublic reports whether addr can be a public exit address: global unicast,
// not private, not carrier-grade NAT. IPv4-mapped IPv6 addresses are judged as IPv4.
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !cgnat.Contains(addr)
}
