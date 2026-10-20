package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/config"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLoadCipherCreatesKeyOnlyWithoutEncryptedRecords(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openStore(t, dir)
	if _, err := loadCipher(ctx, dir, db); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if ok, _ := secrets.Exists(dir); !ok {
		t.Fatal("key was not created")
	}
}

func TestLoadCipherRefusesToRegenerateMissingKey(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openStore(t, dir)
	if err := db.SetSetting(ctx, "telegram_token", store.Setting{Value: "ciphertext", Secret: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCipher(ctx, dir, db); err == nil {
		t.Fatal("a new key was generated over existing encrypted records")
	}
	if ok, _ := secrets.Exists(dir); ok {
		t.Fatal("key file was created despite the refusal")
	}
	// With the key present the same database loads fine.
	if err := os.WriteFile(secrets.KeyPath(dir), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCipher(ctx, dir, db); err != nil {
		t.Fatalf("with key: %v", err)
	}
}

func TestSetAdminPasswordStoresVerifiableHashAndRejectsShort(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{ListenAddr: "127.0.0.1:8080", DataDir: filepath.Join(t.TempDir(), "data")}
	if err := SetAdminPassword(ctx, cfg, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := SetAdminPassword(ctx, cfg, "a sufficiently long one"); err != nil {
		t.Fatal(err)
	}
	db := openStore(t, cfg.DataDir)
	hash, err := db.AdminPasswordHash(ctx)
	if err != nil || !auth.Verify(hash, "a sufficiently long one") {
		t.Fatalf("stored hash does not verify: %v", err)
	}
	// Reset revokes sessions.
	if err := db.CreateSession(ctx, "d", time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := SetAdminPassword(ctx, cfg, "yet another long one"); err != nil {
		t.Fatal(err)
	}
	if err := db.SessionActive(ctx, "d", time.Now()); err == nil {
		t.Fatal("session survived the reset")
	}
}

func TestSetAdminPasswordCreatesTheKeyButNeverReplacesAMissingOne(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{ListenAddr: "127.0.0.1:8080", DataDir: filepath.Join(t.TempDir(), "data")}
	if err := SetAdminPassword(ctx, cfg, "a sufficiently long one"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := secrets.Exists(cfg.DataDir); !ok {
		t.Fatal("the key was not created, so a fresh install cannot be backed up")
	}
	// With encrypted records present and the key gone, resetting the password must not invent a new key.
	db := openStore(t, cfg.DataDir)
	if err := db.SetSetting(ctx, "telegram.bot_token", store.Setting{Value: "ciphertext", Secret: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secrets.KeyPath(cfg.DataDir)); err != nil {
		t.Fatal(err)
	}
	if err := SetAdminPassword(ctx, cfg, "another long password"); err == nil {
		t.Fatal("a new key was created over existing encrypted records")
	}
	if ok, _ := secrets.Exists(cfg.DataDir); ok {
		t.Fatal("a key file appeared")
	}
}
