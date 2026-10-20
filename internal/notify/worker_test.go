package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/secrets"
	"proxy-checker/internal/store"
	"proxy-checker/internal/testkit"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

type rig struct {
	t     *testing.T
	db    *store.Store
	path  string
	clock *testkit.Clock
	bot   *botAPI
	tg    *Telegram
	w     *Worker
	logs  *bytes.Buffer
	n     int
}

func newRig(t *testing.T, configured bool) *rig {
	t.Helper()
	ctx := context.Background()
	bot, client := newBotAPI(t)
	r := &rig{t: t, path: filepath.Join(t.TempDir(), "state.db"), clock: testkit.NewClock(t0), bot: bot, logs: &bytes.Buffer{}}
	r.open()
	if configured {
		tok, chat := testToken, testChat
		if err := r.db.SetTelegram(ctx, store.TelegramUpdate{Token: &tok, ChatID: &chat}, t0); err != nil {
			t.Fatal(err)
		}
	}
	r.tg = bot.telegram(client)
	r.tg.Token = StoreToken(r.db)
	r.build()
	return r
}

func (r *rig) seq() int {
	r.n++
	return r.n
}

func (r *rig) open() {
	ctx := context.Background()
	if r.db != nil {
		r.db.Close()
	}
	db, err := store.Open(ctx, r.path)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		r.t.Fatal(err)
	}
	c, err := secrets.Load(filepath.Dir(r.path))
	if err != nil {
		r.t.Fatal(err)
	}
	db.UseSecrets(c)
	r.t.Cleanup(func() { db.Close() })
	r.db = db
}

func (r *rig) build() {
	logger := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.tg.Token = StoreToken(r.db)
	r.w = &Worker{Store: r.db, Telegram: r.tg, Clock: r.clock, Logger: logger}
}

func (r *rig) proxy(name string) domain.ProxyID {
	r.t.Helper()
	id, err := r.db.InsertProxy(context.Background(), domain.Proxy{
		Name: name, Protocol: domain.ProtocolHTTP, Host: fmt.Sprintf("h%d.lan", r.seq()), Port: 1,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *rig) result(id domain.ProxyID, at time.Time, st domain.State) {
	r.t.Helper()
	a := domain.Availability{ProxyID: id, CheckedAt: at, State: st, Endpoint: "ipify"}
	if st == domain.StateDown {
		a.Failure = &domain.Failure{Code: "auth_failed", Message: "m"}
	}
	if _, err := r.db.RecordAvailabilityOutcome(context.Background(), a, 1); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) down(id domain.ProxyID, at time.Time) { r.result(id, at, domain.StateDown) }

func (r *rig) deliver(now time.Time) {
	r.t.Helper()
	r.clock.Advance(now.Sub(r.clock.Now()))
	if err := r.w.DeliverDue(context.Background(), now); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) delivery(id domain.ProxyID, kind string) store.EventRow {
	r.t.Helper()
	events, _, err := r.db.ListEvents(context.Background(), store.EventFilter{ProxyID: &id, Kinds: []string{kind}, Limit: 10})
	if err != nil || len(events) != 1 {
		r.t.Fatalf("events for proxy %d: %v %v", id, events, err)
	}
	return events[0]
}

func TestSingleEventIsSentAsPlainText(t *testing.T) {
	r := newRig(t, true)
	id := r.proxy("Office")
	r.down(id, t0)
	r.deliver(t0.Add(10 * time.Second))
	texts := r.bot.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], `"Office"`) || !strings.Contains(texts[0], "down") || strings.Contains(texts[0], "\n") {
		t.Fatalf("%q", texts)
	}
	if d := r.delivery(id, "proxy_down").Delivery; d.Status != store.DeliveryDelivered || d.Attempts != 0 || d.DeliveredAt == nil {
		t.Fatalf("%+v", d)
	}
	r.deliver(t0.Add(time.Hour))
	if r.bot.requests() != 1 {
		t.Fatal("a delivered notification was sent again")
	}
}

