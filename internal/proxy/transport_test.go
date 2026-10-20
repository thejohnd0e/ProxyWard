package proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

func TestDeadProxyCannotReachTarget(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	c, err := NewClient(domain.Proxy{Protocol: "http", Host: "127.0.0.1", Port: uint16(port)}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Get(target.URL)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || hits.Load() != 0 {
		t.Fatalf("direct bypass: err=%v hits=%d", err, hits.Load())
	}
}

// fixture bundles a CA, a target and a way to start each proxy kind.
type fixture struct {
	t      *testing.T
	ca     *testkit.CA
	target *testkit.Target
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ca := testkit.NewCA(t)
	return &fixture{t: t, ca: ca, target: testkit.NewTarget(t, ca, testkit.IPifyHandler("198.51.100.7"))}
}

type running struct {
	proxy domain.Proxy
	seen  func() []testkit.ProxyRequest
}

// start launches a fixture proxy of the given protocol. clientAuth is what the
// checked Proxy record carries; opts.Username/Password is what the fixture demands.
func (f *fixture) start(proto domain.Protocol, opts testkit.ProxyOptions, clientAuth *domain.Credentials) running {
	f.t.Helper()
	opts.Resolve = f.target.Resolve()
	var host string
	var port uint16
	var seen func() []testkit.ProxyRequest
	switch proto {
	case domain.ProtocolSOCKS5:
		p := testkit.NewSocksProxy(f.t, opts)
		host, port = p.Endpoint()
		seen = p.Requests
	case domain.ProtocolHTTPS:
		opts.CA = f.ca
		fallthrough
	default:
		p := testkit.NewConnectProxy(f.t, opts)
		host, port = p.Endpoint()
		seen = p.Requests
	}
	return running{proxy: domain.Proxy{Name: "fx", Protocol: proto, Host: host, Port: port, Auth: clientAuth}, seen: seen}
}

func (f *fixture) client(p domain.Proxy, timeout time.Duration) *http.Client {
	f.t.Helper()
	c, err := NewClient(p, timeout, WithRootCAs(f.ca.Pool))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fixture) get(c *http.Client) (string, error) {
	req, _ := http.NewRequest("GET", f.target.NamedURL("/"), nil)
	return doGet(c, req)
}

func doGet(c *http.Client, req *http.Request) (string, error) {
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

var protocols = []domain.Protocol{domain.ProtocolHTTP, domain.ProtocolHTTPS, domain.ProtocolSOCKS5}

func TestProtocolAuthMatrix(t *testing.T) {
	for _, proto := range protocols {
		for _, withAuth := range []bool{false, true} {
			name := string(proto) + "/open"
			var opts testkit.ProxyOptions
			var creds *domain.Credentials
			if withAuth {
				name = string(proto) + "/auth"
				// Characters that must survive Basic and RFC 1929 encoding.
				opts.Username, opts.Password = "us@er-x", "p%a:ss/w@rd#1"
				creds = &domain.Credentials{Username: opts.Username, Password: opts.Password}
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				px := f.start(proto, opts, creds)
				body, err := f.get(f.client(px.proxy, 3*time.Second))
				if err != nil {
					t.Fatalf("request through proxy failed: %v", err)
				}
				if !strings.Contains(body, "198.51.100.7") {
					t.Fatalf("body = %q", body)
				}
				reqs := f.target.Requests()
				if len(reqs) != 1 {
					t.Fatalf("target saw %d requests", len(reqs))
				}
				for _, h := range []string{"Proxy-Authorization", "Authorization", "Proxy-Connection"} {
					if v := reqs[0].Header.Get(h); v != "" {
						t.Errorf("target received %s: %q", h, v)
					}
				}
				seen := px.seen()
				if len(seen) != 1 || seen[0].Target != testkit.TargetName+":443" || !seen[0].Authorized {
					t.Fatalf("proxy saw %+v", seen)
				}
				if proto == domain.ProtocolSOCKS5 && !seen[0].AtypDomain {
					t.Error("SOCKS5 hostname was resolved locally instead of at the proxy")
				}
			})
		}
	}
}

func TestAuthFailures(t *testing.T) {
	for _, proto := range protocols {
		t.Run(string(proto), func(t *testing.T) {
			f := newFixture(t)
			opts := testkit.ProxyOptions{Username: "alice", Password: "right"}

			px := f.start(proto, opts, &domain.Credentials{Username: "alice", Password: "wrong"})
			_, err := f.get(f.client(px.proxy, 3*time.Second))
			if !errors.Is(err, ErrAuthFailed) {
				t.Fatalf("wrong password: %v", err)
			}

			px = f.start(proto, opts, nil)
			_, err = f.get(f.client(px.proxy, 3*time.Second))
			if !errors.Is(err, ErrAuthRequired) {
				t.Fatalf("missing credentials: %v", err)
			}
			if f.target.Hits() != 0 {
				t.Fatal("target was reached without valid proxy credentials")
			}
		})
	}
}

func TestConfiguredCredentialsWorkAgainstOpenProxy(t *testing.T) {
	for _, proto := range protocols {
		f := newFixture(t)
		px := f.start(proto, testkit.ProxyOptions{}, &domain.Credentials{Username: "u", Password: "p"})
		if _, err := f.get(f.client(px.proxy, 3*time.Second)); err != nil {
			t.Errorf("%s: %v", proto, err)
		}
	}
}

func stageOf(err error) (Stage, bool) {
	var se *StageError
	if errors.As(err, &se) {
		return se.Stage, true
	}
	return 0, false
}

func TestRefusedProxyPort(t *testing.T) {
	for _, proto := range protocols {
		f := newFixture(t)
		p := domain.Proxy{Protocol: proto, Host: "127.0.0.1", Port: testkit.FreePort(t)}
		_, err := f.get(f.client(p, 2*time.Second))
		if st, ok := stageOf(err); !ok || st != StageConnect {
			t.Errorf("%s: stage = %v ok=%v err=%v", proto, st, ok, err)
		}
		if f.target.Hits() != 0 {
			t.Errorf("%s: target reached despite a dead proxy", proto)
		}
	}
}

func TestProxyTLSCertificateIsVerified(t *testing.T) {
	f := newFixture(t)
	px := f.start(domain.ProtocolHTTPS, testkit.ProxyOptions{}, nil)
	other := testkit.NewCA(t) // the client trusts this CA, not the proxy's
	c, err := NewClient(px.proxy, 2*time.Second, WithRootCAs(other.Pool))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.get(c)
	if st, ok := stageOf(err); !ok || st != StageProxyTLS {
		t.Fatalf("stage = %v ok=%v err=%v", st, ok, err)
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected an unknown-authority error, got %v", err)
	}
	if f.target.Hits() != 0 {
		t.Fatal("request went through an unverified proxy")
	}
}

func TestTargetCertificateIsVerified(t *testing.T) {
	f := newFixture(t)
	px := f.start(domain.ProtocolHTTP, testkit.ProxyOptions{}, nil)
	// The client trusts only a CA that did not sign the target.
	c, err := NewClient(px.proxy, 2*time.Second, WithRootCAs(testkit.NewCA(t).Pool))
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.get(c)
	if err == nil {
		t.Fatal("untrusted target certificate accepted")
	}
	if _, isProxyStage := stageOf(err); isProxyStage {
		t.Fatalf("target TLS failure misattributed to the proxy: %v", err)
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected an unknown-authority error, got %v", err)
	}
}

func TestStalledProxyTimesOut(t *testing.T) {
	for _, proto := range protocols {
		f := newFixture(t)
		px := f.start(proto, testkit.ProxyOptions{Stall: true}, nil)
		start := time.Now()
		_, err := f.get(f.client(px.proxy, 300*time.Millisecond))
		if err == nil {
			t.Fatalf("%s: stalled proxy returned a response", proto)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("%s: took %v to give up", proto, el)
		}
		var ne net.Error
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &ne) && ne.Timeout()) {
			t.Errorf("%s: not a timeout: %v", proto, err)
		}
	}
}

