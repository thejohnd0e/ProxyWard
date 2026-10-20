package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// makeEvent creates a down event (and its outbox row) for a proxy.
func makeEvent(t *testing.T, db *Store, id domain.ProxyID, at time.Time) IncidentOutcome {
	t.Helper()
	ctx := context.Background()
	var last IncidentOutcome
	for i := 0; i < 3; i++ {
		o, err := db.RecordAvailabilityOutcome(ctx, avail(id, at, domain.StateDown, "x"), 3)
		if err != nil {
			t.Fatal(err)
		}
		last = o
	}
	if last.Transition != TransitionDown {
		t.Fatalf("%+v", last)
	}
	// Recover so that the next call can create another incident.
	if _, err := db.RecordAvailabilityOutcome(ctx, avail(id, at, domain.StateUp, ""), 3); err != nil {
		t.Fatal(err)
	}
	return last
}

func TestOutboxDueWindowAndOrder(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	makeEvent(t, db, ids[0], t0)
	makeEvent(t, db, ids[1], t0.Add(5*time.Second))

	if items, err := db.DueOutbox(ctx, t0.Add(9*time.Second), 10); err != nil || len(items) != 0 {
		t.Fatalf("items are due before the grouping window ended: %+v %v", items, err)
	}
	items, err := db.DueOutbox(ctx, t0.Add(10*time.Second), 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("both events were created by now (the recovery rows too): %+v %v", items, err)
	}
	items, _ = db.DueOutbox(ctx, t0.Add(time.Hour), 10)
	if len(items) != 4 {
		t.Fatalf("expected 2 down + 2 recovered rows, got %d", len(items))
	}
	for i := 1; i < len(items); i++ {
		if items[i].CreatedAt.Before(items[i-1].CreatedAt) {
			t.Fatalf("not in creation order: %+v", items)
		}
	}
	first := items[0]
	if first.EventKind != "proxy_down" || !strings.Contains(first.EventMessage, "down") || first.EventID == nil || first.Attempts != 0 {
		t.Fatalf("%+v", first)
	}
	if limited, _ := db.DueOutbox(ctx, t0.Add(time.Hour), 2); len(limited) != 2 {
		t.Fatalf("limit ignored: %d", len(limited))
	}
}