func TestChangesWithinTheWindowAreGroupedIntoOneMessage(t *testing.T) {
	r := newRig(t, true)
	a, b := r.proxy("Alpha"), r.proxy("Beta")
	r.down(a, t0)
	r.down(b, t0.Add(3*time.Second))
	r.deliver(t0.Add(9 * time.Second))
	if r.bot.requests() != 0 {
		t.Fatal("sent before the grouping window ended")
	}
	r.deliver(t0.Add(13 * time.Second))
	texts := r.bot.texts()
	if len(texts) != 1 {
		t.Fatalf("%d messages, want 1", len(texts))
	}
	lines := strings.Split(texts[0], "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "2") || !strings.Contains(lines[1], "Alpha") || !strings.Contains(lines[2], "Beta") {
		t.Fatalf("%q", texts[0])
	}
}

func TestRecoveryIsGroupedWithTheDownEventInOrder(t *testing.T) {
	r := newRig(t, true)
	id := r.proxy("Gateway")
	r.down(id, t0)
	r.result(id, t0.Add(4*time.Second), domain.StateUp)
	r.deliver(t0.Add(20 * time.Second))
	texts := r.bot.texts()
	if len(texts) != 1 {
		t.Fatalf("%q", texts)
	}
	down, up := strings.Index(texts[0], "down"), strings.Index(texts[0], "up again")
	if down < 0 || up < 0 || down > up {
		t.Fatalf("down must precede recovery: %q", texts[0])
	}
	if r.delivery(id, "proxy_recovered").Delivery.Status != store.DeliveryDelivered {
		t.Fatal("recovery not marked delivered")
	}
}

func TestLongGroupsAreSplitWithoutDroppingEvents(t *testing.T) {
	r := newRig(t, true)
	const n = 60
	markers := make([]string, n)
	for i := 0; i < n; i++ {
		markers[i] = fmt.Sprintf("MARK%03d", i)
		r.down(r.proxy(markers[i]+strings.Repeat("x", 90)), t0.Add(time.Duration(i)*50*time.Millisecond))
	}
	r.deliver(t0.Add(time.Minute))
	texts := r.bot.texts()
	if len(texts) < 2 {
		t.Fatalf("%d messages: a group this size must be split", len(texts))
	}
	all := strings.Join(texts, "\n")
	last := -1
	for i, m := range markers {
		if c := strings.Count(all, m); c != 1 {
			t.Fatalf("%s appears %d times", m, c)
		}
		if pos := strings.Index(all, m); pos < last {
			t.Fatalf("event %d out of order", i)
		} else {
			last = pos
		}
	}
	for _, txt := range texts {
		if utf8.RuneCountInString(txt) > MaxMessageRunes {
			t.Fatalf("message of %d characters", utf8.RuneCountInString(txt))
		}
	}
	if sum, _ := r.db.OutboxSummary(context.Background()); sum.Pending != 0 || sum.Failed != 0 {
		t.Fatalf("%+v", sum)
	}
}

func TestRetryScheduleIs30_60_120_240ThenGivesUp(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(reply(500, `{"ok":false}`))
	id := r.proxy("Flaky")
	r.down(id, t0)

	now := t0.Add(10 * time.Second)
	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second}
	for i, d := range want {
		before := r.bot.requests()
		r.deliver(now)
		if r.bot.requests() != before+1 {
			t.Fatalf("attempt %d: no request", i+1)
		}
		// Not yet due just before the delay ends...
		r.deliver(now.Add(d - time.Second))
		if r.bot.requests() != before+1 {
			t.Fatalf("attempt %d retried %v early", i+1, time.Second)
		}
		// ...and due exactly when it ends.
		now = now.Add(d)
		ev := r.delivery(id, "proxy_down").Delivery
		if ev.Status != store.DeliveryPending || ev.Attempts != i+1 || ev.LastError != "server_error" {
			t.Fatalf("after attempt %d: %+v", i+1, ev)
		}
	}
	r.deliver(now) // fifth and last attempt
	ev := r.delivery(id, "proxy_down").Delivery
	if ev.Status != store.DeliveryFailed || ev.Attempts != 5 || ev.LastError != "server_error" {
		t.Fatalf("final failure not recorded: %+v", ev)
	}
	total := r.bot.requests()
	r.deliver(now.Add(48 * time.Hour))
	if r.bot.requests() != total || total != 5 {
		t.Fatalf("requests: %d, want exactly 5 attempts", total)
	}
	if sum, _ := r.db.OutboxSummary(context.Background()); sum.Failed != 1 || sum.Pending != 0 {
		t.Fatalf("%+v", sum)
	}
}

