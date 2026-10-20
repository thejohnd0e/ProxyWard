package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"proxy-checker/internal/domain"
)

const jobColumns = `id, proxy_id, kind, status, source, due_at, created_at, started_at, finished_at`

func scanJob(r rowScanner) (domain.Job, error) {
	var (
		j                  domain.Job
		kind, status       string
		due, created       int64
		started, finishedN sql.NullInt64
	)
	if err := r.Scan(&j.ID, &j.ProxyID, &kind, &status, &j.Source, &due, &created, &started, &finishedN); err != nil {
		return domain.Job{}, err
	}
	j.Kind, j.Status = domain.CheckKind(kind), domain.JobStatus(status)
	j.DueAt, j.CreatedAt = time.UnixMilli(due).UTC(), time.UnixMilli(created).UTC()
	if started.Valid {
		t := time.UnixMilli(started.Int64).UTC()
		j.StartedAt = &t
	}
	if finishedN.Valid {
		t := time.UnixMilli(finishedN.Int64).UTC()
		j.FinishedAt = &t
	}
	return j, nil
}

func validKind(k domain.CheckKind) bool {
	return k == domain.CheckAvailability || k == domain.CheckSpeed
}

// EnqueueResult reports what an enqueue did for each requested proxy.
type EnqueueResult struct {
	New      []domain.Job // jobs created by this call
	Existing []domain.Job // active jobs that already covered the request
	Missing  []domain.ProxyID
}

// EnqueueJobs creates one pending job per proxy for kind, unless the proxy
// already has an active (pending or running) job of that kind, which is
// returned instead. A manual request promotes an existing scheduled pending
// job: it becomes manual and due no later than due, so disabling schedules
// cannot silently drop what the user asked for.
func (s *Store) EnqueueJobs(ctx context.Context, ids []domain.ProxyID, kind domain.CheckKind, source string, due, now time.Time) (EnqueueResult, error) {
	var res EnqueueResult
	if !validKind(kind) || (source != domain.SourceManual && source != domain.SourceScheduled) {
		return res, fmt.Errorf("%w: kind or source", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("enqueue jobs: %w", err)
	}
	defer tx.Rollback()

	seen := map[domain.ProxyID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true

		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM proxies WHERE id = ?`, int64(id)).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			res.Missing = append(res.Missing, id)
			continue
		} else if err != nil {
			return res, fmt.Errorf("enqueue jobs: %w", err)
		}

		if source == domain.SourceScheduled {
			// Re-check inside the transaction that the proxy is still due. A
			// check that finished after the scheduler took its snapshot has
			// already stored a later next run, and must not be queued again.
			col := "next_availability_at"
			if kind == domain.CheckSpeed {
				col = "next_speed_at"
			}
			var next sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT `+col+` FROM proxies WHERE id = ?`, int64(id)).Scan(&next); err != nil {
				return res, fmt.Errorf("enqueue jobs: %w", err)
			}
			if next.Valid && next.Int64 > now.UTC().UnixMilli() {
				continue
			}
		}

		existing, err := scanJob(tx.QueryRowContext(ctx,
			`SELECT `+jobColumns+` FROM jobs WHERE proxy_id = ? AND kind = ? AND status IN ('pending','running')`, int64(id), string(kind)))
		switch {
		case err == nil:
			if source == domain.SourceManual && existing.Status == domain.JobPending && existing.Source == domain.SourceScheduled {
				newDue := existing.DueAt
				if due.Before(newDue) {
					newDue = due
				}
				if _, err := tx.ExecContext(ctx, `UPDATE jobs SET source = 'manual', due_at = ? WHERE id = ?`, newDue.UTC().UnixMilli(), existing.ID); err != nil {
					return res, fmt.Errorf("enqueue jobs: %w", err)
				}
				existing.Source, existing.DueAt = domain.SourceManual, newDue.UTC()
			}
			res.Existing = append(res.Existing, existing)
		case errors.Is(err, sql.ErrNoRows):
			r, err := tx.ExecContext(ctx,
				`INSERT INTO jobs (proxy_id, kind, status, source, due_at, created_at) VALUES (?, ?, 'pending', ?, ?, ?)`,
				int64(id), string(kind), source, due.UTC().UnixMilli(), now.UTC().UnixMilli())
			if err != nil {
				return res, fmt.Errorf("enqueue jobs: %w", err)
			}
			jid, _ := r.LastInsertId()
			res.New = append(res.New, domain.Job{ID: jid, ProxyID: id, Kind: kind, Status: domain.JobPending, Source: source,
				DueAt: due.UTC(), CreatedAt: now.UTC()})
		default:
			return res, fmt.Errorf("enqueue jobs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("enqueue jobs: %w", err)
	}
	return res, nil
}

// ClaimJob atomically turns the oldest due pending job of kind into a running
// one. A job is skipped while its proxy has any running job, so the two check
// kinds never overlap on one proxy.
func (s *Store) ClaimJob(ctx context.Context, kind domain.CheckKind, now time.Time) (domain.Job, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM jobs j
		 WHERE j.status = 'pending' AND j.kind = ? AND j.due_at <= ?
		   AND NOT EXISTS (SELECT 1 FROM jobs r WHERE r.proxy_id = j.proxy_id AND r.status = 'running')
		 ORDER BY j.due_at, j.id LIMIT 1`, string(kind), now.UTC().UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, nil
	}
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	ms := now.UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'running', started_at = ? WHERE id = ? AND status = 'pending'`, ms, j.ID); err != nil {
		return domain.Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	t := now.UTC()
	j.Status, j.StartedAt = domain.JobRunning, &t
	return j, true, nil
}

// FinishJob moves a pending or running job to a terminal status.
func (s *Store) FinishJob(ctx context.Context, id int64, status domain.JobStatus, now time.Time) error {
	switch status {
	case domain.JobDone, domain.JobCanceled, domain.JobInterrupted:
	default:
		return fmt.Errorf("%w: not a terminal status", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, finished_at = ? WHERE id = ? AND status IN ('pending','running')`,
		string(status), now.UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetJob returns one job, or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, id int64) (domain.Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("get job: %w", err)
	}
	return j, nil
}

// ListActiveJobs returns pending and running jobs in execution order.
func (s *Store) ListActiveJobs(ctx context.Context) ([]domain.Job, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE status IN ('pending','running') ORDER BY due_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CancelPendingJob cancels a pending job. A running job is returned
// unchanged with canceled=false: only its runner can stop it.
func (s *Store) CancelPendingJob(ctx context.Context, id int64, now time.Time) (job domain.Job, canceled bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("cancel job: %w", err)
	}
	defer tx.Rollback()
	job, err = scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, false, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, false, fmt.Errorf("cancel job: %w", err)
	}
	if job.Status != domain.JobPending {
		return job, false, nil
	}
	ms := now.UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'canceled', finished_at = ? WHERE id = ?`, ms, id); err != nil {
		return domain.Job{}, false, fmt.Errorf("cancel job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Job{}, false, fmt.Errorf("cancel job: %w", err)
	}
	t := now.UTC()
	job.Status, job.FinishedAt = domain.JobCanceled, &t
	return job, true, nil
}

// CancelProxyJobs cancels all pending jobs of a proxy and returns its running
// jobs so the caller can stop them.
func (s *Store) CancelProxyJobs(ctx context.Context, id domain.ProxyID, now time.Time) ([]domain.Job, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cancel proxy jobs: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE jobs SET status = 'canceled', finished_at = ? WHERE proxy_id = ? AND status = 'pending'`,
		now.UTC().UnixMilli(), int64(id)); err != nil {
		return nil, fmt.Errorf("cancel proxy jobs: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE proxy_id = ? AND status = 'running'`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("cancel proxy jobs: %w", err)
	}
	var running []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("cancel proxy jobs: %w", err)
		}
		running = append(running, j)
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cancel proxy jobs: %w", err)
	}
	return running, nil
}

