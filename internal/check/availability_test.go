package check

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/providers"
	"proxy-checker/internal/proxy"
	"proxy-checker/internal/testkit"
)

// rig runs the checker against fixture services through a fixture proxy.
type rig struct {
	t      *testing.T
	ca     *testkit.CA
	target *testkit.Target
	clock  *testkit.Clock

	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, ca: testkit.NewCA(t), handlers: map[string]http.HandlerFunc{}}
	r.clock = testkit.NewClock(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	r.target = testkit.NewTarget(t, r.ca, func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		h := r.handlers[req.URL.Path]
		r.mu.Unlock()
		if h == nil {
			http.NotFound(w, req)
			return
		}
		h(w, req)
	})
	r.handle("/ipify", testkit.IPifyHandler("198.51.100.7"))
	r.handle("/aws", testkit.CheckIPHandler("198.51.100.7"))
	return r
}

func (r *rig) handle(path string, h http.HandlerFunc) {
	r.mu.Lock()
	r.handlers[path] = h
	r.mu.Unlock()
}

func (r *rig) checker() *Checker {
	return &Checker{
		Providers: []providers.IPProvider{
			providers.NewIPify(r.target.NamedURL("/ipify")),
			providers.NewAWS(r.target.NamedURL("/aws")),
		},
		Clock: r.clock,
	}
}

func (r *rig) proxy(proto domain.Protocol, opts testkit.ProxyOptions, creds *domain.Credentials) (domain.Proxy, func() []testkit.ProxyRequest) {
	r.t.Helper()
	opts.Resolve = r.target.Resolve()
	var host string
	var port uint16
	var seen func() []testkit.ProxyRequest
	if proto == domain.ProtocolSOCKS5 {
		p := testkit.NewSocksProxy(r.t, opts)
		host, port = p.Endpoint()
		seen = p.Requests
	} else {
		if proto == domain.ProtocolHTTPS {
			opts.CA = r.ca
		}
		p := testkit.NewConnectProxy(r.t, opts)
		host, port = p.Endpoint()
		seen = p.Requests
	}
	return domain.Proxy{ID: 7, Name: "fx", Protocol: proto, Host: host, Port: port, Auth: creds}, seen
}

func (r *rig) run(p domain.Proxy, timeout time.Duration) domain.Availability {
	r.t.Helper()
	return r.runCtx(context.Background(), p, timeout)
}

func (r *rig) runCtx(ctx context.Context, p domain.Proxy, timeout time.Duration) domain.Availability {
	r.t.Helper()
	c, err := proxy.NewClient(p, timeout, proxy.WithRootCAs(r.ca.Pool))
	if err != nil {
		r.t.Fatal(err)
	}
	return r.checker().Check(ctx, p, c)
}

func code(a domain.Availability) string {
	if a.Failure == nil {
		return ""
	}
	return a.Failure.Code
}

func TestUpThroughEveryProtocol(t *testing.T) {
	for _, proto := range []domain.Protocol{domain.ProtocolHTTP, domain.ProtocolHTTPS, domain.ProtocolSOCKS5} {
		r := newRig(t)
		p, _ := r.proxy(proto, testkit.ProxyOptions{Username: "u", Password: "p"}, &domain.Credentials{Username: "u", Password: "p"})
		a := r.run(p, 3*time.Second)
		if a.State != domain.StateUp || a.Failure != nil {
			t.Fatalf("%s: %+v", proto, a)
		}
		if a.ExitIP != "198.51.100.7" || a.Endpoint != "ipify" || a.ProxyID != 7 {
			t.Errorf("%s: %+v", proto, a)
		}
		if !a.CheckedAt.Equal(r.clock.Now()) {
			t.Errorf("%s: CheckedAt = %v", proto, a.CheckedAt)
		}
		if a.Duration <= 0 || a.Duration > 3*time.Second {
			t.Errorf("%s: duration %v", proto, a.Duration)
		}
	}
}

func TestFallbackProviderEstablishesUp(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"500":       testkit.StatusHandler(500, "boom"),
		"429":       testkit.StatusHandler(429, ""),
		"bad json":  testkit.StatusHandler(200, `{"ip":`),
		"private":   testkit.IPifyHandler("10.1.2.3"),
		"truncated": testkit.StatusHandler(200, `{"ip":"8.8`),
	} {
		r := newRig(t)
		r.handle("/ipify", h)
		p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{}, nil)
		a := r.run(p, 3*time.Second)
		if a.State != domain.StateUp || a.Endpoint != "aws" || a.ExitIP != "198.51.100.7" || a.Failure != nil {
			t.Errorf("%s: %+v", name, a)
		}
	}
}