func TestTelegramRetryAfterTakesPriorityOverTheFixedDelay(t *testing.T) {
	for _, retry := range []int{5, 100} {
		r := newRig(t, true)
		r.bot.set(reply(429, fmt.Sprintf(`{"ok":false,"error_code":429,"parameters":{"retry_after":%d}}`, retry)))
		id := r.proxy("Limited")
		r.down(id, t0)
		now := t0.Add(10 * time.Second)
		r.deliver(now)
		n := r.bot.requests()
		r.deliver(now.Add(time.Duration(retry)*time.Second - time.Second))
		if r.bot.requests() != n {
			t.Fatalf("retry_after=%d: retried before Telegram allowed it", retry)
		}
		r.deliver(now.Add(time.Duration(retry) * time.Second))
		if r.bot.requests() != n+1 {
			t.Fatalf("retry_after=%d: not retried when Telegram allowed it (%d requests)", retry, r.bot.requests())
		}
		if ev := r.delivery(id, "proxy_down").Delivery; ev.LastError != "rate_limited" || ev.Attempts != 2 {
			t.Fatalf("%+v", ev)
		}
	}
}

func TestDeliverySucceedsAfterEarlierFailures(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(func(w http.ResponseWriter, req *http.Request, n int) {
		if n <= 2 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	})
	id := r.proxy("Eventually")
	r.down(id, t0)
	now := t0.Add(10 * time.Second)
	r.deliver(now)
	r.deliver(now.Add(30 * time.Second))
	r.deliver(now.Add(90 * time.Second))
	ev := r.delivery(id, "proxy_down").Delivery
	if ev.Status != store.DeliveryDelivered || ev.Attempts != 2 || ev.LastError != "" {
		t.Fatalf("%+v", ev)
	}
}

func TestRetrySchedulePersistsAcrossRestart(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(reply(500, ``))
	id := r.proxy("Persistent")
	r.down(id, t0)
	now := t0.Add(10 * time.Second)
	r.deliver(now)

	r.open() // restart: new connection, new worker
	r.build()
	n := r.bot.requests()
	r.deliver(now.Add(29 * time.Second))
	if r.bot.requests() != n {
		t.Fatal("the retry delay was forgotten after a restart")
	}
	r.bot.set(nil)
	r.deliver(now.Add(30 * time.Second))
	if r.bot.requests() != n+1 || r.delivery(id, "proxy_down").Delivery.Status != store.DeliveryDelivered {
		t.Fatal("not retried after the restart")
	}
}

