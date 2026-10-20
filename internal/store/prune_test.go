package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/secrets"
)

func TestPruneDeletesOldHistoryButKeepsTheCurrentState(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	cutoff := t0.Add(30 * 24 * time.Hour)

	// Proxy 0: three old results, the newest of them is still the "current state".
	for i := 0; i < 3; i++ {
		_ = db.AddAvailability(ctx, domain.Availability{ProxyID: ids[0], CheckedAt: t0.Add(time.Duration(i) * time.Hour), State: domain.StateUp})
		_ = db.AddSpeed(ctx, domain.Speed{ProxyID: ids[0], CheckedAt: t0.Add(time.Duration(i) * time.Hour), Provider: "p", Method: "m"})
	}
	// Proxy 1: one old and one recent result.
	_ = db.AddAvailability(ctx, domain.Availability{ProxyID: ids[1], CheckedAt: t0, State: domain.StateDown})
	_ = db.AddAvailability(ctx, domain.Availability{ProxyID: ids[1], CheckedAt: cutoff.Add(time.Hour), State: domain.StateUp})

	n, err := db.Prune(ctx, cutoff, 1000)
	if err != nil || n == 0 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM availability_results WHERE proxy_id = ?`, int64(ids[0])); c != 1 {
		t.Fatalf("proxy 0 keeps only its latest result, has %d", c)
	}
	latest, err := db.LatestAvailability(ctx, ids[0])
	if err != nil || !latest.CheckedAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("the current state was pruned: %+v %v", latest, err)
	}
	if _, err := db.LatestSpeed(ctx, ids[0]); err != nil {
		t.Fatalf("the latest speed result was pruned: %v", err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM speed_results`); c != 1 {
		t.Fatalf("speed rows = %d", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM availability_results WHERE proxy_id = ?`, int64(ids[1])); c != 1 {
		t.Fatalf("proxy 1 keeps its recent result only, has %d", c)
	}
	// Nothing recent was touched, and a second run finds nothing to do.
	if n, _ := db.Prune(ctx, cutoff, 1000); n != 0 {
		t.Fatalf("second prune removed %d", n)
	}
}

func TestPruneKeepsIncidentTracking(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	// A closed incident from long ago and an incident that is still open.
	for i := 0; i < 3; i++ {
		record(t, db, id, i, domain.StateDown, "x")
	}
	record(t, db, id, 4, domain.StateUp, "") // closes it
	for i := 0; i < 3; i++ {
		record(t, db, id, 10+i, domain.StateDown, "x") // opens a second one
	}
	cutoff := t0.Add(24 * time.Hour)
	if _, err := db.db.Exec(`UPDATE incidents SET ended_at = ? WHERE ended_at IS NOT NULL`, t0.Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Prune(ctx, cutoff, 1000); err != nil {
		t.Fatal(err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM incidents WHERE ended_at IS NULL`); c != 1 {
		t.Fatalf("the open incident must survive: %d", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM incidents WHERE ended_at IS NOT NULL`); c != 0 {
		t.Fatalf("old closed incidents should be pruned: %d", c)
	}
	var streak int
	if err := db.db.QueryRow(`SELECT failure_streak FROM proxy_state WHERE proxy_id = ?`, int64(id)).Scan(&streak); err != nil || streak != 3 {
		t.Fatalf("failure streak lost: %d %v", streak, err)
	}
	// The incident logic still works after pruning: a recovery closes the open one exactly once.
	if o := record(t, db, id, 40, domain.StateUp, ""); o.Transition != TransitionRecovered {
		t.Fatalf("%+v", o)
	}
}

func TestPruneKeepsUndeliveredNotifications(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	makeEvent(t, db, id, t0) // a down and a recovered event with outbox rows
	rows, _ := db.DueOutbox(ctx, t0.Add(time.Hour), 10)
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	_ = db.MarkOutboxDelivered(ctx, []int64{rows[0].ID}, t0)
	// rows[1] stays undelivered and still has attempts left.
	if _, err := db.Prune(ctx, t0.Add(100*24*time.Hour), 1000); err != nil {
		t.Fatal(err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM notification_outbox`); c != 1 {
		t.Fatalf("outbox rows = %d: only the delivered row may go", c)
	}
	if left, _ := db.DueOutbox(ctx, t0.Add(100*24*time.Hour), 10); len(left) != 1 || left[0].ID != rows[1].ID {
		t.Fatalf("a pending notification was pruned: %+v", left)
	}
}

func TestPruneIsBounded(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	for i := 0; i < 50; i++ {
		_ = db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Duration(i) * time.Minute), State: domain.StateUp})
	}
	total := 0
	for rounds := 0; rounds < 100; rounds++ {
		n, err := db.Prune(ctx, t0.Add(24*time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		if n > 10*8 { // at most one batch per table
			t.Fatalf("a round removed %d rows", n)
		}
		if n == 0 {
			break
		}
		total += n
	}
	if total != 49 {
		t.Fatalf("pruned %d rows in total, want 49 (all but the newest)", total)
	}
	if _, err := db.Prune(ctx, t0, 0); err == nil {
		t.Fatal("a zero batch size must be rejected")
	}
}

func TestPruneDropsExpiredSessionsAndStaleGeo(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	_ = db.CreateSession(ctx, "old", t0, t0.Add(time.Hour))
	_ = db.CreateSession(ctx, "live", t0, t0.Add(1000*time.Hour))
	_ = db.PutGeoCache(ctx, domain.Geo{IP: "1.1.1.1", FetchedAt: t0})
	_ = db.PutGeoCache(ctx, domain.Geo{IP: "8.8.8.8", FetchedAt: t0.Add(500 * time.Hour)})
	if _, err := db.Prune(ctx, t0.Add(100*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM sessions`); c != 1 {
		t.Fatalf("sessions = %d", c)
	}
	if c := countRows(t, db, `SELECT COUNT(*) FROM geo_cache`); c != 1 {
		t.Fatalf("geo rows = %d", c)
	}
}

func TestBackupIsAConsistentSnapshotEvenWhileWriting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	db.UseSecrets(cipher)
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h.lan", Port: 1,
		Auth: &domain.Credentials{Username: "u", Password: "Backed-Up-Secret"}})
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // keep writing while the backup runs
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Duration(i) * time.Second), State: domain.StateUp})
		}
	}()
	dest := filepath.Join(t.TempDir(), "snapshot.db")
	err = db.Backup(ctx, dest)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	// Open the backup on its own: it must be intact and carry the encrypted secret.
	snap, err := Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var result string
	if err := snap.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		t.Fatalf("integrity_check = %q %v", result, err)
	}
	snap.UseSecrets(cipher) // the key that is backed up alongside the database
	p, err := snap.GetProxy(ctx, id)
	if err != nil || p.Auth == nil || p.Auth.Password != "Backed-Up-Secret" {
		t.Fatalf("the backup cannot be decrypted with the key: %+v %v", p.Auth, err)
	}
	if err := snap.Migrate(ctx); err != nil {
		t.Fatalf("a restored database must be ready for the next start: %v", err)
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	dest := filepath.Join(t.TempDir(), "x.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, dest); err == nil {
		t.Fatal("an existing backup file was overwritten")
	}
}
