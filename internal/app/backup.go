package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/config"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

const (
	dbFileName  = "state.db"
	keyFileName = secrets.KeyFileName
	keySize     = 32
	maxDBBytes  = 8 << 30 // sanity bound for a restored database
)

// CreateBackup writes a tar.gz with a consistent snapshot of the database and
// the secrets key (mode 0600). It is safe while the server runs. Both files
// are needed together: the key decrypts the secrets stored in the database.
func CreateBackup(ctx context.Context, cfg config.Config, output string) error {
	dbPath := filepath.Join(cfg.DataDir, dbFileName)
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no database in %s: %w", cfg.DataDir, err)
	}
	key, err := os.ReadFile(secrets.KeyPath(cfg.DataDir))
	if err != nil || len(key) != keySize {
		return fmt.Errorf("the secrets key %s is missing or invalid; a backup without it could not restore stored passwords", secrets.KeyPath(cfg.DataDir))
	}
	if _, err := os.Stat(output); err == nil {
		return fmt.Errorf("%s already exists", output)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(output), ".backup-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	snapshot := filepath.Join(tmp, dbFileName)

	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	err = db.Backup(ctx, snapshot)
	db.Close()
	if err != nil {
		return err
	}

	out, err := os.CreateTemp(filepath.Dir(output), ".backup-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer os.Remove(out.Name())
	if err := out.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		out.Close()
		return err
	}
	if err := writeArchive(out, snapshot, key); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), output)
}

func writeArchive(w io.Writer, snapshot string, key []byte) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()

	f, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: dbFileName, Mode: 0o600, Size: info.Size(), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: keyFileName, Mode: 0o600, Size: int64(len(key)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(key); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// RestoreBackup restores an archive made by CreateBackup into the data
// directory. The server must be stopped. The archive is validated before
// anything in the data directory changes: exactly the two expected regular
// files, a key of the right size and a database that passes SQLite's integrity
// check. An existing database is only replaced with force.
//
// input "-" reads the archive from standard input, which avoids file
// permission problems when restoring inside a container:
//
//	docker compose run --rm -T proxyward restore --input - < backup.tar.gz
func RestoreBackup(ctx context.Context, cfg config.Config, input string, force bool) error {
	if input == "-" {
		return RestoreBackupFrom(ctx, cfg, os.Stdin, force)
	}
	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	return RestoreBackupFrom(ctx, cfg, f, force)
}

// RestoreBackupFrom is RestoreBackup reading the archive from r.
func RestoreBackupFrom(ctx context.Context, cfg config.Config, r io.Reader, force bool) error {
	dbPath := filepath.Join(cfg.DataDir, dbFileName)
	if _, err := os.Stat(dbPath); err == nil && !force {
		return fmt.Errorf("%s already exists; stop the server and use --force to replace it", dbPath)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	tmp, err := os.MkdirTemp(cfg.DataDir, ".restore-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := extractArchive(r, tmp); err != nil {
		return err
	}
	if err := checkDatabase(ctx, filepath.Join(tmp, dbFileName)); err != nil {
		return err
	}

	// Replace the database first, so a crash leaves the old key with the old data.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if err := os.Rename(filepath.Join(tmp, keyFileName), secrets.KeyPath(cfg.DataDir)+".new"); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(tmp, dbFileName), dbPath); err != nil {
		return err
	}
	return os.Rename(secrets.KeyPath(cfg.DataDir)+".new", secrets.KeyPath(cfg.DataDir))
}

func extractArchive(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("the archive is not a gzip file: %w", err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || (h.Name != dbFileName && h.Name != keyFileName) || seen[h.Name] {
			return fmt.Errorf("unexpected archive entry %q: a backup holds exactly %s and %s", h.Name, dbFileName, keyFileName)
		}
		limit := int64(maxDBBytes)
		if h.Name == keyFileName {
			limit = keySize
		}
		if h.Size > limit || (h.Name == keyFileName && h.Size != keySize) {
			return fmt.Errorf("archive entry %q has an invalid size", h.Name)
		}
		seen[h.Name] = true
		out, err := os.OpenFile(filepath.Join(dir, h.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(tr, limit+1))
		if err := out.Close(); copyErr == nil {
			copyErr = err
		}
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", h.Name, copyErr)
		}
	}
	if !seen[dbFileName] || !seen[keyFileName] {
		return fmt.Errorf("the archive must contain %s and %s", dbFileName, keyFileName)
	}
	return nil
}

func checkDatabase(ctx context.Context, path string) error {
	db, err := store.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("the archived database cannot be opened: %w", err)
	}
	defer db.Close()
	if err := db.IntegrityCheck(ctx); err != nil {
		return fmt.Errorf("the archived database is damaged: %w", err)
	}
	return nil
}

// HealthCheck probes the local /healthz endpoint (used by the container
// health check). A wildcard listen address is probed on loopback.
func HealthCheck(ctx context.Context, cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// Retention timing.
const (
	retentionBatch   = 1000
	retentionPause   = 20 * time.Millisecond
	retentionRounds  = 10_000
	retentionStartIn = time.Minute
)

// pruneHistory applies the retention setting once, in bounded batches, and
// returns the number of rows removed.
func pruneHistory(ctx context.Context, db *store.Store, now time.Time, batch int) (int, error) {
	settings, err := db.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	before := now.AddDate(0, 0, -settings.RetentionDays)
	total := 0
	for i := 0; i < retentionRounds; i++ {
		n, err := db.Prune(ctx, before, batch)
		if err != nil {
			return total, err
		}
		total += n
		if n == 0 {
			return total, nil
		}
		select {
		case <-time.After(retentionPause):
		case <-ctx.Done():
			return total, ctx.Err()
		}
	}
	return total, nil
}

// runRetention prunes history shortly after start and then every interval,
// until ctx is done.
func runRetention(ctx context.Context, db *store.Store, clock domain.Clock, interval time.Duration) {
	wait := retentionStartIn
	if interval < wait {
		wait = interval
	}
	for {
		select {
		case <-clock.After(wait):
		case <-ctx.Done():
			return
		}
		if n, err := pruneHistory(ctx, db, clock.Now(), retentionBatch); err != nil && ctx.Err() == nil {
			slog.Error("history retention failed", "error", err)
		} else if n > 0 {
			slog.Info("pruned old history", "rows", n)
		}
		wait = interval
	}
}
