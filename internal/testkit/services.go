package testkit

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// TargetName is a hostname that only fixture proxies can resolve, which proves
// that name resolution happens at the proxy (for example SOCKS5 remote DNS).
const TargetName = "api.target.test"

// Request is a request captured by a fixture server.
type Request struct {
	Method, Host, Path, RawQuery string
	Header                       http.Header
	RemoteAddr                   string
}

// Target is a local HTTPS server standing in for a public service. It records
// every request so tests can prove what did, or did not, reach it.
type Target struct {
	// Addr is the real listen address (host:port).
	Addr string
	// URL is the base URL using the real address.
	URL string

	srv *httptest.Server
	mu  sync.Mutex
	req []Request
}

// NewTarget starts an HTTPS server with a certificate from ca valid for
// TargetName and the loopback addresses.
func NewTarget(t testing.TB, ca *CA, h http.HandlerFunc) *Target {
	t.Helper()
	tg := &Target{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		tg.req = append(tg.req, Request{
			Method: r.Method, Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Header: r.Header.Clone(), RemoteAddr: r.RemoteAddr,
		})
		tg.mu.Unlock()
		h(w, r)
	}))
	srv.TLS = nil
	srv.EnableHTTP2 = false
	cert := ca.Issue(t, []string{TargetName, "localhost"}, LocalIPs())
	srv.TLS = tlsConfig(cert)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	tg.srv = srv
	tg.URL = srv.URL
	tg.Addr = srv.Listener.Addr().String()
	return tg
}

// Close stops the server early; it is safe to call more than once.
func (t *Target) Close() { t.srv.Close() }

// Requests returns the captured requests.
func (t *Target) Requests() []Request {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Request(nil), t.req...)
}

// Hits returns the number of requests received.
func (t *Target) Hits() int { return len(t.Requests()) }

// Resolve returns the fixture-proxy name table mapping TargetName to this server.
func (t *Target) Resolve() map[string]string { return map[string]string{TargetName: t.Addr} }

// NamedURL returns an https URL using TargetName and the default port, which
// only works through a fixture proxy that resolves the name.
func (t *Target) NamedURL(path string) string { return "https://" + TargetName + path }

// TargetNamedURL returns an https URL for TargetName and path, independent of any server.
func TargetNamedURL(path string) string { return "https://" + TargetName + path }

// IPifyHandler answers like api.ipify.org?format=json.
func IPifyHandler(ip string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ip":%q}`, ip)
	}
}

// CheckIPHandler answers like checkip.amazonaws.com.
func CheckIPHandler(ip string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, ip)
	}
}

// StatusHandler always answers with the given status and body.
func StatusHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

// SplitAddr returns the host and numeric port of a host:port string.
func SplitAddr(t testing.TB, addr string) (string, uint16) {
	t.Helper()
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(port)
}

// FreePort returns a loopback port that nothing is listening on.
func FreePort(t testing.TB) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port := SplitAddr(t, l.Addr().String())
	return port
}