func TestHostileNamesCannotInjectContentOrFormatting(t *testing.T) {
	r := newRig(t, true)
	names := []string{
		"<b>bold</b> & <a href=\"https://evil.example\">link</a>",
		"*markdown* _italic_ `code` [x](https://evil.example)",
		"line one\nProxy \"Other\" is up again.\nFAKE",
		"‮evil‬ rtl override",
		strings.Repeat("N", 5000),
		"nul\x00byte\ttab\rcr",
	}
	for i, n := range names {
		r.down(r.proxy(n), t0.Add(time.Duration(i)*time.Millisecond))
	}
	// A message stored with raw control characters (older data, manual edits) is also neutralised.
	id := r.proxy("raw")
	r.down(id, t0.Add(time.Second))
	r.deliver(t0.Add(time.Minute))
	all := strings.Join(r.bot.texts(), "\n")
	if strings.ContainsAny(all, "\x00\r\t‮‬") {
		t.Fatalf("control or bidi characters reached Telegram: %q", all)
	}
	for _, body := range r.bot.bodies {
		if _, has := body["parse_mode"]; has {
			t.Fatal("markup parsing was enabled")
		}
		if txt, _ := body["text"].(string); utf8.RuneCountInString(txt) > MaxMessageRunes {
			t.Fatalf("message of %d characters", utf8.RuneCountInString(txt))
		}
	}
	// Every event is exactly one line, so a name cannot add fake lines.
	for _, txt := range r.bot.texts() {
		lines := strings.Split(txt, "\n")
		if len(lines) > 1 && !strings.HasPrefix(lines[0], "Proxy Checker:") {
			t.Fatalf("unexpected layout: %q", txt)
		}
		for _, l := range lines[1:] {
			if !strings.HasPrefix(l, "- ") {
				t.Fatalf("a forged line got through: %q in %q", l, txt)
			}
		}
	}
	if sum, _ := r.db.OutboxSummary(context.Background()); sum.Pending != 0 {
		t.Fatalf("%+v", sum)
	}
}

func TestEventTextWithRawNewlinesIsNeutralised(t *testing.T) {
	line := lineFor(store.OutboxItem{EventKind: "proxy_down", EventID: ptr(int64(1)), EventMessage: "Proxy is down.\nProxy \"x\" is up again.‮"})
	if strings.ContainsAny(line, "\n\r‮") {
		t.Fatalf("%q", line)
	}
}

func ptr[T any](v T) *T { return &v }

func TestNothingIsSentOrCountedWithoutConfiguration(t *testing.T) {
	r := newRig(t, false)
	id := r.proxy("Waiting")
	r.down(id, t0)
	r.deliver(t0.Add(time.Minute))
	if r.bot.requests() != 0 {
		t.Fatal("sent without configuration")
	}
	if ev := r.delivery(id, "proxy_down").Delivery; ev.Status != store.DeliveryPending || ev.Attempts != 0 {
		t.Fatalf("an unconfigured bot consumed attempts: %+v", ev)
	}
	// Once configured, the waiting notification is delivered.
	tok, chat := testToken, testChat
	if err := r.db.SetTelegram(context.Background(), store.TelegramUpdate{Token: &tok, ChatID: &chat}, t0); err != nil {
		t.Fatal(err)
	}
	r.deliver(t0.Add(2 * time.Minute))
	if r.bot.requests() != 1 {
		t.Fatalf("%d requests", r.bot.requests())
	}
	// A token without a chat is not configured either.
	empty := ""
	_ = r.db.SetTelegram(context.Background(), store.TelegramUpdate{ChatID: &empty}, t0)
	r.down(r.proxy("Second"), t0.Add(3*time.Minute))
	r.deliver(t0.Add(4 * time.Minute))
	if r.bot.requests() != 1 {
		t.Fatal("sent without a chat ID")
	}
}

func TestTestMessagesAreDeliveredAndReported(t *testing.T) {
	r := newRig(t, true)
	id, err := r.db.EnqueueTestMessage(context.Background(), t0)
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	r.deliver(t0)
	texts := r.bot.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "Test") || strings.Contains(texts[0], "Proxy Checker: 1") {
		t.Fatalf("%q", texts)
	}
	sum, _ := r.db.OutboxSummary(context.Background())
	if sum.Pending != 0 || sum.LastDeliveredAt == nil {
		t.Fatalf("%+v", sum)
	}
}