func TestCancellationAbortsPromptly(t *testing.T) {
	for _, proto := range protocols {
		f := newFixture(t)
		px := f.start(proto, testkit.ProxyOptions{Stall: true}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		req, _ := http.NewRequestWithContext(ctx, "GET", f.target.NamedURL("/"), nil)
		start := time.Now()
		_, err := doGet(f.client(px.proxy, 30*time.Second), req)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v", proto, err)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("%s: cancellation took %v", proto, el)
		}
	}
}

func TestEnvironmentProxySettingsAreIgnored(t *testing.T) {
	trap, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer trap.Close()
	var trapped atomic.Int32
	go func() {
		for {
			c, err := trap.Accept()
			if err != nil {
				return
			}
			trapped.Add(1)
			c.Close()
		}
	}()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "http://"+trap.Addr().String())
	}
	t.Setenv("NO_PROXY", "")

	f := newFixture(t)
	for _, proto := range protocols {
		px := f.start(proto, testkit.ProxyOptions{}, nil)
		if _, err := f.get(f.client(px.proxy, 3*time.Second)); err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
	}
	dead := domain.Proxy{Protocol: domain.ProtocolHTTP, Host: "127.0.0.1", Port: testkit.FreePort(t)}
	if _, err := f.get(f.client(dead, time.Second)); err == nil {
		t.Fatal("dead proxy succeeded")
	}
	time.Sleep(50 * time.Millisecond)
	if n := trapped.Load(); n != 0 {
		t.Fatalf("%d connections went to the environment proxy", n)
	}
}

