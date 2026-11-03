package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

const appSettingsKey = "app.settings"

// settingsJSON is the stored shape. Absent fields keep their default, so
// newer versions can add settings without migrating old rows.
type settingsJSON struct {
	AvailabilityEnabled     bool   `json:"availability_enabled"`
	AvailabilityIntervalS   int64  `json:"availability_interval_s"`
	SpeedEnabled            bool   `json:"speed_enabled"`
	SpeedIntervalS          int64  `json:"speed_interval_s"`
	AvailabilityConcurrency int    `json:"availability_concurrency"`
	AvailabilityTimeoutS    int64  `json:"availability_timeout_s"`
	SpeedSampleBytes        int64  `json:"speed_sample_bytes"`
	SpeedWarmupBytes        int64  `json:"speed_warmup_bytes"`
	SpeedDirectionTimeoutS  int64  `json:"speed_direction_timeout_s"`
	DownThreshold           int    `json:"down_threshold"`
	RetentionDays           int    `json:"retention_days"`
	GeoSource               string `json:"geo_source"`
}

func toJSON(s domain.Settings) settingsJSON {
	return settingsJSON{
		AvailabilityEnabled: s.Availability.Enabled, AvailabilityIntervalS: int64(s.Availability.Interval / time.Second),
		SpeedEnabled: s.Speed.Enabled, SpeedIntervalS: int64(s.Speed.Interval / time.Second),
		AvailabilityConcurrency: s.AvailabilityConcurrency, AvailabilityTimeoutS: int64(s.AvailabilityTimeout / time.Second),
		SpeedSampleBytes: s.SpeedSampleBytes, SpeedWarmupBytes: s.SpeedWarmupBytes,
		SpeedDirectionTimeoutS: int64(s.SpeedDirectionTimeout / time.Second),
		DownThreshold:          s.DownThreshold, RetentionDays: s.RetentionDays, GeoSource: s.GeoSource,
	}
}

func (j settingsJSON) settings() domain.Settings {
	return domain.Settings{
		Availability:            domain.Schedule{Enabled: j.AvailabilityEnabled, Interval: time.Duration(j.AvailabilityIntervalS) * time.Second},
		Speed:                   domain.Schedule{Enabled: j.SpeedEnabled, Interval: time.Duration(j.SpeedIntervalS) * time.Second},
		AvailabilityConcurrency: j.AvailabilityConcurrency,
		AvailabilityTimeout:     time.Duration(j.AvailabilityTimeoutS) * time.Second,
		SpeedSampleBytes:        j.SpeedSampleBytes,
		SpeedWarmupBytes:        j.SpeedWarmupBytes,
		SpeedDirectionTimeout:   time.Duration(j.SpeedDirectionTimeoutS) * time.Second,
		DownThreshold:           j.DownThreshold,
		RetentionDays:           j.RetentionDays,
		GeoSource:               j.GeoSource,
	}
}

// GetSettings returns the global settings, with defaults for anything unset.
func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	v, err := s.GetSetting(ctx, appSettingsKey)
	if errors.Is(err, ErrNotFound) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return domain.Settings{}, err
	}
	j := toJSON(domain.DefaultSettings())
	if err := json.Unmarshal([]byte(v.Value), &j); err != nil {
		return domain.Settings{}, fmt.Errorf("stored settings are corrupt: %w", err)
	}
	return j.settings(), nil
}

// PutSettings validates and stores the global settings.
func (s *Store) PutSettings(ctx context.Context, st domain.Settings, now time.Time) error {
	if err := st.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(toJSON(st))
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	defer tx.Rollback()
	before, err := settingsFromTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("read previous settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, secret, updated_at) VALUES (?, ?, 0, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, secret = 0, updated_at = excluded.updated_at`,
		appSettingsKey, string(b), now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := s.reconcileAllSchedulesTx(ctx, tx, before, st, now); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return tx.Commit()
}
