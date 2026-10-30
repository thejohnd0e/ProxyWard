package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/geo"
	"github.com/thejohnd0e/ProxyWard/internal/jobs"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

const (
	adminPassword = "correct horse battery"
	newPassword   = "another long passphrase"
)

var (
	hashOnce sync.Once
	hashVal  string
)

// adminHash hashes once: Argon2id is deliberately slow.
func adminHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		var err error
		if hashVal, err = auth.Hash(adminPassword); err != nil {
			t.Fatal(err)
		}
	})
	return hashVal
}

type env struct {
	geoViews   map[netip.Addr]geo.GeoView
	reconciles atomic.Int32
	canceler   *fakeCanceler
	t          *testing.T
	srv        *httptest.Server
	db         *store.Store
	clock      *testkit.Clock
	logs       *bytes.Buffer
	bodiesMu   sync.Mutex
	bodies     []string // every response body, for leak checks
}

func newEnv(t *testing.T, withAdmin bool) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewClock(time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC))
	if withAdmin {
		if err := db.SetAdminPasswordHash(ctx, adminHash(t), clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	canceler := &fakeCanceler{}
	queue := &jobs.Queue{Store: db, Clock: clock, Canceler: canceler}
	e := &env{t: t, db: db, clock: clock, logs: logs, canceler: canceler}
	e.srv = httptest.NewServer(New(db, cipher, WithClock(clock), WithLogger(logger), WithJobs(queue), WithGeo(peekFunc(func(ctx context.Context, ip netip.Addr) (geo.GeoView, error) {
		return fakePeek{e.geoViews}.Peek(ctx, ip)
	})),
		WithReconcile(func(context.Context) error { e.reconciles.Add(1); return nil })))
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (e *env) do(method, path, body string, hdr map[string]string, cookie string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	e.bodiesMu.Lock()
	e.bodies = append(e.bodies, string(b))
	e.bodiesMu.Unlock()
	return resp{status: res.StatusCode, header: res.Header, body: string(b)}
}

func (e *env) login(password string) (cookie, csrf string, r resp) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"password": password})
	r = e.do("POST", "/api/auth/login", string(b), nil, "")
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.CookieName {
			cookie = c.Value
		}
	}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	return cookie, out.CSRF, r
}

func (e *env) sessionAuthenticated(cookie string) bool {
	e.t.Helper()
	r := e.do("GET", "/api/auth/session", "", nil, cookie)
	var out struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.Authenticated
}

func TestLoginSuccessSetsHardenedCookie(t *testing.T) {
	e := newEnv(t, true)
	cookie, csrf, r := e.login(adminPassword)
	if r.status != 200 || cookie == "" || csrf == "" {
		t.Fatalf("login: status=%d cookie=%q csrf=%q body=%s", r.status, cookie, csrf, r.body)
	}
	c := (&http.Response{Header: r.header}).Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 86400 {
		t.Fatalf("cookie flags: %+v", c)
	}
	if strings.Contains(r.body, cookie) {
		t.Fatal("session token echoed in the response body")
	}
	if !e.sessionAuthenticated(cookie) {
		t.Fatal("session not recognized")
	}
	// Only a digest is stored.
	var n int
	if err := e.db.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = n
	if err := e.db.SessionActive(context.Background(), cookie, e.clock.Now()); err == nil {
		t.Fatal("raw token is stored in the database")
	}
	if err := e.db.SessionActive(context.Background(), auth.Digest(cookie), e.clock.Now()); err != nil {
		t.Fatalf("digest not stored: %v", err)
	}
}

func TestLoginWrongPasswordAndMalformedBodies(t *testing.T) {
	e := newEnv(t, true)
	if _, _, r := e.login("wrong password!"); r.status != 401 || !strings.Contains(r.body, "invalid_credentials") {
		t.Fatalf("wrong password: %d %s", r.status, r.body)
	}
	for name, body := range map[string]string{
		"not json":      "nope",
		"unknown field": `{"password":"x","extra":1}`,
		"trailing data": `{"password":"x"} {}`,
	} {
		if r := e.do("POST", "/api/auth/login", body, nil, ""); r.status != 400 {
			t.Errorf("%s: status %d", name, r.status)
		}
	}
}