// RemoveScheduledPending deletes pending scheduled jobs of kind for every
// proxy not listed in keep. Manual jobs are never touched.
func (s *Store) RemoveScheduledPending(ctx context.Context, kind domain.CheckKind, keep []domain.ProxyID) (int, error) {
	q := `DELETE FROM jobs WHERE status = 'pending' AND source = 'scheduled' AND kind = ?`
	args := []any{string(kind)}
	if len(keep) > 0 {
		q += ` AND proxy_id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",") + `)`
		for _, id := range keep {
			args = append(args, int64(id))
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("remove scheduled jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RecoverInterrupted marks jobs left running by a previous process as
// interrupted. It records no proxy failure.
func (s *Store) RecoverInterrupted(ctx context.Context, now time.Time) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, now.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("recover jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PruneJobs deletes finished jobs that ended before the cutoff.
func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE status NOT IN ('pending','running') AND finished_at < ?`, before.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("prune jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ScheduleState is a proxy's schedule overrides and persisted next-run times.
type ScheduleState struct {
	ProxyID                     domain.ProxyID
	Availability, Speed         *domain.Schedule // overrides; nil inherits
	NextAvailability, NextSpeed *time.Time
}

// ListSchedules returns the schedule state of every proxy, ordered by ID.
func (s *Store) ListSchedules(ctx context.Context) ([]ScheduleState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, availability_enabled, availability_interval_s, speed_enabled, speed_interval_s,
		        next_availability_at, next_speed_at FROM proxies ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var out []ScheduleState
	for rows.Next() {
		var (
			st             ScheduleState
			ae, ai, se, si sql.NullInt64
			nextA, nextS   sql.NullInt64
		)
		if err := rows.Scan(&st.ProxyID, &ae, &ai, &se, &si, &nextA, &nextS); err != nil {
			return nil, fmt.Errorf("list schedules: %w", err)
		}
		if ae.Valid {
			st.Availability = &domain.Schedule{Enabled: ae.Int64 == 1, Interval: time.Duration(ai.Int64) * time.Second}
		}
		if se.Valid {
			st.Speed = &domain.Schedule{Enabled: se.Int64 == 1, Interval: time.Duration(si.Int64) * time.Second}
		}
		if nextA.Valid {
			t := time.UnixMilli(nextA.Int64).UTC()
			st.NextAvailability = &t
		}
		if nextS.Valid {
			t := time.UnixMilli(nextS.Int64).UTC()
			st.NextSpeed = &t
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SetNextRun stores (or with nil clears) the next scheduled run of a kind.
func (s *Store) SetNextRun(ctx context.Context, id domain.ProxyID, kind domain.CheckKind, at *time.Time) error {
	var col string
	switch kind {
	case domain.CheckAvailability:
		col = "next_availability_at"
	case domain.CheckSpeed:
		col = "next_speed_at"
	default:
		return fmt.Errorf("%w: kind", ErrInvalid)
	}
	var v any
	if at != nil {
		v = at.UTC().UnixMilli()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE proxies SET `+col+` = ? WHERE id = ?`, v, int64(id))
	if err != nil {
		return fmt.Errorf("set next run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
