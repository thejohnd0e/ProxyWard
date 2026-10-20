package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"proxy-checker/internal/domain"
)

func TestGeoKeyIsEncryptedAndReplaceable(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	if _, ok, err := db.GeoKey(ctx); err != nil || ok {
		t.Fatalf("empty: %v %v", ok, err)
	}
	if err := db.SetGeoKey(ctx, "  my-secret-key  ", t0); err != nil {
		t.Fatal(err)
	}
	key, ok, err := db.GeoKey(ctx)
	if err != nil || !ok || key != "my-secret-key" {
		t.Fatalf("key = %q %v %v", key, ok, err)
	}
	var raw string
	var secret int
	if err := db.db.QueryRow(`SELECT value, secret FROM settings WHERE key = 'geo.api_key'`).Scan(&raw, &secret); err != nil {
		t.Fatal(err)
	}
	if secret != 1 || raw == "" || raw == "my-secret-key" || contains(raw, "my-secret-key") {
		t.Fatalf("key stored in the clear: %q (secret=%d)", raw, secret)
	}
	if has, _ := db.HasEncryptedRecords(ctx); !has {
		t.Fatal("encrypted key not reported by HasEncryptedRecords")
	}
	if err := db.SetGeoKey(ctx, "", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GeoKey(ctx); ok {
		t.Fatal("key still present after removal")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestGeoKeyNeedsSecrets(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if err := db.SetGeoKey(ctx, "k", t0); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("set without secrets: %v", err)
	}
	if _, _, err := db.GeoKey(ctx); err != nil {
		t.Fatalf("reading a missing key must not fail: %v", err)
	}
}

func TestGeoKeyFingerprintTracksKey(t *testing.T) {
	db, _ := openWithSecrets(t)
	a, b := db.GeoKeyFingerprint("one"), db.GeoKeyFingerprint("two")
	if a == "" || a == b || a != db.GeoKeyFingerprint("one") {
		t.Fatalf("%q %q", a, b)
	}
	if db.GeoKeyFingerprint("") != "" {
		t.Fatal("empty key must have an empty fingerprint")
	}
}

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

func TestGeoBudgetIsPersistentAndResetsAtUTCMidnight(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
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
	now := time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)
	db := open()
	for i := 1; i <= 3; i++ {
		g, err := db.GeoBeginAttempt(ctx, now, "fp", 3)
		if err != nil || !g.Allowed || g.Used != i {
			t.Fatalf("attempt %d: %+v %v", i, g, err)
		}
	}
	g, _ := db.GeoBeginAttempt(ctx, now, "fp", 3)
	if g.Allowed || g.Reason != GateQuota || !g.RetryAt.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("over budget: %+v", g)
	}
	db.Close()

	db = open() // restart: usage is remembered
	defer db.Close()
	if g, _ := db.GeoBeginAttempt(ctx, now.Add(30*time.Minute), "fp", 3); g.Allowed {
		t.Fatalf("budget forgotten after restart: %+v", g)
	}
	g, _ = db.GeoBeginAttempt(ctx, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), "fp", 3)
	if !g.Allowed || g.Used != 1 {
		t.Fatalf("budget did not reset at UTC midnight: %+v", g)
	}
	// A non-UTC clock reading is converted: 2026-10-21 01:30 +02:00 is still 2026-10-20 23:30 UTC... of the previous day.
	loc := time.FixedZone("x", 2*3600)
	st, _ := db.GeoState(ctx)
	if st.BudgetDay != "2026-10-21" || st.Used != 1 {
		t.Fatalf("state %+v", st)
	}
	g, _ = db.GeoBeginAttempt(ctx, time.Date(2026, 10, 21, 1, 30, 0, 0, loc), "fp", 3)
	if !g.Allowed || g.Used != 1 {
		t.Fatalf("local time not converted to UTC day: %+v", g)
	}
}

func TestGeoPauseLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	if err := db.GeoSetPause(ctx, now, GateRateLimited, now.Add(5*time.Minute), "fp1"); err != nil {
		t.Fatal(err)
	}
	g, _ := db.GeoBeginAttempt(ctx, now.Add(time.Minute), "fp1", 1000)
	if g.Allowed || g.Reason != GateRateLimited || !g.RetryAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("during pause: %+v", g)
	}
	if st, _ := db.GeoState(ctx); st.Used != 0 {
		t.Fatalf("a denied attempt consumed budget: %+v", st)
	}
	g, _ = db.GeoBeginAttempt(ctx, now.Add(5*time.Minute), "fp1", 1000)
	if !g.Allowed {
		t.Fatalf("pause did not expire: %+v", g)
	}
	if st, _ := db.GeoState(ctx); st.BackoffAttempt != 1 {
		t.Fatalf("backoff attempt = %d", st.BackoffAttempt)
	}
	if err := db.GeoSetPause(ctx, now, GateUnavailable, now.Add(time.Minute), "fp1"); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.GeoState(ctx); st.BackoffAttempt != 2 {
		t.Fatalf("backoff attempt = %d, want 2", st.BackoffAttempt)
	}
	if err := db.GeoClearPause(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := db.GeoState(ctx)
	if st.BackoffAttempt != 0 || st.PauseReason != "" || !st.PauseUntil.IsZero() {
		t.Fatalf("after clear: %+v", st)
	}
}

func TestForbiddenPauseEndsOnlyWhenTheKeyChanges(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		_, _ = db.GeoBeginAttempt(ctx, now, "old", 1000)
	}
	if err := db.GeoSetPause(ctx, now, GateForbidden, time.Time{}, "old"); err != nil {
		t.Fatal(err)
	}
	for _, later := range []time.Duration{time.Minute, 9 * time.Hour} { // same UTC day, so usage is comparable
		g, _ := db.GeoBeginAttempt(ctx, now.Add(later), "old", 1000)
		if g.Allowed || g.Reason != GateForbidden {
			t.Fatalf("forbidden pause lifted by time (%v): %+v", later, g)
		}
	}
	g, _ := db.GeoBeginAttempt(ctx, now.Add(10*time.Hour), "new", 1000)
	if !g.Allowed {
		t.Fatalf("pause not reset by a changed key: %+v", g)
	}
	st, _ := db.GeoState(ctx)
	if st.PauseReason != "" || st.Used != 3 {
		t.Fatalf("usage must survive a key change: %+v", st)
	}
}
