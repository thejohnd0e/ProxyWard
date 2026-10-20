package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"proxy-checker/internal/domain"
)

func openMigrated(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func countRows(t *testing.T, db *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM schema_migrations`); n != len(migs) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(migs))
	}
}

func TestMigrateCreatesSpecTables(t *testing.T) {
	db := openMigrated(t)
	for _, table := range []string{
		"proxies", "settings", "admin", "sessions", "jobs", "availability_results",
		"speed_results", "geo_cache", "events", "notification_outbox", "schema_migrations", "incidents", "geo_state", "provider_pause", "proxy_state",
	} {
		if n := countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table); n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestFailedMigrationLeavesNoPartialSchema(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	bad := migration{
		version: 99,
		name:    "099_bad.sql",
		sql:     `CREATE TABLE half_done (id INTEGER); INSERT INTO missing_table VALUES (1);`,
	}
	if err := db.migrate(ctx, []migration{bad}); err == nil {
		t.Fatal("expected migration error")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name='half_done'`); n != 0 {
		t.Fatal("partial schema left behind")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=99`); n != 0 {
		t.Fatal("failed migration recorded as applied")
	}
}

func TestConnectionPragmas(t *testing.T) {
	db := openMigrated(t)
	var fk, timeout int
	var mode string
	if err := db.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	if err := db.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy_timeout=%d err=%v", timeout, err)
	}
	if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode=%q err=%v", mode, err)
	}
}

func TestForeignKeysAreEnforcedAndCascade(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.db.Exec(
		`INSERT INTO jobs (proxy_id, kind, status, source, due_at, created_at) VALUES (999, 'availability', 'pending', 'manual', 0, 0)`); err == nil {
		t.Fatal("job for a missing proxy was accepted")
	}
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "a", Protocol: domain.ProtocolHTTP, Host: "10.0.0.1", Port: 3128})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO jobs (proxy_id, kind, status, source, due_at, created_at) VALUES (?, 'availability', 'pending', 'manual', 0, 0)`, int64(id)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM proxies WHERE id = ?`, int64(id)); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM jobs`); n != 0 {
		t.Fatalf("jobs after proxy delete = %d, want 0", n)
	}
}

func TestOneActiveJobPerProxyAndKind(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "a", Protocol: domain.ProtocolSOCKS5, Host: "h", Port: 1080})
	if err != nil {
		t.Fatal(err)
	}
	const ins = `INSERT INTO jobs (proxy_id, kind, status, source, due_at, created_at) VALUES (?, ?, ?, 'manual', 0, 0)`
	if _, err := db.db.Exec(ins, int64(id), "availability", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(ins, int64(id), "availability", "running"); err == nil {
		t.Fatal("second active availability job accepted")
	}
	if _, err := db.db.Exec(ins, int64(id), "speed", "pending"); err != nil {
		t.Fatalf("speed job for the same proxy rejected: %v", err)
	}
	if _, err := db.db.Exec(ins, int64(id), "availability", "done"); err != nil {
		t.Fatalf("finished job rejected: %v", err)
	}
}

func TestInsertProxyValidationAndDuplicates(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	base := domain.Proxy{Name: "a", Protocol: domain.ProtocolHTTP, Host: "Example.com", Port: 8080}

	if _, err := db.InsertProxy(ctx, base); err != nil {
		t.Fatal(err)
	}
	dup := base
	dup.Host = "example.COM"
	if _, err := db.InsertProxy(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate err = %v, want ErrConflict", err)
	}
	other := base
	other.Protocol = domain.ProtocolSOCKS5
	if _, err := db.InsertProxy(ctx, other); err != nil {
		t.Fatalf("different protocol rejected: %v", err)
	}

	for name, p := range map[string]domain.Proxy{
		"bad protocol": {Protocol: "ftp", Host: "h", Port: 1},
		"empty host":   {Protocol: domain.ProtocolHTTP, Host: " ", Port: 1},
		"zero port":    {Protocol: domain.ProtocolHTTP, Host: "h", Port: 0},
	} {
		if _, err := db.InsertProxy(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	withAuth := base
	withAuth.Host = "other"
	withAuth.Auth = &domain.Credentials{Username: "u", Password: "p"}
	if _, err := db.InsertProxy(ctx, withAuth); !errors.Is(err, ErrAuthUnsupported) {
		t.Fatalf("auth err = %v, want ErrAuthUnsupported", err)
	}
}

func TestGetAndListProxies(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.GetProxy(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}
	id, err := db.InsertProxy(ctx, domain.Proxy{
		Name: "v6", Protocol: domain.ProtocolHTTPS, Host: "2001:db8::1", Port: 443, Tags: []string{"lab", "eu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProxy(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "2001:db8::1" || got.Port != 443 || len(got.Tags) != 2 || got.Tags[1] != "eu" || got.Auth != nil {
		t.Fatalf("unexpected proxy: %+v", got)
	}
	list, err := db.ListProxies(ctx)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v err = %v", list, err)
	}
}
