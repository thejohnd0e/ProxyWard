package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"proxy-checker/internal/testkit"
)

const (
	testToken = "123456789:AAH_test-TOKEN-value-1234567890abcd"
	testChat  = "-1001234567890"
)

// botAPI is a fake Telegram Bot API.
type botAPI struct {
	*testkit.Target
	mu       sync.Mutex
	paths    []string
	bodies   []map[string]any
	rawBody  []string
	handler  func(w http.ResponseWriter, r *http.Request, n int)
	sentText []string
}

func newBotAPI(t *testing.T) (*botAPI, *http.Client) {
	t.Helper()
	ca := testkit.NewCA(t)
	b := &botAPI{}
	b.Target = testkit.NewTarget(t, ca, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		b.mu.Lock()
		b.paths = append(b.paths, r.URL.Path)
		b.rawBody = append(b.rawBody, string(raw))
		b.bodies = append(b.bodies, body)
		if txt, ok := body["text"].(string); ok {
			b.sentText = append(b.sentText, txt)
		}
		n := len(b.paths)
		h := b.handler
		b.mu.Unlock()
		if h != nil {
			h(w, r, n)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	})
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return b, client
}

func (b *botAPI) set(h func(w http.ResponseWriter, r *http.Request, n int)) {
	b.mu.Lock()
	b.handler = h
	b.mu.Unlock()
}

func (b *botAPI) texts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.sentText...)
}

func (b *botAPI) requests() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.paths)
}

func (b *botAPI) telegram(client *http.Client) *Telegram {
	return &Telegram{
		BaseURL: b.URL, Client: client, Timeout: 2 * time.Second,
		Token: func(context.Context) (string, bool, error) { return testToken, true, nil },
	}
}

