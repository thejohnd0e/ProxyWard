//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/app"
	"github.com/thejohnd0e/ProxyWard/internal/config"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

const (
	adminPassword = "e2e-password-123"
)

type exitInfo struct {
	ip, countryCode, country, city, org string
	asn                                 int
}

var internets = []exitInfo{
	{"198.51.100.7", "DE", "Germany", "Berlin", "Example GmbH", 64500},
	{"203.0.113.20", "US", "United States", "New York", "Example Networks Inc.", 64501},
	{"192.0.2.55", "JP", "Japan", "Tokyo", "Example KK", 64502},
}

type fixtureProxy struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	ExitIP   string `json:"exit_ip,omitempty"`
}

// internet is a stand-in for the public services as seen through one proxy.
func internet(t *testing.T, ca *testkit.CA, exit exitInfo) *testkit.Target {
	speed := testkit.NewSpeedService(t, ca)
	speed.Set(testkit.SpeedBehavior{ChunkDelay: 2 * time.Millisecond, UploadReadDelay: 2 * time.Millisecond})
	return testkit.NewTarget(t, ca, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/ipify":
			testkit.IPifyHandler(exit.ip)(w, r)
		case r.URL.Path == "/aws":
			testkit.CheckIPHandler(exit.ip)(w, r)
		case r.URL.Path == "/geo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ip":%q,"asn":%d,"organization_name":%q,"country":%q,"country_code":%q,"city":%q}`,
				exit.ip, exit.asn, exit.org, exit.country, exit.countryCode, exit.city)
		default:
			speed.Serve(w, r)
		}
	})
}

var botPath = regexp.MustCompile(`^/bot[^/]+/sendMessage$`)

func TestServe(t *testing.T) {
	if os.Getenv("E2E_SERVE") != "1" {
		t.Skip("set E2E_SERVE=1 to run the end-to-end stand")
	}
	listen := envOr("E2E_LISTEN", "127.0.0.1:18080")
	control := envOr("E2E_CONTROL", "127.0.0.1:18081")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ca := testkit.NewCA(t)
	var nets []*testkit.Target
	for _, ex := range internets {
		nets = append(nets, internet(t, ca, ex))
	}
	resolve := func(i int) map[string]string { return nets[i].Resolve() }

	var fixtures []fixtureProxy
	add := func(name, proto, host string, port uint16, user, pass, exit string) {
		fixtures = append(fixtures, fixtureProxy{Name: name, Protocol: proto, Host: host, Port: port, Username: user, Password: pass, ExitIP: exit})
	}

	open := testkit.NewConnectProxy(t, testkit.ProxyOptions{Resolve: resolve(0)})
	h, p := open.Endpoint()
	add("Office gateway", "http", h, p, "", "", internets[0].ip)

	socks := testkit.NewSocksProxy(t, testkit.ProxyOptions{Username: "alice", Password: "s3cret-pass", Resolve: resolve(1)})
	h, p = socks.Endpoint()
	add("Berlin SOCKS", "socks5", h, p, "alice", "s3cret-pass", internets[1].ip)

	tlsProxy := testkit.NewConnectProxy(t, testkit.ProxyOptions{CA: ca, Resolve: resolve(2)})
	h, p = tlsProxy.Endpoint()
	add("Paris TLS", "https", h, p, "", "", internets[2].ip)

	wall := testkit.NewConnectProxy(t, testkit.ProxyOptions{Username: "bob", Password: "right-pass", Resolve: resolve(0)})
	h, p = wall.Endpoint()
	add("Auth wall", "http", h, p, "bob", "wrong-pass", "")

	add("Dead proxy", "http", "127.0.0.1", testkit.FreePort(t), "", "", "")

	// Direct service: the Telegram Bot API.
	var mu sync.Mutex
	sent := []string{}
	direct := testkit.NewTarget(t, ca, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case botPath.MatchString(r.URL.Path) && r.Method == http.MethodPost:
			var req struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			sent = append(sent, req.Text)
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
		default:
			http.NotFound(w, r)
		}
	})

	// A small plain-HTTP control server for the browser tests.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__e2e/fixtures", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"admin_password": adminPassword, "proxies": fixtures,
			"telegram_token": "123456789:AAH_e2e-TOKEN-value-1234567890abcd", "telegram_chat": "-1001234567890",
		})
	})
	mux.HandleFunc("GET /__e2e/telegram", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": sent})
	})
	srv := &http.Server{Addr: control, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.ListenAndServe() }()
	defer srv.Close()

	cfg := config.Config{ListenAddr: listen, DataDir: t.TempDir()}
	if err := app.SetAdminPassword(ctx, cfg, adminPassword); err != nil {
		t.Fatal(err)
	}
	opts := app.Options{
		IPProviders: []providers.IPProvider{
			providers.NewIPify(testkit.TargetNamedURL("/ipify")),
			providers.NewAWS(testkit.TargetNamedURL("/aws")),
		},
		SpeedService: providers.NewCloudflareAt("https://" + testkit.TargetName),
		GeoSources:   []providers.GeoSource{{ID: "fixture", Name: "Fixture", Site: "https://example.invalid/", URL: testkit.TargetNamedURL("/geo")}},
		TelegramURL:  direct.URL,
		RootCAs:      ca.Pool,
	}
	fmt.Fprintf(os.Stderr, "e2e: app on http://%s, control on http://%s\n", listen, control)
	if err := app.RunWith(ctx, cfg, opts); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