func TestLoginBlockedUntilAdminIsSet(t *testing.T) {
	e := newEnv(t, false)
	if _, _, r := e.login(adminPassword); r.status != 503 || !strings.Contains(r.body, "not_initialized") {
		t.Fatalf("login without admin: %d %s", r.status, r.body)
	}
	if r := e.do("POST", "/api/auth/logout", "", nil, ""); r.status != 503 {
		t.Fatalf("protected route without admin: %d", r.status)
	}
}

func TestLoginThrottling(t *testing.T) {
	e := newEnv(t, true)
	for i := 0; i < auth.LoginPerPeerPerMinute; i++ {
		if _, _, r := e.login("wrong password!"); r.status != 401 {
			t.Fatalf("attempt %d: %d", i+1, r.status)
		}
	}
	_, _, r := e.login(adminPassword)
	if r.status != 429 || r.header.Get("Retry-After") == "" {
		t.Fatalf("throttled: %d retry-after=%q", r.status, r.header.Get("Retry-After"))
	}
	e.clock.Advance(61 * time.Second)
	if cookie, _, r := e.login(adminPassword); r.status != 200 || cookie == "" {
		t.Fatalf("after window: %d", r.status)
	}
}

func TestForwardedHeaderDoesNotBypassThrottle(t *testing.T) {
	e := newEnv(t, true)
	body := `{"password":"wrong password!"}`
	for i := 0; i < auth.LoginPerPeerPerMinute; i++ {
		e.do("POST", "/api/auth/login", body, map[string]string{"X-Forwarded-For": "1.2.3." + string(rune('0'+i))}, "")
	}
	r := e.do("POST", "/api/auth/login", body, map[string]string{"X-Forwarded-For": "9.9.9.9"}, "")
	if r.status != 429 {
		t.Fatalf("spoofed X-Forwarded-For escaped throttling: %d", r.status)
	}
}

func TestSessionExpiresAfter24Hours(t *testing.T) {
	e := newEnv(t, true)
	cookie, _, _ := e.login(adminPassword)
	e.clock.Advance(auth.SessionTTL - time.Minute)
	if !e.sessionAuthenticated(cookie) {
		t.Fatal("session expired early")
	}
	e.clock.Advance(2 * time.Minute)
	if e.sessionAuthenticated(cookie) {
		t.Fatal("session still valid after 24 hours")
	}
	if r := e.do("POST", "/api/auth/logout", "", nil, cookie); r.status != 401 {
		t.Fatalf("expired session on a protected route: %d", r.status)
	}
}

func TestMutationsRequireCSRFAndSameOrigin(t *testing.T) {
	e := newEnv(t, true)
	cookie, csrf, _ := e.login(adminPassword)

	if r := e.do("POST", "/api/auth/logout", "", nil, cookie); r.status != 403 || !strings.Contains(r.body, "csrf_failed") {
		t.Fatalf("missing CSRF: %d %s", r.status, r.body)
	}
	if r := e.do("POST", "/api/auth/logout", "", map[string]string{auth.CSRFHeader: "bad"}, cookie); r.status != 403 {
		t.Fatalf("wrong CSRF: %d", r.status)
	}
	if r := e.do("POST", "/api/auth/logout", "", map[string]string{
		auth.CSRFHeader: csrf, "Origin": "http://evil.example"}, cookie); r.status != 403 || !strings.Contains(r.body, "forbidden_origin") {
		t.Fatalf("foreign origin: %d %s", r.status, r.body)
	}
	if !e.sessionAuthenticated(cookie) {
		t.Fatal("rejected requests revoked the session")
	}
	if r := e.do("POST", "/api/auth/logout", "", nil, ""); r.status != 401 {
		t.Fatalf("no session: %d", r.status)
	}
	// A CSRF token from another session is not accepted.
	_, otherCSRF, _ := e.login(adminPassword)
	if r := e.do("POST", "/api/auth/logout", "", map[string]string{auth.CSRFHeader: otherCSRF}, cookie); r.status != 403 {
		t.Fatalf("CSRF from another session: %d", r.status)
	}
}

