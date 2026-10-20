package testkit

import (
	"bufio"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ProxyOptions configures a fixture proxy.
type ProxyOptions struct {
	// Username and Password enable authentication when Username is not empty.
	Username, Password string
	// CA, when set, makes the proxy listen with TLS using a certificate
	// valid for the loopback addresses (an HTTPS proxy).
	CA *CA
	// Resolve maps hostnames to the real dial address (host:port) so tests can
	// prove that name resolution happens at the proxy.
	Resolve map[string]string
	// Stall accepts connections and never answers.
	Stall bool
	// ConnectStatus overrides the CONNECT answer (for example 502) and
	// ReplyCode overrides the SOCKS5 reply code; zero means succeed.
	ConnectStatus int
	ReplyCode     byte
}

// ProxyRequest is a tunnel request received by a fixture proxy.
type ProxyRequest struct {
	// Target is the requested host:port as sent by the client.
	Target string
	// AtypDomain is true for SOCKS5 requests that carried a hostname.
	AtypDomain bool
	// Authorized reports whether valid credentials were presented (always true
	// when the proxy requires none).
	Authorized bool
}

type base struct {
	t    testing.TB
	opts ProxyOptions
	ln   net.Listener
	wg   sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	seen  []ProxyRequest
}

func newBase(t testing.TB, opts ProxyOptions, tlsOK bool) *base {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsOK && opts.CA != nil {
		ln = tls.NewListener(ln, tlsConfig(opts.CA.Issue(t, nil, LocalIPs())))
	}
	return &base{t: t, opts: opts, ln: ln, conns: map[net.Conn]struct{}{}}
}

// Endpoint returns the proxy's host and port.
func (b *base) Endpoint() (string, uint16) { return SplitAddr(b.t, b.ln.Addr().String()) }

// Requests returns the tunnel requests received so far.
func (b *base) Requests() []ProxyRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ProxyRequest(nil), b.seen...)
}

func (b *base) record(r ProxyRequest) {
	b.mu.Lock()
	b.seen = append(b.seen, r)
	b.mu.Unlock()
}

// Close stops the proxy and closes all connections.
func (b *base) Close() {
	b.ln.Close()
	b.mu.Lock()
	for c := range b.conns {
		c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *base) track(c net.Conn) {
	b.mu.Lock()
	b.conns[c] = struct{}{}
	b.mu.Unlock()
}

func (b *base) untrack(c net.Conn) {
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
	c.Close()
}

func (b *base) serve(handle func(net.Conn)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		for {
			c, err := b.ln.Accept()
			if err != nil {
				return
			}
			b.track(c)
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()
				defer b.untrack(c)
				if b.opts.Stall {
					io.Copy(io.Discard, c) // hold the connection open, never answer
					return
				}
				handle(c)
			}()
		}
	}()
}

// dialTarget opens the upstream connection, resolving fixture names.
func (b *base) dialTarget(target string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	addr := target
	if real, ok := b.opts.Resolve[strings.ToLower(host)]; ok {
		addr = real
	}
	return net.DialTimeout("tcp", addr, 3*time.Second)
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	// When either side ends, close both so the other copy cannot block on a
	// peer that is waiting for this connection to close.
	<-done
	a.Close()
	b.Close()
	<-done
}

// ConnectProxy is a fixture HTTP proxy that supports CONNECT, optional Basic
// authentication and optional TLS.
type ConnectProxy struct{ *base }

// NewConnectProxy starts a CONNECT proxy.
func NewConnectProxy(t testing.TB, opts ProxyOptions) *ConnectProxy {
	t.Helper()
	p := &ConnectProxy{newBase(t, opts, true)}
	p.serve(p.handle)
	t.Cleanup(p.Close)
	return p
}

func (p *ConnectProxy) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		fmt.Fprint(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	authorized := p.authorized(req)
	p.record(ProxyRequest{Target: req.Host, Authorized: authorized})
	if !authorized {
		fmt.Fprint(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"fixture\"\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if st := p.opts.ConnectStatus; st != 0 {
		fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", st, http.StatusText(st))
		return
	}
	up, err := p.dialTarget(req.Host)
	if err != nil {
		fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	fmt.Fprint(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	c.SetDeadline(time.Time{})
	if n := br.Buffered(); n > 0 {
		buf, _ := br.Peek(n)
		up.Write(buf)
	}
	pipe(c, up)
}

func (p *ConnectProxy) authorized(req *http.Request) bool {
	if p.opts.Username == "" {
		return true
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(p.opts.Username+":"+p.opts.Password))
	got := req.Header.Get("Proxy-Authorization")
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
