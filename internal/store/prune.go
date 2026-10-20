package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Prune deletes history older than before, at most batchSize rows per table
// per call, so it never holds the write lock for long. It returns the number of
// rows removed; callers repeat it until it returns 0.
//
// What is kept regardless of age: the newest availability and speed result of
// every proxy (the current state), open incidents and the failure streaks, and
// notifications that are still waiting to be delivered.
func (s *Store) Prune(ctx context.Context, before time.Time, batchSize int) (int, error) {
	if batchSize < 1 {
		return 0, errors.New("batch size must be positive")
	}
	cutoff := before.UTC().UnixMilli()
	steps := []struct{ name, query string }{
		{"availability results",
			`DELETE FROM availability_results WHERE id IN (
			   SELECT id FROM availability_results WHERE checked_at < ?1
			     AND id NOT IN (SELECT MAX(id) FROM availability_results GROUP BY proxy_id) LIMIT ?2)`},
		{"speed results",
			`DELETE FROM speed_results WHERE id IN (
			   SELECT id FROM speed_results WHERE checked_at < ?1
			     AND id NOT IN (SELECT MAX(id) FROM speed_results GROUP BY proxy_id) LIMIT ?2)`},
		{"jobs",
			`DELETE FROM jobs WHERE id IN (
			   SELECT id FROM jobs WHERE status NOT IN ('pending','running') AND finished_at < ?1 LIMIT ?2)`},
		{"notifications",
			`DELETE FROM notification_outbox WHERE id IN (
			   SELECT id FROM notification_outbox WHERE created_at < ?1
			     AND (delivered_at IS NOT NULL OR attempts >= ` + fmt.Sprint(MaxOutboxAttempts) + `) LIMIT ?2)`},
		{"events",
			`DELETE FROM events WHERE id IN (
			   SELECT id FROM events WHERE created_at < ?1
			     AND id NOT IN (SELECT event_id FROM notification_outbox WHERE event_id IS NOT NULL AND delivered_at IS NULL AND attempts < ` + fmt.Sprint(MaxOutboxAttempts) + `)
			   LIMIT ?2)`},
		{"incidents",
			`DELETE FROM incidents WHERE id IN (
			   SELECT id FROM incidents WHERE ended_at IS NOT NULL AND ended_at < ?1 LIMIT ?2)`},
		{"geolocation cache",
			`DELETE FROM geo_cache WHERE ip IN (SELECT ip FROM geo_cache WHERE fetched_at < ?1 LIMIT ?2)`},
		{"sessions",
			`DELETE FROM sessions WHERE token_digest IN (SELECT token_digest FROM sessions WHERE expires_at < ?1 LIMIT ?2)`},
	}
	total := 0
	for _, st := range steps {
		n, err := s.pruneStep(ctx, st.query, cutoff, batchSize)
		if err != nil {
			return total, fmt.Errorf("prune %s: %w", st.name, err)
		}
		total += n
	}
	return total, nil
}

func (s *Store) pruneStep(ctx context.Context, query string, cutoff int64, batch int) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, query, cutoff, batch)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Backup writes a consistent snapshot of the database to dest, which must not
// exist. It uses SQLite's VACUUM INTO, which reads a stable view and so is safe
// while the server keeps writing and while the database is in WAL mode; copying
// the live file would miss data that is still in the WAL.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup destination %s already exists", dest)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("backup database: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's integrity check and returns an error unless it reports "ok".
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New(result)
	}
	return nil
}