func TestLoginRejectsForeignOrigin(t *testing.T) {
	e := newEnv(t, true)
	b, _ := json.Marshal(map[string]string{"password": adminPassword})
	r := e.do("POST", "/api/auth/login", string(b), map[string]string{"Origin": "http://evil.example"}, "")
	if r.status != 403 {
		t.Fatalf("cross-origin login: %d", r.status)
	}
}

func TestSessionEndpointReportsCSRFOnlyWhenAuthenticated(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("GET", "/api/auth/session", "", nil, "")
	if r.status != 200 || strings.Contains(r.body, "csrf") || !strings.Contains(r.body, `"authenticated":false`) {
		t.Fatalf("anonymous: %d %s", r.status, r.body)
	}
	cookie, csrf, _ := e.login(adminPassword)
	r = e.do("GET", "/api/auth/session", "", nil, cookie)
	if !strings.Contains(r.body, csrf) || !strings.Contains(r.body, `"authenticated":true`) {
		t.Fatalf("authenticated: %s", r.body)
	}
}

func TestLogoutRevokesOnlyThatSession(t *testing.T) {
	e := newEnv(t, true)
	c1, csrf1, _ := e.login(adminPassword)
	c2, _, _ := e.login(adminPassword)
	r := e.do("POST", "/api/auth/logout", "", map[string]string{auth.CSRFHeader: csrf1}, c1)
	if r.status != 204 {
		t.Fatalf("logout: %d %s", r.status, r.body)
	}
	cleared := (&http.Response{Header: r.header}).Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", cleared)
	}
	if e.sessionAuthenticated(c1) {
		t.Fatal("logged-out session still valid")
	}
	if !e.sessionAuthenticated(c2) {
		t.Fatal("logout revoked another session")
	}
}

func TestPasswordChangeRevokesAllSessions(t *testing.T) {
	e := newEnv(t, true)
	c1, csrf1, _ := e.login(adminPassword)
	c2, _, _ := e.login(adminPassword)

	change := func(current, next string) resp {
		b, _ := json.Marshal(map[string]string{"current_password": current, "new_password": next})
		return e.do("PUT", "/api/auth/password", string(b), map[string]string{auth.CSRFHeader: csrf1}, c1)
	}
	if r := change("wrong password!", newPassword); r.status != 401 {
		t.Fatalf("wrong current password: %d", r.status)
	}
	if r := change(adminPassword, "short"); r.status != 422 || !strings.Contains(r.body, "new_password") {
		t.Fatalf("short new password: %d %s", r.status, r.body)
	}
	if !e.sessionAuthenticated(c1) {
		t.Fatal("failed changes revoked the session")
	}
	if r := change(adminPassword, newPassword); r.status != 204 {
		t.Fatalf("change: %d %s", r.status, r.body)
	}
	if e.sessionAuthenticated(c1) || e.sessionAuthenticated(c2) {
		t.Fatal("sessions survived the password change")
	}
	if _, _, r := e.login(adminPassword); r.status != 401 {
		t.Fatalf("old password still works: %d", r.status)
	}
	if cookie, _, r := e.login(newPassword); r.status != 200 || cookie == "" {
		t.Fatalf("new password rejected: %d", r.status)
	}
}