func reply(status int, body string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func sendCode(err error) string {
	var se *SendError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestSendPostsPlainTextToTheBotEndpoint(t *testing.T) {
	b, c := newBotAPI(t)
	if err := b.telegram(c).Send(context.Background(), testChat, "Proxy <b>one</b> is down"); err != nil {
		t.Fatal(err)
	}
	if b.paths[0] != "/bot"+testToken+"/sendMessage" {
		t.Fatalf("path %q", b.paths[0])
	}
	body := b.bodies[0]
	if body["chat_id"] != testChat || body["text"] != "Proxy <b>one</b> is down" {
		t.Fatalf("%v", body)
	}
	if _, has := body["parse_mode"]; has {
		t.Fatal("parse_mode must not be set: names are sent as plain text")
	}
	if strings.Contains(b.rawBody[0], testToken) {
		t.Fatal("the token must only appear in the URL path, never in the body")
	}
}

func TestSendFailuresAreClassifiedAndRetryAfterIsKept(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		hdr    string
		code   string
		retry  time.Duration
	}{
		"ok false on 200":   {200, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, "", "bad_request", 0},
		"ok false 403":      {200, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked"}`, "", "forbidden", 0},
		"ok false 429":      {200, `{"ok":false,"error_code":429,"parameters":{"retry_after":17}}`, "", "rate_limited", 17 * time.Second},
		"http 429 params":   {429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 9","parameters":{"retry_after":9}}`, "", "rate_limited", 9 * time.Second},
		"http 429 header":   {429, `{"ok":false}`, "12", "rate_limited", 12 * time.Second},
		"http 401":          {401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, "", "unauthorized", 0},
		"http 403":          {403, `{"ok":false,"error_code":403}`, "", "forbidden", 0},
		"http 400":          {400, `{"ok":false,"error_code":400}`, "", "bad_request", 0},
		"http 404":          {404, `{"ok":false}`, "", "not_found", 0},
		"http 500":          {500, `oops`, "", "server_error", 0},
		"http 502":          {502, ``, "", "server_error", 0},
		"redirect":          {302, ``, "", "rejected", 0},
		"ok missing":        {200, `{"result":{}}`, "", "invalid_response", 0},
		"not json":          {200, `<html>`, "", "invalid_response", 0},
		"empty":             {200, ``, "", "invalid_response", 0},
		"ok true and false": {200, `{"ok":"yes"}`, "", "invalid_response", 0},
	} {
		b, c := newBotAPI(t)
		b.set(func(w http.ResponseWriter, r *http.Request, n int) {
			if tc.hdr != "" {
				w.Header().Set("Retry-After", tc.hdr)
			}
			w.Header().Set("Location", "https://elsewhere.example/")
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		err := b.telegram(c).Send(context.Background(), testChat, "hello")
		var se *SendError
		if !errors.As(err, &se) || se.Code != tc.code || se.RetryAfter != tc.retry {
			t.Errorf("%s: %v (retry %v)", name, err, se)
			continue
		}
		if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "bot was blocked") {
			t.Errorf("%s: error leaks token or upstream text: %v", name, err)
		}
	}
}

func TestSendNeverLeaksTheTokenInNetworkErrors(t *testing.T) {
	b, c := newBotAPI(t)
	tg := b.telegram(c)

	// Server gone: the transport error would normally contain the full URL.
	url := b.URL
	b.Close()
	tg.BaseURL = url
	err := tg.Send(context.Background(), testChat, "x")
	if sendCode(err) != "network" || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "bot") {
		t.Fatalf("connection refused: %v", err)
	}

	// Timeout.
	b2, c2 := newBotAPI(t)
	b2.set(func(w http.ResponseWriter, r *http.Request, n int) { time.Sleep(600 * time.Millisecond) })
	tg2 := b2.telegram(c2)
	tg2.Timeout = 100 * time.Millisecond
	start := time.Now()
	err = tg2.Send(context.Background(), testChat, "x")
	if sendCode(err) != "timeout" || strings.Contains(err.Error(), testToken) || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}

	// Cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = b2.telegram(c2).Send(ctx, testChat, "x")
	if sendCode(err) != "canceled" || strings.Contains(err.Error(), testToken) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestSendValidatesInputBeforeAnyRequest(t *testing.T) {
	b, c := newBotAPI(t)
	tg := b.telegram(c)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		chat, text string
		code       string
	}{
		"empty text":  {testChat, "", "bad_request"},
		"too long":    {testChat, strings.Repeat("a", 4097), "too_long"},
		"empty chat":  {"", "x", "invalid_chat"},
		"chat inject": {"1/../x", "x", "invalid_chat"},
		"chat space":  {"12 34", "x", "invalid_chat"},
		"chat name":   {"@ab", "x", "invalid_chat"},
	} {
		if err := tg.Send(ctx, tc.chat, tc.text); sendCode(err) != tc.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := tg.Send(ctx, "@good_channel", strings.Repeat("é", 4096)); err != nil {
		t.Errorf("exactly 4096 characters must be accepted: %v", err)
	}
	// Characters, not bytes, are limited.
	if b.requests() != 1 {
		t.Fatalf("%d requests; only the valid message may be sent", b.requests())
	}

	for name, tok := range map[string]string{
		"path injection": "123:abc/../../x",
		"query":          "123456789:AAH_test-TOKEN?x=1",
		"space":          "123456789:AAH test-TOKEN-value-1234567890",
		"no colon":       "AAHtestTOKENvalue1234567890abcdefghij",
		"short":          "1:abc",
		"fragment":       "123456789:AAH_test-TOKEN#frag-1234567890",
	} {
		tg := b.telegram(c)
		tg.Token = func(context.Context) (string, bool, error) { return tok, true, nil }
		if err := tg.Send(ctx, testChat, "x"); sendCode(err) != "invalid_token" {
			t.Errorf("%s: %v", name, err)
		}
	}
	tg.Token = func(context.Context) (string, bool, error) { return "", false, nil }
	if err := tg.Send(ctx, testChat, "x"); sendCode(err) != "not_configured" {
		t.Errorf("no token: %v", err)
	}
	boom := errors.New("decrypt failed")
	tg.Token = func(context.Context) (string, bool, error) { return "", false, boom }
	if err := tg.Send(ctx, testChat, "x"); !errors.Is(err, boom) {
		t.Errorf("token source error: %v", err)
	}
	if b.requests() != 1 {
		t.Fatalf("invalid input produced requests: %d", b.requests())
	}
}

func TestClientIsDirectAndDoesNotFollowRedirects(t *testing.T) {
	tg := NewTelegram(func(context.Context) (string, bool, error) { return testToken, true, nil })
	if tg.BaseURL != TelegramURL || !strings.HasPrefix(tg.BaseURL, "https://") || tg.Timeout != 15*time.Second {
		t.Fatalf("%+v", tg)
	}
	tr, ok := tg.Client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("Telegram must be reached directly, not through checked proxies or the environment: %#v", tg.Client.Transport)
	}
	if tg.Client.CheckRedirect == nil {
		t.Fatal("redirects must not be followed")
	}
}

func TestAnAcceptedButUnconfirmedMessageIsSentAgain(t *testing.T) {
	// The service accepts the message, then the response is lost. The sender
	// cannot know, retries, and the recipient sees it twice: delivery is
	// at-least-once, never exactly-once.
	b, c := newBotAPI(t)
	b.set(func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			time.Sleep(500 * time.Millisecond) // the reply arrives after the client gave up
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	})
	tg := b.telegram(c)
	tg.Timeout = 100 * time.Millisecond
	if err := tg.Send(context.Background(), testChat, "same message"); sendCode(err) != "timeout" {
		t.Fatalf("%v", err)
	}
	tg.Timeout = 2 * time.Second
	if err := tg.Send(context.Background(), testChat, "same message"); err != nil {
		t.Fatal(err)
	}
	if got := b.texts(); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("recipient saw %v", got)
	}
}
