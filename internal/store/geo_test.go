package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func TestGeoCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.GetGeoCache(ctx, "1.1.1.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("miss: %v", err)
	}
	asn := uint32(13335)
	in := domain.Geo{IP: "1.1.1.1", CountryCode: "AU", Country: "Australia", City: "Brisbane", Organization: "Cloudflare, Inc.", ASN: &asn, FetchedAt: t0}
	if err := db.PutGeoCache(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetGeoCache(ctx, "1.1.1.1")
	if err != nil || got.Country != "Australia" || got.City != "Brisbane" || got.CountryCode != "AU" ||
		got.Organization != "Cloudflare, Inc." || got.ASN == nil || *got.ASN != 13335 || !got.FetchedAt.Equal(t0) {
		t.Fatalf("%+v %v", got, err)
	}
	// Replace with a record without an ASN.
	if err := db.PutGeoCache(ctx, domain.Geo{IP: "1.1.1.1", Country: "Nowhere", FetchedAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetGeoCache(ctx, "1.1.1.1")
	if got.ASN != nil || got.Country != "Nowhere" || got.City != "" || !got.FetchedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", got)
	}
}

func TestMigrationDropsTheOldGeolocationKeyAndBudget(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	// Bring a database to the state before this change: migrations 1-4 only.
	if _, err := db.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migs[:4] {
		if err := db.applyMigration(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `INSERT INTO settings (key, value, secret, updated_at) VALUES ('geo.api_key', 'sealed', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM settings WHERE key = 'geo.api_key'`); n != 0 {
		t.Fatal("the old geolocation key survived the migration")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='geo_state'`); n != 0 {
		t.Fatal("the daily-budget table still exists")
	}
}