func TestUnknownAPIRouteIsJSON404(t *testing.T) {
	e := newEnv(t, true)
	r := e.do("GET", "/api/nope", "", nil, "")
	if r.status != 404 || !strings.Contains(r.body, `"code":"not_found"`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestSecretsNeverAppearInResponsesOrLogs(t *testing.T) {
	e := newEnv(t, true)
	e.login("wrong password!")
	cookie, csrf, _ := e.login(adminPassword)
	b, _ := json.Marshal(map[string]string{"current_password": adminPassword, "new_password": newPassword})
	e.do("PUT", "/api/auth/password", string(b), map[string]string{auth.CSRFHeader: csrf}, cookie)
	e.do("POST", "/api/auth/login", `{"password":"`+adminPassword+`"} garbage`, nil, "")
	hash, _ := e.db.AdminPasswordHash(context.Background())

	logs := e.logs.String()
	if !strings.Contains(logs, "login failed") {
		t.Fatal("expected a login failure log entry")
	}
	for _, secret := range []string{adminPassword, newPassword, "wrong password!", cookie, csrf, hash} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain a secret value %q", secret[:6])
		}
		for _, body := range e.bodies {
			if strings.Contains(body, secret) && secret != csrf {
				t.Errorf("a response body contains a secret value %q", secret[:6])
			}
		}
	}
}

func (e *env) setup(password string) (cookie, csrf string, r resp) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"password": password})
	r = e.do("POST", "/api/auth/setup", string(b), nil, "")
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == auth.CookieName {
			cookie = c.Value
		}
	}
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	return cookie, out.CSRF, r
}

func TestFirstRunSetupCreatesTheAdministratorAndSignsIn(t *testing.T) {
	e := newEnv(t, false)
	if r := e.do("GET", "/api/auth/session", "", nil, ""); !strings.Contains(r.body, `"needs_setup":true`) {
		t.Fatalf("session before setup: %s", r.body)
	}
	cookie, csrf, r := e.setup(adminPassword)
	if r.status != 200 || cookie == "" || csrf == "" {
		t.Fatalf("setup: %d %s", r.status, r.body)
	}
	if !e.sessionAuthenticated(cookie) {
		t.Fatal("the setup session is not authenticated")
	}
	if strings.Contains(r.body, adminPassword) {
		t.Fatal("the password appears in the response")
	}
	if r := e.do("GET", "/api/auth/session", "", nil, ""); strings.Contains(r.body, `"needs_setup":true`) {
		t.Fatalf("setup still offered after it was done: %s", r.body)
	}
	if _, _, r := e.login(adminPassword); r.status != 200 {
		t.Fatalf("login with the new password: %d", r.status)
	}
}

func TestSetupIsRefusedOnceAnAdministratorExists(t *testing.T) {
	e := newEnv(t, true)
	if _, _, r := e.setup(newPassword); r.status != 409 || !strings.Contains(r.body, "already_initialized") {
		t.Fatalf("setup over an administrator: %d %s", r.status, r.body)
	}
	if _, _, r := e.login(adminPassword); r.status != 200 {
		t.Fatalf("the original password stopped working: %d", r.status)
	}
	if _, _, r := e.login(newPassword); r.status != 401 {
		t.Fatalf("the attacker's password works: %d", r.status)
	}
}

func TestSetupRejectsShortPasswordsAndForeignOrigins(t *testing.T) {
	e := newEnv(t, false)
	if _, _, r := e.setup("short"); r.status != 422 {
		t.Fatalf("short password: %d %s", r.status, r.body)
	}
	b, _ := json.Marshal(map[string]string{"password": adminPassword})
	if r := e.do("POST", "/api/auth/setup", string(b), map[string]string{"Origin": "http://evil.example"}, ""); r.status != 403 {
		t.Fatalf("foreign origin: %d", r.status)
	}
	if r := e.do("GET", "/api/auth/session", "", nil, ""); !strings.Contains(r.body, `"needs_setup":true`) {
		t.Fatalf("a rejected setup must leave the installation open: %s", r.body)
	}
}

func TestConcurrentSetupHasExactlyOneWinner(t *testing.T) {
	e := newEnv(t, false)
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, r := e.setup("first-run password " + string(rune('a'+i)))
			switch r.status {
			case 200:
				wins.Add(1)
			case 409:
				conflicts.Add(1)
			default:
				t.Errorf("unexpected status %d: %s", r.status, r.body)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 3 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}
