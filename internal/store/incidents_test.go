package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxy-checker/internal/domain"
)

func avail(id domain.ProxyID, at time.Time, st domain.State, code string) domain.Availability {
	a := domain.Availability{ProxyID: id, CheckedAt: at, State: st, Endpoint: "ipify"}
	if code != "" {
		a.Failure = &domain.Failure{Code: code, Message: "m"}
	}
	return a
}

type counts struct{ incidents, open, events, outbox int }

func tally(t *testing.T, db *Store) counts {
	t.Helper()
	return counts{
		incidents: countRows(t, db, `SELECT COUNT(*) FROM incidents`),
		open:      countRows(t, db, `SELECT COUNT(*) FROM incidents WHERE ended_at IS NULL`),
		events:    countRows(t, db, `SELECT COUNT(*) FROM events`),
		outbox:    countRows(t, db, `SELECT COUNT(*) FROM notification_outbox`),
	}
}

func record(t *testing.T, db *Store, id domain.ProxyID, i int, st domain.State, code string) IncidentOutcome {
	t.Helper()
	o, err := db.RecordAvailabilityOutcome(context.Background(), avail(id, t0.Add(time.Duration(i)*time.Minute), st, code), 3)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestInitialUpSendsNothing(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	for i := 0; i < 3; i++ {
		if o := record(t, db, id, i, domain.StateUp, ""); o.Transition != "" {
			t.Fatalf("%+v", o)
		}
	}
	if c := tally(t, db); c != (counts{}) {
		t.Fatalf("%+v", c)
	}
}

func TestThreeDefiniteFailuresCreateOneDownEvent(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	if o := record(t, db, id, 0, domain.StateDown, "auth_failed"); o.Transition != "" || o.Streak != 1 {
		t.Fatalf("%+v", o)
	}
	// Indeterminate neither counts nor resets the streak.
	if o := record(t, db, id, 1, domain.StateIndeterminate, "timeout"); o.Transition != "" || o.Streak != 1 {
		t.Fatalf("%+v", o)
	}
	if o := record(t, db, id, 2, domain.StateDown, "auth_failed"); o.Transition != "" || o.Streak != 2 {
		t.Fatalf("%+v", o)
	}
	o := record(t, db, id, 3, domain.StateDown, "connection_refused")
	if o.Transition != TransitionDown || o.EventID == 0 || o.Streak != 3 {
		t.Fatalf("%+v", o)
	}
	if c := tally(t, db); c != (counts{incidents: 1, open: 1, events: 1, outbox: 1}) {
		t.Fatalf("%+v", c)
	}
	// Further failures and indeterminate results do not duplicate or close it.
	for i := 4; i < 8; i++ {
		st := domain.StateDown
		if i%2 == 0 {
			st = domain.StateIndeterminate
		}
		if o := record(t, db, id, i, st, "timeout"); o.Transition != "" {
			t.Fatalf("step %d: %+v", i, o)
		}
	}
	if c := tally(t, db); c != (counts{incidents: 1, open: 1, events: 1, outbox: 1}) {
		t.Fatalf("%+v", c)
	}
	var failures int
	if err := db.db.QueryRow(`SELECT consecutive_failures FROM incidents`).Scan(&failures); err != nil || failures != 5 {
		t.Fatalf("incident failures = %d %v", failures, err)
	}
}

func TestRecoveryOnlyAfterADownIncident(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	for i := 0; i < 3; i++ {
		record(t, db, id, i, domain.StateDown, "auth_failed")
	}
	if o := record(t, db, id, 3, domain.StateIndeterminate, "timeout"); o.Transition != "" {
		t.Fatalf("indeterminate recovered an incident: %+v", o)
	}
	o := record(t, db, id, 4, domain.StateUp, "")
	if o.Transition != TransitionRecovered || o.EventID == 0 {
		t.Fatalf("%+v", o)
	}
	if c := tally(t, db); c != (counts{incidents: 1, open: 0, events: 2, outbox: 2}) {
		t.Fatalf("%+v", c)
	}
	if o := record(t, db, id, 5, domain.StateUp, ""); o.Transition != "" {
		t.Fatalf("second up after recovery: %+v", o)
	}
	// The streak restarted: two failures are not enough, a third starts incident two.
	record(t, db, id, 6, domain.StateDown, "x")
	record(t, db, id, 7, domain.StateDown, "x")
	if o := record(t, db, id, 8, domain.StateDown, "x"); o.Transition != TransitionDown {
		t.Fatalf("%+v", o)
	}
	if c := tally(t, db); c != (counts{incidents: 2, open: 1, events: 3, outbox: 3}) {
		t.Fatalf("%+v", c)
	}
}

func TestAnUpResultBreaksAFailureStreak(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	record(t, db, id, 0, domain.StateDown, "x")
	record(t, db, id, 1, domain.StateDown, "x")
	record(t, db, id, 2, domain.StateUp, "")
	record(t, db, id, 3, domain.StateDown, "x")
	if o := record(t, db, id, 4, domain.StateDown, "x"); o.Transition != "" {
		t.Fatalf("a streak survived an up result: %+v", o)
	}
}

func TestCanceledAndInterruptedNeverCount(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	record(t, db, id, 0, domain.StateDown, "x")
	record(t, db, id, 1, domain.StateDown, "x")
	for i, code := range []string{"canceled", "interrupted", "canceled"} {
		if o := record(t, db, id, 2+i, domain.StateDown, code); o.Transition != "" || o.Streak != 2 {
			t.Fatalf("%s counted: %+v", code, o)
		}
	}
	if o := record(t, db, id, 9, domain.StateUnknown, ""); o.Transition != "" || o.Streak != 2 {
		t.Fatalf("unknown state counted: %+v", o)
	}
	if o := record(t, db, id, 10, domain.StateDown, "x"); o.Transition != TransitionDown {
		t.Fatalf("the third real failure must open the incident: %+v", o)
	}
	// A canceled "up" does not recover either.
	if o := record(t, db, id, 11, domain.StateUp, "canceled"); o.Transition != "" {
		t.Fatalf("%+v", o)
	}
}

func TestThresholdIsConfigurable(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	o, err := db.RecordAvailabilityOutcome(ctx, avail(ids[0], t0, domain.StateDown, "x"), 1)
	if err != nil || o.Transition != TransitionDown {
		t.Fatalf("threshold 1: %+v %v", o, err)
	}
	for i := 0; i < 9; i++ {
		o, _ := db.RecordAvailabilityOutcome(ctx, avail(ids[1], t0.Add(time.Duration(i)*time.Minute), domain.StateDown, "x"), 10)
		if o.Transition != "" {
			t.Fatalf("threshold 10 fired at %d", i+1)
		}
	}
	if o, _ := db.RecordAvailabilityOutcome(ctx, avail(ids[1], t0.Add(time.Hour), domain.StateDown, "x"), 10); o.Transition != TransitionDown {
		t.Fatalf("%+v", o)
	}
	for _, bad := range []int{0, -1, 11} {
		if _, err := db.RecordAvailabilityOutcome(ctx, avail(ids[0], t0, domain.StateDown, "x"), bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("threshold %d: %v", bad, err)
		}
	}
}

func TestIncidentStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	open := func() *Store {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	id := addProxies(t, db, 1)[0]
	record(t, db, id, 0, domain.StateDown, "x")
	record(t, db, id, 1, domain.StateDown, "x")
	db.Close()

	db = open()
	if o := record(t, db, id, 2, domain.StateDown, "x"); o.Transition != TransitionDown {
		t.Fatalf("streak lost across a restart: %+v", o)
	}
	db.Close()

	db = open()
	defer db.Close()
	if o := record(t, db, id, 3, domain.StateDown, "x"); o.Transition != "" {
		t.Fatalf("duplicate event after restart: %+v", o)
	}
	if c := tally(t, db); c != (counts{incidents: 1, open: 1, events: 1, outbox: 1}) {
		t.Fatalf("%+v", c)
	}
	// The database itself refuses a second open incident or a second outbox row.
	if _, err := db.db.Exec(`INSERT INTO incidents (proxy_id, started_at) VALUES (?, 1)`, int64(id)); err == nil {
		t.Fatal("two open incidents accepted")
	}
	var eventID int64
	_ = db.db.QueryRow(`SELECT id FROM events`).Scan(&eventID)
	if _, err := db.db.Exec(`INSERT INTO notification_outbox (event_id, payload, next_attempt_at, created_at) VALUES (?, '{}', 0, 0)`, eventID); err == nil {
		t.Fatal("two outbox rows for one event accepted")
	}
}

func TestEventAndOutboxContent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, _ := db.InsertProxy(ctx, domain.Proxy{Name: "Office <b>proxy</b>", Protocol: domain.ProtocolSOCKS5, Host: "gw.lan", Port: 1080})
	for i := 0; i < 3; i++ {
		record(t, db, id, i, domain.StateDown, "connection_refused")
	}
	var kind, msg string
	var evProxy int64
	var createdAt int64
	if err := db.db.QueryRow(`SELECT kind, message, proxy_id, created_at FROM events`).Scan(&kind, &msg, &evProxy, &createdAt); err != nil {
		t.Fatal(err)
	}
	if kind != "proxy_down" || evProxy != int64(id) || !strings.Contains(msg, "Office <b>proxy</b>") ||
		!strings.Contains(msg, "3") || !strings.Contains(msg, "connection_refused") || createdAt != t0.Add(2*time.Minute).UnixMilli() {
		t.Fatalf("event: %s %q %d", kind, msg, evProxy)
	}
	var payload string
	var attempts int
	var next int64
	if err := db.db.QueryRow(`SELECT payload, attempts, next_attempt_at FROM notification_outbox`).Scan(&payload, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Kind    string `json:"kind"`
		EventID int64  `json:"event_id"`
		ProxyID int64  `json:"proxy_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil || p.Kind != "proxy_down" || p.ProxyID != int64(id) || p.EventID == 0 {
		t.Fatalf("payload %q %v", payload, err)
	}
	if attempts != 0 || next != t0.Add(2*time.Minute+10*time.Second).UnixMilli() {
		t.Fatalf("attempts=%d next=%d: delivery must wait for the 10 s grouping window", attempts, next)
	}
	// Deleting the proxy keeps the history of what happened.
	if err := db.DeleteProxy(ctx, id); err != nil {
		t.Fatal(err)
	}
	if c := tally(t, db); c.events != 1 || c.outbox != 1 || c.incidents != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestOutcomeForMissingProxy(t *testing.T) {
	db := openMigrated(t)
	if _, err := db.RecordAvailabilityOutcome(context.Background(), avail(77, t0, domain.StateDown, "x"), 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
