package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"proxy-checker/internal/domain"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestAdminPasswordHashLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.AdminPasswordHash(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unset admin err = %v", err)
	}
	if err := db.SetAdminPasswordHash(ctx, "h1", t0); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "d1", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAdminPasswordHash(ctx, "h2", t0); err != nil {
		t.Fatal(err)
	}
	if h, err := db.AdminPasswordHash(ctx); err != nil || h != "h2" {
		t.Fatalf("hash = %q err = %v", h, err)
	}
	if err := db.SessionActive(ctx, "d1", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived password change: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM admin`); n != 1 {
		t.Fatalf("admin rows = %d", n)
	}
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if err := db.CreateSession(ctx, "a", t0, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "b", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "a", t0, t0.Add(time.Hour)); err == nil {
		t.Fatal("duplicate session digest accepted")
	}
	if err := db.SessionActive(ctx, "a", t0.Add(23*time.Hour)); err != nil {
		t.Fatalf("active session rejected: %v", err)
	}
	if err := db.SessionActive(ctx, "a", t0.Add(24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session accepted: %v", err)
	}
	if err := db.DeleteExpiredSessions(ctx, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sessions`); n != 1 {
		t.Fatalf("sessions after purge = %d", n)
	}
	if err := db.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, "missing"); err != nil {
		t.Fatalf("deleting a missing session: %v", err)
	}
	if err := db.DeleteAllSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("sessions after revoke all = %d", n)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.GetSetting(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing setting err = %v", err)
	}
	if err := db.SetSetting(ctx, "k", Setting{Value: "v1"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, "k", Setting{Value: "v2", Secret: true}, t0); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetSetting(ctx, "k")
	if err != nil || got.Value != "v2" || !got.Secret {
		t.Fatalf("setting = %+v err = %v", got, err)
	}
	if err := db.DeleteSetting(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSetting(ctx, "k"); err != nil {
		t.Fatalf("deleting a missing setting: %v", err)
	}
}

func TestHasEncryptedRecords(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	has := func() bool {
		t.Helper()
		ok, err := db.HasEncryptedRecords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if has() {
		t.Fatal("empty database reports encrypted records")
	}
	if err := db.SetSetting(ctx, "plain", Setting{Value: "x"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProxy(ctx, domain.Proxy{Name: "n", Protocol: domain.ProtocolHTTP, Host: "h", Port: 1}); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("plain records reported as encrypted")
	}
	if err := db.SetSetting(ctx, "token", Setting{Value: "c", Secret: true}, t0); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("secret setting not detected")
	}
	if err := db.DeleteSetting(ctx, "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE proxies SET password_enc = x'0102'`); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("encrypted proxy password not detected")
	}
}
