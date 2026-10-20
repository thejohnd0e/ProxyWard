package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"proxy-checker/internal/testkit"
)

// serve starts a fixture HTTPS service and returns a client trusting it.
func serve(t *testing.T, h http.HandlerFunc) (*testkit.Target, *http.Client) {
	t.Helper()
	ca := testkit.NewCA(t)
	tg := testkit.NewTarget(t, ca, h)
	tr := &http.Transport{TLSClientConfig: testTLS(ca)}
	t.Cleanup(tr.CloseIdleConnections)
	return tg, &http.Client{Transport: tr, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func body(s string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(s)) }
}

func TestIPifyParsesValidResponses(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"ipv4":              {`{"ip":"203.0.113.9"}`, "203.0.113.9"},
		"ipv6":              {`{"ip":"2606:4700:4700::1111"}`, "2606:4700:4700::1111"},
		"extra field":       {`{"ip":"8.8.8.8","note":"x"}`, "8.8.8.8"},
		"whitespace":        {" {\"ip\": \"8.8.4.4\"}\n", "8.8.4.4"},
		"mapped public v4":  {`{"ip":"::ffff:8.8.8.8"}`, "8.8.8.8"},
		"trailing ip space": {`{"ip":" 8.8.8.8 "}`, "8.8.8.8"},
	} {
		tg, c := serve(t, body(tc.body))
		addr, err := NewIPify(tg.URL).Lookup(context.Background(), c)
		if err != nil || addr.String() != tc.want {
			t.Errorf("%s: %v %v", name, addr, err)
		}
	}
}

func TestIPifyRejectsInvalidResponses(t *testing.T) {
	for name, b := range map[string]string{
		"empty":          ``,
		"not json":       `8.8.8.8`,
		"truncated":      `{"ip":"8.8.`,
		"missing ip":     `{"address":"8.8.8.8"}`,
		"ip not string":  `{"ip":8}`,
		"ip null":        `{"ip":null}`,
		"not an address": `{"ip":"not-an-ip"}`,
		"with port":      `{"ip":"8.8.8.8:80"}`,
		"html":           `<html>captive portal</html>`,
		"array":          `["8.8.8.8"]`,
		"trailing junk":  `{"ip":"8.8.8.8"} extra`,
	} {
		tg, c := serve(t, body(b))
		_, err := NewIPify(tg.URL).Lookup(context.Background(), c)
		assertKind(t, name, err, KindInvalidResponse)
	}
}

func TestAWSParsing(t *testing.T) {
	tg, c := serve(t, body("203.0.113.9\n"))
	addr, err := NewAWS(tg.URL).Lookup(context.Background(), c)
	if err != nil || addr.String() != "203.0.113.9" {
		t.Fatalf("%v %v", addr, err)
	}
	for name, b := range map[string]string{
		"empty":        "",
		"blank":        "\n",
		"extra text":   "203.0.113.9 and more",
		"two lines":    "203.0.113.9\n203.0.113.10\n",
		"html":         "<html></html>",
		"json":         `{"ip":"203.0.113.9"}`,
		"invalid addr": "999.1.1.1",
	} {
		tg, c := serve(t, body(b))
		_, err := NewAWS(tg.URL).Lookup(context.Background(), c)
		assertKind(t, name, err, KindInvalidResponse)
	}
}

func TestExitIPMustBePublic(t *testing.T) {
	for _, ip := range []string{
		"10.0.0.1", "172.16.5.4", "192.168.1.1", "127.0.0.1", "169.254.1.1", "100.64.0.1", "100.127.255.255",
		"0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1",
	} {
		tg, c := serve(t, body(ip))
		_, err := NewAWS(tg.URL).Lookup(context.Background(), c)
		assertKind(t, ip, err, KindInvalidResponse)
	}
	for _, ip := range []string{"1.1.1.1", "100.63.255.255", "100.128.0.0", "198.51.100.7", "2606:4700::1111"} {
		tg, c := serve(t, body(ip))
		if _, err := NewAWS(tg.URL).Lookup(context.Background(), c); err != nil {
			t.Errorf("%s rejected: %v", ip, err)
		}
	}
}

func TestStatusMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		kind   Kind
	}{
		"429":    {429, KindRateLimited},
		"500":    {500, KindUnavailable},
		"503":    {503, KindUnavailable},
		"403":    {403, KindUnavailable},
		"404":    {404, KindUnavailable},
		"302":    {302, KindInvalidResponse},
		"204":    {204, KindInvalidResponse},
		"206":    {206, KindInvalidResponse},
		"100ish": {201, KindInvalidResponse},
	} {
		tg, c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://elsewhere.example/")
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"ip":"8.8.8.8"}`))
		})
		_, err := NewIPify(tg.URL).Lookup(context.Background(), c)
		var pe *Error
		if !errors.As(err, &pe) || pe.Kind != tc.kind || pe.Status != tc.status {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	tg, c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	})
	_, err := NewIPify(tg.URL).Lookup(context.Background(), c)
	var pe *Error
	if !errors.As(err, &pe) || pe.RetryAfter != 120*time.Second {
		t.Fatalf("%v", err)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	tg, c := serve(t, body(`{"ip":"8.8.8.8","pad":"`+strings.Repeat("x", 5000)+`"}`))
	_, err := NewIPify(tg.URL).Lookup(context.Background(), c)
	assertKind(t, "oversized", err, KindInvalidResponse)
	// A body exactly at the cap is still read.
	pad := 4096 - len(`{"ip":"8.8.8.8","pad":""}`)
	tg, c = serve(t, body(`{"ip":"8.8.8.8","pad":"`+strings.Repeat("x", pad)+`"}`))
	if _, err := NewIPify(tg.URL).Lookup(context.Background(), c); err != nil {
		t.Fatalf("4096-byte body: %v", err)
	}
}

func TestTransportErrorsPassThrough(t *testing.T) {
	tg, c := serve(t, body("x"))
	tg.Addr = ""
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewIPify(tg.URL).Lookup(ctx, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
	var pe *Error
	if errors.As(err, &pe) {
		t.Fatalf("transport error was turned into a provider error: %v", err)
	}
}

func TestProviderErrorsNeverContainResponseBodies(t *testing.T) {
	tg, c := serve(t, body("SECRET-UPSTREAM-BODY <html>"))
	_, err := NewIPify(tg.URL).Lookup(context.Background(), c)
	if err == nil || strings.Contains(err.Error(), "SECRET-UPSTREAM-BODY") {
		t.Fatalf("error leaks upstream body: %v", err)
	}
}

func TestNames(t *testing.T) {
	if NewIPify("x").Name() != "ipify" || NewAWS("x").Name() != "aws" {
		t.Fatal("unexpected provider names")
	}
	if DefaultIPify().Name() != "ipify" || DefaultAWS().Name() != "aws" {
		t.Fatal("unexpected default provider names")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"30":                            30 * time.Second,
		" 45 ":                          45 * time.Second,
		"-5":                            0,
		"abc":                           0,
		"Tue, 20 Oct 2026 12:01:30 GMT": 90 * time.Second,
		"Tue, 20 Oct 2026 11:00:00 GMT": 0,
		"99999999999999999999":          0,
	} {
		if got := ParseRetryAfter(in, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func assertKind(t *testing.T, name string, err error, want Kind) {
	t.Helper()
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != want {
		t.Errorf("%s: err = %v, want kind %v", name, err, want)
	}
}
