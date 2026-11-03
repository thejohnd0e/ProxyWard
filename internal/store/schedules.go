package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

type scheduleRow struct {
	id               domain.ProxyID
	paused           bool
	availability     *domain.Schedule
	speed            *domain.Schedule
	availabilityRev  int64
	speedRev         int64
	nextAvailability *time.Time
	nextSpeed        *time.Time
}

func settingsFromTx(ctx context.Context, tx *sql.Tx) (domain.Settings, error) {
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, appSettingsKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return domain.Settings{}, err
	}
	j := toJSON(domain.DefaultSettings())
	if err := json.Unmarshal([]byte(value), &j); err != nil {
		return domain.Settings{}, fmt.Errorf("stored settings are corrupt: %w", err)
	}
	return j.settings(), nil
}

func readScheduleRows(ctx context.Context, tx *sql.Tx) ([]scheduleRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, availability_enabled, availability_interval_s, speed_enabled, speed_interval_s,
		monitoring_paused, availability_revision, speed_revision, next_availability_at, next_speed_at FROM proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scheduleRow
	for rows.Next() {
		var row scheduleRow
		var availOn, availInterval, speedOn, speedInterval, nextAvailability, nextSpeed sql.NullInt64
		if err := rows.Scan(&row.id, &availOn, &availInterval, &speedOn, &speedInterval, &row.paused, &row.availabilityRev, &row.speedRev, &nextAvailability, &nextSpeed); err != nil {
			return nil, err
		}
		if availOn.Valid {
			row.availability = &domain.Schedule{Enabled: availOn.Int64 == 1, Interval: time.Duration(availInterval.Int64) * time.Second}
		}
		if speedOn.Valid {
			row.speed = &domain.Schedule{Enabled: speedOn.Int64 == 1, Interval: time.Duration(speedInterval.Int64) * time.Second}
		}
		if nextAvailability.Valid {
			at := time.UnixMilli(nextAvailability.Int64).UTC()
			row.nextAvailability = &at
		}
		if nextSpeed.Valid {
			at := time.UnixMilli(nextSpeed.Int64).UTC()
			row.nextSpeed = &at
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func nextScheduleRun(row scheduleRow, kind domain.CheckKind) *time.Time {
	if kind == domain.CheckSpeed {
		return row.nextSpeed
	}
	return row.nextAvailability
}

func scheduleFor(row scheduleRow, kind domain.CheckKind) (*domain.Schedule, int64) {
	if kind == domain.CheckSpeed {
		return row.speed, row.speedRev
	}
	return row.availability, row.availabilityRev
}

func globalSchedule(settings domain.Settings, kind domain.CheckKind) domain.Schedule {
	if kind == domain.CheckSpeed {
		return settings.Speed
	}
	return settings.Availability
}

func sameSchedule(a, b domain.Schedule) bool {
	return a.Enabled == b.Enabled && a.Interval == b.Interval
}

func reconcileOneScheduleTx(ctx context.Context, tx *sql.Tx, id domain.ProxyID, kind domain.CheckKind,
	oldSchedule, newSchedule domain.Schedule, oldPaused, newPaused bool, revision int64, now time.Time) error {
	if sameSchedule(oldSchedule, newSchedule) && oldPaused == newPaused {
		return nil
	}
	col, revCol := "next_availability_at", "availability_revision"
	if kind == domain.CheckSpeed {
		col, revCol = "next_speed_at", "speed_revision"
	}
	var next any
	if newSchedule.Enabled && !newPaused && oldSchedule.Enabled && !oldPaused {
		next = now.UTC().Add(newSchedule.Interval).UnixMilli()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE proxies SET `+col+` = ?, `+revCol+` = ? WHERE id = ?`, next, revision+1, int64(id)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE proxy_id = ? AND kind = ? AND source = 'scheduled' AND status = 'pending'`, int64(id), string(kind)); err != nil {
		return err
	}
	return nil
}

func effective(row scheduleRow, settings domain.Settings, kind domain.CheckKind) domain.Schedule {
	configured, _ := scheduleFor(row, kind)
	return domain.EffectiveSchedule(globalSchedule(settings, kind), configured)
}

func (s *Store) reconcileAllSchedulesTx(ctx context.Context, tx *sql.Tx, before, after domain.Settings, now time.Time) error {
	rows, err := readScheduleRows(ctx, tx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		for _, kind := range []domain.CheckKind{domain.CheckAvailability, domain.CheckSpeed} {
			_, rev := scheduleFor(row, kind)
			if err := reconcileOneScheduleTx(ctx, tx, row.id, kind, effective(row, before, kind), effective(row, after, kind), row.paused, row.paused, rev, now); err != nil {
				return fmt.Errorf("reconcile proxy %d %s schedule: %w", row.id, kind, err)
			}
		}
	}
	return nil
}

// ReconcileSchedules atomically removes pending scheduled work and clears next
// runs for schedules that are currently disabled or paused. It is safe to call
// after any schedule writer and never applies a stale scheduler snapshot.
func (s *Store) ReconcileSchedules(ctx context.Context, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reconcile schedules: %w", err)
	}
	defer tx.Rollback()
	rows, err := readScheduleRows(ctx, tx)
	if err != nil {
		return fmt.Errorf("reconcile schedules: %w", err)
	}
	settings, err := settingsFromTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("reconcile schedules: %w", err)
	}
	for _, row := range rows {
		for _, kind := range []domain.CheckKind{domain.CheckAvailability, domain.CheckSpeed} {
			eff := effective(row, settings, kind)
			if !eff.Enabled || row.paused {
				col := "next_availability_at"
				if kind == domain.CheckSpeed {
					col = "next_speed_at"
				}
				if _, err := tx.ExecContext(ctx, `UPDATE proxies SET `+col+` = NULL WHERE id = ?`, int64(row.id)); err != nil {
					return fmt.Errorf("reconcile schedules: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE proxy_id = ? AND kind = ? AND source = 'scheduled' AND status = 'pending'`, int64(row.id), string(kind)); err != nil {
					return fmt.Errorf("reconcile schedules: %w", err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile schedules: %w", err)
	}
	return nil
}

// AdvanceScheduleAfterJob advances the current schedule only if the job was
// claimed under the still-current revision. The settings read and update share
// a transaction so a concurrent settings write cannot mix revisions.
func (s *Store) AdvanceScheduleAfterJob(ctx context.Context, id domain.ProxyID, kind domain.CheckKind, revision int64, now time.Time) error {
	revCol := "availability_revision"
	if kind == domain.CheckSpeed {
		revCol = "speed_revision"
	} else if kind != domain.CheckAvailability {
		return fmt.Errorf("%w: kind", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("advance schedule: %w", err)
	}
	defer tx.Rollback()
	settings, err := settingsFromTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("advance schedule: %w", err)
	}
	rows, err := readScheduleRows(ctx, tx)
	if err != nil {
		return fmt.Errorf("advance schedule: %w", err)
	}
	for _, row := range rows {
		if row.id != id {
			continue
		}
		_, currentRevision := scheduleFor(row, kind)
		if currentRevision != revision {
			return tx.Commit()
		}
		eff := effective(row, settings, kind)
		var next *time.Time
		if eff.Enabled && !row.paused {
			at := now.UTC().Add(eff.Interval)
			next = &at
		}
		col := "next_availability_at"
		if kind == domain.CheckSpeed {
			col = "next_speed_at"
		}
		var value any
		if next != nil {
			value = next.UnixMilli()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxies SET `+col+` = ? WHERE id = ? AND `+revCol+` = ?`, value, int64(id), revision); err != nil {
			return fmt.Errorf("advance schedule: %w", err)
		}
		return tx.Commit()
	}
	return ErrNotFound
}