func TestBothProvidersFailingIsIndeterminate(t *testing.T) {
	for name, tc := range map[string]struct {
		primary, fallback http.HandlerFunc
		wantCode          string
	}{
		"both 503":         {testkit.StatusHandler(503, ""), testkit.StatusHandler(503, ""), "provider_unavailable"},
		"429 then 500":     {testkit.StatusHandler(429, ""), testkit.StatusHandler(500, ""), "provider_rate_limited"},
		"both garbage":     {testkit.StatusHandler(200, "<html>"), testkit.StatusHandler(200, "<html>"), "invalid_response"},
		"private both":     {testkit.IPifyHandler("192.168.1.1"), testkit.CheckIPHandler("192.168.1.1"), "invalid_response"},
		"oversized":        {testkit.StatusHandler(200, strings.Repeat("a", 5000)), testkit.StatusHandler(200, strings.Repeat("a", 5000)), "invalid_response"},
		"redirect and 404": {func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/aws", 302) }, testkit.StatusHandler(404, ""), "invalid_response"},
	} {
		r := newRig(t)
		r.handle("/ipify", tc.primary)
		r.handle("/aws", tc.fallback)
		p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{}, nil)
		a := r.run(p, 3*time.Second)
		if a.State != domain.StateIndeterminate || code(a) != tc.wantCode || a.ExitIP != "" {
			t.Errorf("%s: %+v failure=%+v", name, a, a.Failure)
		}
		if a.Failure != nil && a.Failure.Message == "" {
			t.Errorf("%s: empty failure message", name)
		}
	}
}

func TestExplicitProxyFailuresAreDown(t *testing.T) {
	for _, proto := range []domain.Protocol{domain.ProtocolHTTP, domain.ProtocolHTTPS, domain.ProtocolSOCKS5} {
		t.Run(string(proto)+"/wrong password", func(t *testing.T) {
			r := newRig(t)
			p, seen := r.proxy(proto, testkit.ProxyOptions{Username: "u", Password: "right"}, &domain.Credentials{Username: "u", Password: "wrong"})
			a := r.run(p, 3*time.Second)
			if a.State != domain.StateDown || code(a) != "auth_failed" {
				t.Fatalf("%+v", a)
			}
			if len(seen()) != 1 {
				t.Errorf("fallback provider was attempted after a definite proxy failure: %d requests", len(seen()))
			}
			if r.target.Hits() != 0 {
				t.Error("target reached without valid credentials")
			}
		})
		t.Run(string(proto)+"/missing credentials", func(t *testing.T) {
			r := newRig(t)
			p, _ := r.proxy(proto, testkit.ProxyOptions{Username: "u", Password: "p"}, nil)
			if a := r.run(p, 3*time.Second); a.State != domain.StateDown || code(a) != "auth_required" {
				t.Fatalf("%+v", a)
			}
		})
		t.Run(string(proto)+"/refused", func(t *testing.T) {
			r := newRig(t)
			p := domain.Proxy{ID: 1, Protocol: proto, Host: "127.0.0.1", Port: testkit.FreePort(t)}
			a := r.run(p, 2*time.Second)
			if a.State != domain.StateDown || code(a) != "connection_refused" {
				t.Fatalf("%+v", a)
			}
			if r.target.Hits() != 0 {
				t.Fatal("a dead proxy was bypassed: the target was reached directly")
			}
		})
	}
}

func TestProxyTLSFailureIsDown(t *testing.T) {
	r := newRig(t)
	p, _ := r.proxy(domain.ProtocolHTTPS, testkit.ProxyOptions{}, nil)
	c, err := proxy.NewClient(p, 2*time.Second, proxy.WithRootCAs(testkit.NewCA(t).Pool))
	if err != nil {
		t.Fatal(err)
	}
	a := r.checker().Check(context.Background(), p, c)
	if a.State != domain.StateDown || code(a) != "proxy_tls" {
		t.Fatalf("%+v", a)
	}
}

func TestTargetTLSFailureIsIndeterminate(t *testing.T) {
	r := newRig(t)
	p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{}, nil)
	c, err := proxy.NewClient(p, 2*time.Second, proxy.WithRootCAs(testkit.NewCA(t).Pool))
	if err != nil {
		t.Fatal(err)
	}
	a := r.checker().Check(context.Background(), p, c)
	if a.State != domain.StateIndeterminate || code(a) != "target_tls" {
		t.Fatalf("%+v", a)
	}
}