func TestOnlyTheFailedPartIsRetried(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(func(w http.ResponseWriter, req *http.Request, n int) {
		if n >= 2 {
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{}}`)
	})
	ids := make([]domain.ProxyID, 50)
	for i := range ids {
		ids[i] = r.proxy(fmt.Sprintf("P%02d", i) + strings.Repeat("y", 150))
		r.down(ids[i], t0.Add(time.Duration(i)*time.Millisecond))
	}
	r.deliver(t0.Add(time.Minute))
	if r.bot.requests() != 2 {
		t.Fatalf("%d requests: delivery must stop at the first failing part", r.bot.requests())
	}
	var delivered, retried, untouched int
	for _, id := range ids {
		switch d := r.delivery(id, "proxy_down").Delivery; {
		case d.Status == store.DeliveryDelivered:
			delivered++
		case d.Attempts == 1:
			retried++
		case d.Attempts == 0:
			untouched++
		}
	}
	if delivered == 0 || retried == 0 || untouched == 0 || delivered+retried+untouched != 50 {
		t.Fatalf("delivered=%d retried=%d untouched=%d", delivered, retried, untouched)
	}
}

func TestAHangingTelegramDoesNotBlockForLong(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(func(w http.ResponseWriter, req *http.Request, n int) { time.Sleep(700 * time.Millisecond) })
	r.tg.Timeout = 100 * time.Millisecond
	id := r.proxy("Slow")
	r.down(id, t0)
	start := time.Now()
	r.deliver(t0.Add(time.Minute))
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if ev := r.delivery(id, "proxy_down").Delivery; ev.LastError != "timeout" || ev.Attempts != 1 {
		t.Fatalf("%+v", ev)
	}
}

func TestShutdownDuringDeliveryIsNotAFailure(t *testing.T) {
	r := newRig(t, true)
	r.bot.set(func(w http.ResponseWriter, req *http.Request, n int) { time.Sleep(time.Second) })
	id := r.proxy("Stopping")
	r.down(id, t0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	err := r.w.DeliverDue(ctx, t0.Add(time.Minute))
	if err == nil {
		t.Fatal("expected the cancellation to be returned")
	}
	if ev := r.delivery(id, "proxy_down").Delivery; ev.Attempts != 0 || ev.Status != store.DeliveryPending {
		t.Fatalf("shutdown counted as a failed attempt: %+v", ev)
	}
}

func TestTokenNeverAppearsInLogsOrStoredErrors(t *testing.T) {
	r := newRig(t, true)
	for _, h := range []func(http.ResponseWriter, *http.Request, int){
		reply(500, `{"ok":false,"description":"`+testToken+`"}`),
		reply(401, `{"ok":false}`),
		func(w http.ResponseWriter, req *http.Request, n int) { time.Sleep(300 * time.Millisecond) },
	} {
		r.bot.set(h)
		r.tg.Timeout = 100 * time.Millisecond
		id := r.proxy("Leak")
		r.down(id, t0)
		r.deliver(t0.Add(time.Minute))
		if ev := r.delivery(id, "proxy_down").Delivery; strings.Contains(ev.LastError, "AAH") {
			t.Fatalf("stored error leaks: %q", ev.LastError)
		}
	}
	if !strings.Contains(r.logs.String(), "delivery failed") {
		t.Fatal("expected failure log lines")
	}
	if strings.Contains(r.logs.String(), testToken) || strings.Contains(r.logs.String(), "AAH_test") {
		t.Fatalf("logs leak the token: %s", r.logs.String())
	}
}

func TestRunDeliversOnItsOwnAndStops(t *testing.T) {
	r := newRig(t, true)
	r.w.Clock = domain.SystemClock{}
	r.w.Interval = 10 * time.Millisecond
	id := r.proxy("Auto")
	r.down(id, time.Now().Add(-time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.w.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for r.bot.requests() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	if r.bot.requests() != 1 {
		t.Fatalf("%d requests", r.bot.requests())
	}
}
