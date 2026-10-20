package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"proxy-checker/internal/config"
	"proxy-checker/internal/domain"
	"proxy-checker/internal/secrets"
	"proxy-checker/internal/store"
)

// seed creates a data directory with an encrypted proxy password and returns its config.
func seed(t *testing.T) config.Config {
	t.Helper()
	ctx := context.Background()
	cfg := config.Config{ListenAddr: "127.0.0.1:8080", DataDir: filepath.Join(t.TempDir(), "data")}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := secrets.Load(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	db.UseSecrets(c)
	if _, err := db.InsertProxy(ctx, domain.Proxy{Name: "kept", Protocol: domain.ProtocolHTTP, Host: "kept.lan", Port: 3128,
		Auth: &domain.Credentials{Username: "u", Password: "Restored-Secret-1"}}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBackupAndRestoreRoundTripIncludingTheKey(t *testing.T) {
	ctx := context.Background()
	src := seed(t)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := CreateBackup(ctx, src, archive); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(archive); info.Mode().Perm() != 0o600 {
			t.Fatalf("archive mode %v, want 0600", info.Mode().Perm())
		}
	}

	// The archive holds exactly the snapshot and the key, the key with 0600.
	names := map[string]os.FileMode{}
	f, _ := os.Open(archive)
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = os.FileMode(h.Mode)
	}
	f.Close()
	if len(names) != 2 || names["state.db"] == 0 || names["secrets.key"].Perm() != 0o600 {
		t.Fatalf("archive entries: %v", names)
	}

	// Restore into a fresh directory and read the secret back with the restored key.
	dst := config.Config{ListenAddr: src.ListenAddr, DataDir: filepath.Join(t.TempDir(), "restored")}
	if err := RestoreBackup(ctx, dst, archive, false); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dst.DataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.Load(dst.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	db.UseSecrets(cipher)
	list, err := db.ListProxies(ctx)
	if err != nil || len(list) != 1 || list[0].Auth == nil || list[0].Auth.Password != "Restored-Secret-1" {
		t.Fatalf("restored data: %+v %v", list, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(secrets.KeyPath(dst.DataDir)); info.Mode().Perm() != 0o600 {
			t.Fatalf("restored key mode %v", info.Mode().Perm())
		}
	}
	// The restored server accepts the key: the startup guard finds it present.
	if _, err := loadCipher(ctx, dst.DataDir, db); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRefusesExistingOutputAndMissingData(t *testing.T) {
	ctx := context.Background()
	cfg := seed(t)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := os.WriteFile(out, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreateBackup(ctx, cfg, out); err == nil {
		t.Fatal("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(out); string(b) != "precious" {
		t.Fatal("existing file was changed")
	}
	empty := config.Config{ListenAddr: cfg.ListenAddr, DataDir: t.TempDir()}
	if err := CreateBackup(ctx, empty, filepath.Join(t.TempDir(), "x.tar.gz")); err == nil {
		t.Fatal("backup of a missing database succeeded")
	}
	if _, err := os.Stat(filepath.Join(empty.DataDir, "state.db")); err == nil {
		t.Fatal("a backup attempt created a database")
	}
}

func TestRestoreRefusesToOverwriteWithoutForce(t *testing.T) {
	ctx := context.Background()
	src := seed(t)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := CreateBackup(ctx, src, archive); err != nil {
		t.Fatal(err)
	}
	live := seed(t) // another, different live installation
	before, _ := os.ReadFile(filepath.Join(live.DataDir, "state.db"))
	keyBefore, _ := os.ReadFile(secrets.KeyPath(live.DataDir))
	if err := RestoreBackup(ctx, live, archive, false); err == nil {
		t.Fatal("restore overwrote an existing database without --force")
	}
	after, _ := os.ReadFile(filepath.Join(live.DataDir, "state.db"))
	keyAfter, _ := os.ReadFile(secrets.KeyPath(live.DataDir))
	if string(before) != string(after) || string(keyBefore) != string(keyAfter) {
		t.Fatal("a refused restore changed the data directory")
	}
	if err := RestoreBackup(ctx, live, archive, true); err != nil {
		t.Fatalf("forced restore: %v", err)
	}
	if k, _ := os.ReadFile(secrets.KeyPath(live.DataDir)); string(k) == string(keyBefore) {
		t.Fatal("the key was not replaced by the backed-up key")
	}
}

// writeArchive builds a tar.gz with the given entries.
func craftArchive(t *testing.T, entries []tar.Header, bodies [][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		h.Size = int64(len(bodies[i]))
		if h.Mode == 0 {
			h.Mode = 0o600
		}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(bodies[i])
	}
	tw.Close()
	gz.Close()
	f.Close()
	return path
}

func TestRestoreRejectsMaliciousOrBrokenArchives(t *testing.T) {
	ctx := context.Background()
	good := seed(t)
	goodArchive := filepath.Join(t.TempDir(), "good.tar.gz")
	if err := CreateBackup(ctx, good, goodArchive); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	dbBytes, _ := os.ReadFile(filepath.Join(good.DataDir, "state.db"))

	for name, archive := range map[string]string{
		"path traversal": craftArchive(t, []tar.Header{{Name: "../evil"}, {Name: "secrets.key"}}, [][]byte{[]byte("x"), key}),
		"absolute path":  craftArchive(t, []tar.Header{{Name: "/etc/passwd"}, {Name: "secrets.key"}}, [][]byte{[]byte("x"), key}),
		"extra entry":    craftArchive(t, []tar.Header{{Name: "state.db"}, {Name: "secrets.key"}, {Name: "run.sh"}}, [][]byte{dbBytes, key, []byte("#!/bin/sh")}),
		"missing key":    craftArchive(t, []tar.Header{{Name: "state.db"}}, [][]byte{dbBytes}),
		"short key":      craftArchive(t, []tar.Header{{Name: "state.db"}, {Name: "secrets.key"}}, [][]byte{dbBytes, []byte("short")}),
		"symlink":        craftArchive(t, []tar.Header{{Name: "state.db", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, {Name: "secrets.key"}}, [][]byte{nil, key}),
		"not a database": craftArchive(t, []tar.Header{{Name: "state.db"}, {Name: "secrets.key"}}, [][]byte{[]byte(strings.Repeat("garbage", 1000)), key}),
		"duplicate":      craftArchive(t, []tar.Header{{Name: "state.db"}, {Name: "state.db"}, {Name: "secrets.key"}}, [][]byte{dbBytes, dbBytes, key}),
	} {
		dst := config.Config{ListenAddr: "127.0.0.1:8080", DataDir: filepath.Join(t.TempDir(), "d")}
		if err := RestoreBackup(ctx, dst, archive, false); err == nil {
			t.Errorf("%s: accepted", name)
		}
		for _, f := range []string{"state.db", "secrets.key"} {
			if _, err := os.Stat(filepath.Join(dst.DataDir, f)); err == nil {
				t.Errorf("%s: left %s behind", name, f)
			}
		}
	}
	if err := RestoreBackup(ctx, config.Config{ListenAddr: "127.0.0.1:8080", DataDir: t.TempDir()}, filepath.Join(t.TempDir(), "missing.tar.gz"), false); err == nil {
		t.Error("a missing archive was accepted")
	}
}

func TestHealthCheck(t *testing.T) {
	ctx := context.Background()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ok.Close()
	cfg := config.Config{ListenAddr: strings.TrimPrefix(ok.URL, "http://"), DataDir: "x"}
	if err := HealthCheck(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	if err := HealthCheck(ctx, config.Config{ListenAddr: strings.TrimPrefix(bad.URL, "http://"), DataDir: "x"}); err == nil {
		t.Fatal("a 503 is not healthy")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	if err := HealthCheck(ctx, config.Config{ListenAddr: addr, DataDir: "x"}); err == nil {
		t.Fatal("a closed port is not healthy")
	}
	// A wildcard listen address is probed on loopback.
	_, port, _ := net.SplitHostPort(ok.Listener.Addr().String())
	if err := HealthCheck(ctx, config.Config{ListenAddr: "0.0.0.0:" + port, DataDir: "x"}); err != nil {
		t.Fatalf("wildcard address: %v", err)
	}
}

func TestRetentionPrunesAccordingToTheSettings(t *testing.T) {
	ctx := context.Background()
	cfg := seed(t)
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list, _ := db.ListSchedules(ctx)
	id := list[0].ProxyID
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for i := 19; i >= 0; i-- { // chronological order, oldest first, as the checks happen
		_ = db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: now.AddDate(0, 0, -i*5), State: domain.StateUp})
	}
	st := domain.DefaultSettings()
	st.RetentionDays = 30
	if err := db.PutSettings(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	n, err := pruneHistory(ctx, db, now, 4) // small batches: many rounds
	if err != nil {
		t.Fatal(err)
	}
	got, _ := db.ListAvailability(ctx, id, time.Time{}, time.Time{}, 100)
	// Results from the last 30 days (days 0,5,...,30 ago) plus nothing else.
	if len(got) != 7 || n != 13 {
		t.Fatalf("%d results kept, %d pruned", len(got), n)
	}
	if n2, _ := pruneHistory(ctx, db, now, 4); n2 != 0 {
		t.Fatalf("second run pruned %d", n2)
	}
}

func TestRetentionStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := seed(t)
	db, _ := store.Open(context.Background(), filepath.Join(cfg.DataDir, "state.db"))
	defer db.Close()
	done := make(chan struct{})
	go func() { runRetention(ctx, db, domain.SystemClock{}, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("retention loop ignored cancellation")
	}
}

func TestRestoreFromStandardInput(t *testing.T) {
	ctx := context.Background()
	src := seed(t)
	archive := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := CreateBackup(ctx, src, archive); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dst := config.Config{ListenAddr: src.ListenAddr, DataDir: filepath.Join(t.TempDir(), "d")}
	if err := RestoreBackupFrom(ctx, dst, f, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst.DataDir, "state.db")); err != nil {
		t.Fatal(err)
	}
	// "-" selects standard input.
	r, w, _ := os.Pipe()
	data, _ := os.ReadFile(archive)
	go func() { _, _ = w.Write(data); w.Close() }()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	dst2 := config.Config{ListenAddr: src.ListenAddr, DataDir: filepath.Join(t.TempDir(), "d2")}
	if err := RestoreBackup(ctx, dst2, "-", false); err != nil {
		t.Fatalf("restore from stdin: %v", err)
	}
	if _, err := os.Stat(secrets.KeyPath(dst2.DataDir)); err != nil {
		t.Fatal(err)
	}
}
