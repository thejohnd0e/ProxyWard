package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// CompleteAvailability atomically commits a result, incident/event/
// outbox changes, schedule advancement and terminal job status. A durable
// cancellation intent wins if it committed before this transaction began.
func (s *Store) CompleteAvailability(ctx context.Context, completion domain.AvailabilityCompletion) (IncidentOutcome, error) {
	jobID, result, now := completion.JobID, completion.Result, completion.CompletedAt
	if result.State != domain.StateUnknown && result.State != domain.StateUp && result.State != domain.StateDown && result.State != domain.StateIndeterminate {
		return IncidentOutcome{}, fmt.Errorf("%w: unknown state", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IncidentOutcome{}, fmt.Errorf("complete availability: %w", err)
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return IncidentOutcome{}, ErrNotFound
	}
	if err != nil {
		return IncidentOutcome{}, fmt.Errorf("complete availability: %w", err)
	}
	if job.Kind != domain.CheckAvailability || job.ProxyID != result.ProxyID {
		return IncidentOutcome{}, fmt.Errorf("%w: job/result mismatch", ErrInvalid)
	}
	if job.Status == domain.JobDone {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM availability_results WHERE job_id = ?)`, jobID).Scan(&exists); err != nil {
			return IncidentOutcome{}, fmt.Errorf("check completion replay: %w", err)
		}
		if exists == 1 {
			return IncidentOutcome{}, nil
		}
		return IncidentOutcome{}, ErrConflict
	}
	if job.Status != domain.JobRunning {
		return IncidentOutcome{}, ErrNotFound
	}
	if job.ScheduleRevision != completion.ScheduleRevision {
		return IncidentOutcome{}, ErrConflict
	}
	finishedAt := now.UTC().UnixMilli()
	if job.CancelRequestedAt != nil {
		if job.Source == domain.SourceScheduled {
			if err := advanceScheduleAfterJobTx(ctx, tx, job.ProxyID, job.Kind, job.ScheduleRevision, now); err != nil {
				return IncidentOutcome{}, fmt.Errorf("advance canceled schedule: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'canceled', finished_at = ? WHERE id = ? AND status = 'running'`, finishedAt, jobID); err != nil {
			return IncidentOutcome{}, fmt.Errorf("cancel availability job: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return IncidentOutcome{}, fmt.Errorf("commit canceled availability job: %w", err)
		}
		return IncidentOutcome{}, ErrJobCanceled
	}
	settings, err := settingsFromTx(ctx, tx)
	if err != nil {
		return IncidentOutcome{}, fmt.Errorf("read completion settings: %w", err)
	}
	if err := addAvailabilityForJobTx(ctx, tx, jobID, result); err != nil {
		return IncidentOutcome{}, err
	}
	out, err := recordAvailabilityOutcomeTx(ctx, tx, result, settings.DownThreshold)
	if err != nil {
		return IncidentOutcome{}, err
	}
	if err := advanceScheduleAfterJobTx(ctx, tx, job.ProxyID, job.Kind, job.ScheduleRevision, now); err != nil && !errors.Is(err, ErrNotFound) {
		return IncidentOutcome{}, fmt.Errorf("advance availability schedule: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'done', finished_at = ? WHERE id = ? AND status = 'running' AND cancel_requested_at IS NULL`, finishedAt, jobID)
	if err != nil {
		return IncidentOutcome{}, fmt.Errorf("finish availability job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return IncidentOutcome{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return IncidentOutcome{}, fmt.Errorf("commit availability outcome: %w", err)
	}
	return out, nil
}

// CompleteSpeed atomically commits the speed result, schedule and job
// state, with the same durable cancellation precedence as availability work.
func (s *Store) CompleteSpeed(ctx context.Context, completion domain.SpeedCompletion) error {
	jobID, result, now := completion.JobID, completion.Result, completion.CompletedAt
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("complete speed: %w", err)
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("complete speed: %w", err)
	}
	if job.Kind != domain.CheckSpeed || job.ProxyID != result.ProxyID {
		return fmt.Errorf("%w: job/result mismatch", ErrInvalid)
	}
	if job.Status == domain.JobDone {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM speed_results WHERE job_id = ?)`, jobID).Scan(&exists); err != nil {
			return fmt.Errorf("check completion replay: %w", err)
		}
		if exists == 1 {
			return nil
		}
		return ErrConflict
	}
	if job.Status != domain.JobRunning {
		return ErrNotFound
	}
	if job.ScheduleRevision != completion.ScheduleRevision {
		return ErrConflict
	}
	finishedAt := now.UTC().UnixMilli()
	if job.CancelRequestedAt != nil {
		if job.Source == domain.SourceScheduled {
			if err := advanceScheduleAfterJobTx(ctx, tx, job.ProxyID, job.Kind, job.ScheduleRevision, now); err != nil {
				return fmt.Errorf("advance canceled schedule: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'canceled', finished_at = ? WHERE id = ? AND status = 'running'`, finishedAt, jobID); err != nil {
			return fmt.Errorf("cancel speed job: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit canceled speed job: %w", err)
		}
		return ErrJobCanceled
	}
	if err := addSpeedForJobTx(ctx, tx, jobID, result); err != nil {
		return err
	}
	if err := advanceScheduleAfterJobTx(ctx, tx, job.ProxyID, job.Kind, job.ScheduleRevision, now); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("advance speed schedule: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'done', finished_at = ? WHERE id = ? AND status = 'running' AND cancel_requested_at IS NULL`, finishedAt, jobID)
	if err != nil {
		return fmt.Errorf("finish speed job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit speed outcome: %w", err)
	}
	return nil
}