func TestProxyRefusingTheTunnel(t *testing.T) {
	f := newFixture(t)
	px := f.start(domain.ProtocolHTTP, testkit.ProxyOptions{ConnectStatus: 502}, nil)
	_, err := f.get(f.client(px.proxy, 2*time.Second))
	var te *TunnelError
	if !errors.Is(err, ErrTunnelFailed) || !errors.As(err, &te) || te.Code != 502 {
		t.Fatalf("CONNECT 502: %v", err)
	}
	px = f.start(domain.ProtocolSOCKS5, testkit.ProxyOptions{ReplyCode: 5}, nil)
	_, err = f.get(f.client(px.proxy, 2*time.Second))
	if !errors.Is(err, ErrTunnelFailed) || !errors.As(err, &te) || te.Code != 5 {
		t.Fatalf("SOCKS reply 5: %v", err)
	}
	px = f.start(domain.ProtocolHTTP, testkit.ProxyOptions{ConnectStatus: 403}, nil)
	if _, err = f.get(f.client(px.proxy, 2*time.Second)); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("CONNECT 403 without credentials: %v", err)
	}
	px = f.start(domain.ProtocolSOCKS5, testkit.ProxyOptions{ReplyCode: 2}, nil)
	if _, err = f.get(f.client(px.proxy, 2*time.Second)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("SOCKS reply 2 (not allowed): %v", err)
	}
	if f.target.Hits() != 0 {
		t.Fatal("target reached through a refusing proxy")
	}
}

func TestNewClientValidation(t *testing.T) {
	good := domain.Proxy{Protocol: domain.ProtocolHTTP, Host: "127.0.0.1", Port: 8080}
	if _, err := NewClient(good, 0); err == nil {
		t.Error("zero timeout accepted")
	}
	for name, p := range map[string]domain.Proxy{
		"protocol": {Protocol: "ftp", Host: "h", Port: 1},
		"host":     {Protocol: domain.ProtocolHTTP, Host: "", Port: 1},
		"port":     {Protocol: domain.ProtocolHTTP, Host: "h", Port: 0},
		"long socks user": {Protocol: domain.ProtocolSOCKS5, Host: "h", Port: 1,
			Auth: &domain.Credentials{Username: strings.Repeat("u", 256), Password: "p"}},
		"long socks pass": {Protocol: domain.ProtocolSOCKS5, Host: "h", Port: 1,
			Auth: &domain.Credentials{Username: "u", Password: strings.Repeat("p", 256)}},
		"empty username": {Protocol: domain.ProtocolHTTP, Host: "h", Port: 1, Auth: &domain.Credentials{Password: "p"}},
	} {
		if _, err := NewClient(p, time.Second); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	f := newFixture(t)
	redirectTarget := testkit.NewTarget(t, f.ca, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+testkit.TargetName+"/elsewhere", http.StatusFound)
	})
	p := testkit.NewConnectProxy(t, testkit.ProxyOptions{Resolve: redirectTarget.Resolve()})
	host, port := p.Endpoint()
	c := f.client(domain.Proxy{Protocol: domain.ProtocolHTTP, Host: host, Port: port}, 2*time.Second)
	resp, err := c.Get(redirectTarget.NamedURL("/"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || redirectTarget.Hits() != 1 {
		t.Fatalf("status=%d hits=%d", resp.StatusCode, redirectTarget.Hits())
	}
}

func TestConnectTunnelKeepsBytesBufferedAfterTheReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		buf := make([]byte, 4096)
		server.Read(buf)
		server.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\nHELLO"))
	}()
	conn, err := connectTunnel(context.Background(), client, "example.com:443", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "HELLO" {
		t.Fatalf("buffered bytes lost: %q %v", got, err)
	}
}

func TestSocksReplyDoesNotConsumeTunnelBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		buf := make([]byte, 64)
		server.Read(buf) // greeting
		server.Write([]byte{5, 0})
		server.Read(buf) // request
		server.Write([]byte{5, 0, 0, 1, 1, 2, 3, 4, 0x1f, 0x90, 'H', 'I'})
	}()
	conn, err := socks5Connect(context.Background(), client, "example.com:443", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "HI" {
		t.Fatalf("tunnel bytes lost: %q %v", got, err)
	}
}

func TestMalformedProxyAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		run   func(net.Conn) error
		reply []byte
	}{
		"http garbage": {func(c net.Conn) error { _, err := connectTunnel(context.Background(), c, "h:1", nil); return err }, []byte("not http at all\r\n\r\n")},
		"socks version": {func(c net.Conn) error {
			_, err := socks5Connect(context.Background(), c, "h:1", nil)
			return err
		}, []byte{4, 0}},
	} {
		client, server := net.Pipe()
		go func() {
			buf := make([]byte, 4096)
			server.Read(buf)
			server.Write(tc.reply)
			server.Close()
		}()
		if err := tc.run(client); !errors.Is(err, ErrProxyProtocol) {
			t.Errorf("%s: %v", name, err)
		}
		client.Close()
	}
}