func TestProxyThatCannotReachTheTargetIsIndeterminate(t *testing.T) {
	r := newRig(t)
	p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{ConnectStatus: 502}, nil)
	if a := r.run(p, 2*time.Second); a.State != domain.StateIndeterminate || code(a) != "provider_unavailable" {
		t.Fatalf("CONNECT 502: %+v", a)
	}
	p, _ = r.proxy(domain.ProtocolSOCKS5, testkit.ProxyOptions{ReplyCode: 4}, nil)
	if a := r.run(p, 2*time.Second); a.State != domain.StateIndeterminate || code(a) != "provider_unavailable" {
		t.Fatalf("SOCKS host unreachable: %+v", a)
	}
}

func TestStalledProxyIsIndeterminateTimeout(t *testing.T) {
	r := newRig(t)
	p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{Stall: true}, nil)
	start := time.Now()
	a := r.run(p, 300*time.Millisecond)
	if a.State != domain.StateIndeterminate || code(a) != "timeout" {
		t.Fatalf("%+v", a)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestCancellationIsReportedAndStopsFallback(t *testing.T) {
	r := newRig(t)
	p, seen := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{Stall: true}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	a := r.runCtx(ctx, p, 30*time.Second)
	if code(a) != "canceled" || a.State == domain.StateUp || a.State == domain.StateDown {
		t.Fatalf("%+v", a)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if len(seen()) > 1 {
		t.Fatalf("fallback attempted after cancellation: %d tunnel requests", len(seen()))
	}
}

func TestNoProvidersConfigured(t *testing.T) {
	c := &Checker{Clock: testkit.NewClock(time.Now())}
	a := c.Check(context.Background(), domain.Proxy{ID: 1}, http.DefaultClient)
	if a.State != domain.StateIndeterminate || code(a) != "provider_unavailable" {
		t.Fatalf("%+v", a)
	}
}

func TestFailureMessagesAreFixedEnglishText(t *testing.T) {
	r := newRig(t)
	p, _ := r.proxy(domain.ProtocolHTTP, testkit.ProxyOptions{Username: "secret-user", Password: "secret-pass"},
		&domain.Credentials{Username: "secret-user", Password: "bad-pass"})
	a := r.run(p, 2*time.Second)
	for _, s := range []string{"secret-user", "secret-pass", "bad-pass", "127.0.0.1"} {
		if strings.Contains(a.Failure.Message, s) {
			t.Errorf("message leaks %q: %s", s, a.Failure.Message)
		}
	}
}

func TestClassify(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	for name, tc := range map[string]struct {
		err        error
		code       string
		definiteIs bool
	}{
		"canceled":          {context.Canceled, "canceled", false},
		"deadline":          {timeoutErr, "timeout", false},
		"connect refused":   {&proxy.StageError{Stage: proxy.StageConnect, Err: errors.New("dial tcp: refused")}, "connection_refused", true},
		"connect timeout":   {&proxy.StageError{Stage: proxy.StageConnect, Err: timeoutErr}, "timeout", true},
		"tunnel timeout":    {&proxy.StageError{Stage: proxy.StageTunnel, Err: timeoutErr}, "timeout", false},
		"auth required":     {&proxy.StageError{Stage: proxy.StageTunnel, Err: proxy.ErrAuthRequired}, "auth_required", true},
		"auth failed":       {&proxy.StageError{Stage: proxy.StageTunnel, Err: proxy.ErrAuthFailed}, "auth_failed", true},
		"tunnel refused":    {&proxy.StageError{Stage: proxy.StageTunnel, Err: &proxy.TunnelError{Code: 502}}, "provider_unavailable", false},
		"proxy garbage":     {&proxy.StageError{Stage: proxy.StageTunnel, Err: proxy.ErrProxyProtocol}, "invalid_response", true},
		"proxy tls":         {&proxy.StageError{Stage: proxy.StageProxyTLS, Err: errors.New("x509")}, "proxy_tls", true},
		"provider 429":      {&providers.Error{Provider: "p", Kind: providers.KindRateLimited, Status: 429}, "provider_rate_limited", false},
		"provider 500":      {&providers.Error{Provider: "p", Kind: providers.KindUnavailable, Status: 500}, "provider_unavailable", false},
		"provider invalid":  {&providers.Error{Provider: "p", Kind: providers.KindInvalidResponse}, "invalid_response", false},
		"unknown transport": {errors.New("EOF"), "provider_unavailable", false},
	} {
		f := Classify(tc.err)
		if f.Failure.Code != tc.code || f.Definite != tc.definiteIs || f.Failure.Message == "" {
			t.Errorf("%s: %+v", name, f)
		}
	}
}