func TestOutboxDeliveryAndRetryBookkeeping(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	makeEvent(t, db, id, t0)
	now := t0.Add(time.Minute)
	items, _ := db.DueOutbox(ctx, now, 10)
	down := items[0]

	attempts, err := db.MarkOutboxFailed(ctx, down.ID, now, "server_error", now.Add(30*time.Second))
	if err != nil || attempts != 1 {
		t.Fatalf("%d %v", attempts, err)
	}
	if due, _ := db.DueOutbox(ctx, now.Add(29*time.Second), 10); containsOutbox(due, down.ID) {
		t.Fatal("a failed item was retried before its delay passed")
	}
	due, _ := db.DueOutbox(ctx, now.Add(30*time.Second), 10)
	if !containsOutbox(due, down.ID) {
		t.Fatal("a failed item is not retried after its delay")
	}
	got := findOutbox(due, down.ID)
	if got.Attempts != 1 || got.LastError != "server_error" {
		t.Fatalf("%+v", got)
	}

	if err := db.MarkOutboxDelivered(ctx, []int64{down.ID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if due, _ := db.DueOutbox(ctx, now.Add(time.Hour), 10); containsOutbox(due, down.ID) {
		t.Fatal("a delivered item is due again")
	}
	// Late failures cannot un-deliver an item.
	if _, err := db.MarkOutboxFailed(ctx, down.ID, now, "x", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failing a delivered item: %v", err)
	}
	if err := db.MarkOutboxDelivered(ctx, nil, now); err != nil {
		t.Fatalf("empty delivery: %v", err)
	}
}

func containsOutbox(items []OutboxItem, id int64) bool { return findOutbox(items, id).ID == id }

func findOutbox(items []OutboxItem, id int64) OutboxItem {
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	return OutboxItem{}
}

func TestOutboxStopsAfterMaxAttemptsAndStaysVisible(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	makeEvent(t, db, id, t0)
	items, _ := db.DueOutbox(ctx, t0.Add(time.Minute), 10)
	target := items[0].ID
	for i := 1; i <= MaxOutboxAttempts; i++ {
		n, err := db.MarkOutboxFailed(ctx, target, t0, "timeout", t0)
		if err != nil || n != i {
			t.Fatalf("attempt %d: %d %v", i, n, err)
		}
	}
	if due, _ := db.DueOutbox(ctx, t0.Add(24*time.Hour), 10); containsOutbox(due, target) {
		t.Fatal("an item that used all its attempts is still being retried")
	}
	sum, err := db.OutboxSummary(ctx)
	if err != nil || sum.Failed != 1 || sum.LastError != "timeout" {
		t.Fatalf("%+v %v", sum, err)
	}
	events, _, _ := db.ListEvents(ctx, EventFilter{Limit: 10})
	var seen bool
	for _, e := range events {
		if e.Kind == "proxy_down" {
			seen = true
			if e.Delivery.Status != DeliveryFailed || e.Delivery.Attempts != MaxOutboxAttempts || e.Delivery.LastError != "timeout" {
				t.Fatalf("final failure not visible: %+v", e.Delivery)
			}
		}
	}
	if !seen {
		t.Fatal("event missing")
	}
}

func TestTestMessagesAreQueuedImmediately(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, err := db.EnqueueTestMessage(ctx, t0)
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	items, _ := db.DueOutbox(ctx, t0, 10)
	if len(items) != 1 || items[0].ID != id || items[0].EventID != nil || items[0].Kind() != "test" {
		t.Fatalf("%+v", items)
	}
	id2, _ := db.EnqueueTestMessage(ctx, t0)
	if id2 == id {
		t.Fatal("test messages must be separate rows")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM events`); n != 0 {
		t.Fatalf("a test message created %d events", n)
	}
}

func TestOutboxSummary(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	sum, err := db.OutboxSummary(ctx)
	if err != nil || sum != (OutboxSummary{}) {
		t.Fatalf("%+v %v", sum, err)
	}
	a, _ := db.EnqueueTestMessage(ctx, t0)
	b, _ := db.EnqueueTestMessage(ctx, t0)
	c, _ := db.EnqueueTestMessage(ctx, t0)
	_ = db.MarkOutboxDelivered(ctx, []int64{a}, t0.Add(time.Minute))
	_, _ = db.MarkOutboxFailed(ctx, b, t0, "rate_limited", t0)
	for i := 0; i < MaxOutboxAttempts; i++ {
		_, _ = db.MarkOutboxFailed(ctx, c, t0, "forbidden", t0)
	}
	sum, _ = db.OutboxSummary(ctx)
	if sum.Pending != 1 || sum.Failed != 1 || sum.LastDeliveredAt == nil || !sum.LastDeliveredAt.Equal(t0.Add(time.Minute)) || sum.LastError == "" {
		t.Fatalf("%+v", sum)
	}
}

func TestTelegramSettings(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	cfg, err := db.GetTelegram(ctx)
	if err != nil || cfg != (TelegramConfig{}) {
		t.Fatalf("%+v %v", cfg, err)
	}
	tok, chat := "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ", "-100123"
	if err := db.SetTelegram(ctx, TelegramUpdate{Token: &tok, ChatID: &chat}, t0); err != nil {
		t.Fatal(err)
	}
	cfg, _ = db.GetTelegram(ctx)
	if cfg.Token != tok || cfg.ChatID != chat || !cfg.Configured() {
		t.Fatalf("%+v", cfg)
	}
	var raw string
	_ = db.db.QueryRow(`SELECT value FROM settings WHERE key = 'telegram.bot_token'`).Scan(&raw)
	if raw == "" || strings.Contains(raw, "ABCDEFGH") {
		t.Fatalf("token stored in the clear: %q", raw)
	}
	// Omitted fields are kept; an empty string clears.
	newChat := "@my_channel"
	if err := db.SetTelegram(ctx, TelegramUpdate{ChatID: &newChat}, t0); err != nil {
		t.Fatal(err)
	}
	cfg, _ = db.GetTelegram(ctx)
	if cfg.Token != tok || cfg.ChatID != "@my_channel" {
		t.Fatalf("%+v", cfg)
	}
	empty := ""
	if err := db.SetTelegram(ctx, TelegramUpdate{Token: &empty}, t0); err != nil {
		t.Fatal(err)
	}
	cfg, _ = db.GetTelegram(ctx)
	if cfg.Token != "" || cfg.Configured() || cfg.ChatID != "@my_channel" {
		t.Fatalf("%+v", cfg)
	}
	if has, _ := db.HasEncryptedRecords(ctx); has {
		t.Fatal("cleared token still counted as an encrypted record")
	}
	plain := openMigrated(t)
	if err := plain.SetTelegram(ctx, TelegramUpdate{Token: &tok}, t0); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("token stored without a key: %v", err)
	}
}

func TestListEventsFiltersAndCursor(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	for i := 0; i < 3; i++ {
		makeEvent(t, db, ids[i%2], t0.Add(time.Duration(i)*time.Hour))
	}
	all, next, err := db.ListEvents(ctx, EventFilter{Limit: 100})
	if err != nil || len(all) != 6 || next != nil {
		t.Fatalf("%d events, next=%v, %v", len(all), next, err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].CreatedAt.After(all[i-1].CreatedAt) || (all[i].CreatedAt.Equal(all[i-1].CreatedAt) && all[i].ID > all[i-1].ID) {
			t.Fatalf("not newest first: %v then %v", all[i-1].ID, all[i].ID)
		}
	}
	// Page through two at a time: no overlaps, no gaps.
	var seen []int64
	var cur *EventCursor
	for pages := 0; pages < 10; pages++ {
		page, next, err := db.ListEvents(ctx, EventFilter{Limit: 2, Before: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			seen = append(seen, e.ID)
		}
		if next == nil {
			break
		}
		cur = next
	}
	if len(seen) != 6 {
		t.Fatalf("paging saw %v", seen)
	}
	for i, e := range all {
		if seen[i] != e.ID {
			t.Fatalf("paging order differs: %v vs %d", seen, e.ID)
		}
	}
	onlyDown, _, _ := db.ListEvents(ctx, EventFilter{Kinds: []string{"proxy_down"}, Limit: 100})
	if len(onlyDown) != 3 {
		t.Fatalf("kind filter: %d", len(onlyDown))
	}
	forProxy, _, _ := db.ListEvents(ctx, EventFilter{ProxyID: &ids[1], Limit: 100})
	if len(forProxy) != 2 || forProxy[0].ProxyID == nil || *forProxy[0].ProxyID != int64(ids[1]) {
		t.Fatalf("proxy filter: %+v", forProxy)
	}
	if forProxy[0].ProxyName == "" {
		t.Fatal("proxy name missing")
	}
}
