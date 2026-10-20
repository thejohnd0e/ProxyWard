package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"proxy-checker/internal/config"
	"proxy-checker/internal/secrets"
	"proxy-checker/internal/store"
)

func TestHealthzReflectsDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, cipher)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy status = %d", rec.Code)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed-database status = %d, want 503", rec.Code)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestRunServesAfterMigrationAndStopsOnCancel(t *testing.T) {
	cfg := config.Config{ListenAddr: freeAddr(t), DataDir: filepath.Join(t.TempDir(), "data")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()

	url := "http://" + cfg.ListenAddr + "/healthz"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("healthz status = %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunDoesNotServeWhenDataDirIsUnusable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := writeFile(file); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ListenAddr: freeAddr(t), DataDir: filepath.Join(file, "sub")}
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("expected an error")
	}
}
