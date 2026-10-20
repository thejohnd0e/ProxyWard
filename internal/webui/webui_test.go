package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func site() fstestFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>Proxy Checker</title><div id=root></div>")},
		"assets/app-1a2b3c.js":  {Data: []byte("console.log(1)")},
		"assets/app-1a2b3c.css": {Data: []byte("body{}")},
		"favicon.svg":           {Data: []byte("<svg/>")},
	}
}

type fstestFS = fstest.MapFS

func get(h http.Handler, method, path string) (*http.Response, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestServesIndexAndFallsBackForClientRoutes(t *testing.T) {
	h := NewHandler(site())
	for _, path := range []string{"/", "/proxies", "/proxies/12", "/events", "/settings", "/login", "/index.html"} {
		res, body := get(h, "GET", path)
		if res.StatusCode != 200 || !strings.Contains(body, `id=root`) {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
		if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: the app shell must always be revalidated, got Cache-Control %q", path, cc)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content type %q", path, ct)
		}
	}
}

func TestAssetsAreServedAndCachedImmutably(t *testing.T) {
	h := NewHandler(site())
	res, body := get(h, "GET", "/assets/app-1a2b3c.js")
	if res.StatusCode != 200 || body != "console.log(1)" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Fatalf("hashed assets should be cached for a year: %q", cc)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content type %q", ct)
	}
	if res, _ := get(h, "GET", "/favicon.svg"); res.StatusCode != 200 || strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("root files are revalidated: %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
}

func TestMissingAssetsAre404NotTheAppShell(t *testing.T) {
	h := NewHandler(site())
	for _, path := range []string{"/assets/missing.js", "/assets/", "/nope.png", "/static/x.css", "/a/b/c.map"} {
		if res, body := get(h, "GET", path); res.StatusCode != 404 || strings.Contains(body, "root") {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
	}
}

func TestOnlyReadMethodsAndNoTraversal(t *testing.T) {
	h := NewHandler(site())
	if res, _ := get(h, "POST", "/"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", res.StatusCode)
	}
	if res, body := get(h, "HEAD", "/"); res.StatusCode != 200 || body != "" {
		t.Fatalf("HEAD: %d %q", res.StatusCode, body)
	}
	for _, path := range []string{"/../secrets.key", "/assets/../../state.db", "/%2e%2e/etc/passwd", "/assets/%2e%2e/index.html"} {
		res, body := get(h, "GET", path)
		if strings.Contains(body, "BEGIN") || (res.StatusCode != 404 && res.StatusCode != 200 && res.StatusCode != 301) {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
	}
}

func TestAPIPathsNeverFallBackToTheShell(t *testing.T) {
	h := NewHandler(site())
	for _, path := range []string{"/api", "/api/", "/api/proxies", "/api/unknown/thing", "/healthz"} {
		if res, body := get(h, "GET", path); res.StatusCode != 404 || strings.Contains(body, "root") {
			t.Errorf("%s: %d %q", path, res.StatusCode, body)
		}
	}
}

func TestMissingBuildIsReportedClearly(t *testing.T) {
	h := NewHandler(fstest.MapFS{".gitkeep": {}})
	res, body := get(h, "GET", "/")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "not been built") {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(NewHandler(site()))
	res, _ := get(h, "GET", "/")
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "http:") || strings.Contains(csp, "*") {
		t.Errorf("CSP is too permissive: %s", csp)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("X-Frame-Options") != "DENY" || res.Header.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers: %v", res.Header)
	}
}

func TestEmbeddedBuildIsAvailable(t *testing.T) {
	h := Handler()
	res, _ := get(h, "GET", "/")
	if res.StatusCode != 200 && res.StatusCode != 503 {
		t.Fatalf("unexpected status %d", res.StatusCode)
	}
}
