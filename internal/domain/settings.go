package domain

import "time"

// Schedule configures periodic checks of one kind.
type Schedule struct {
	Enabled  bool
	Interval time.Duration
}

// SystemClock is the real wall clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Clock abstracts time so schedulers and retries can be tested.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Settings are the global monitoring settings.
type Settings struct {
	Availability Schedule // global availability schedule
	Speed        Schedule // global speed schedule

	AvailabilityConcurrency int           // parallel availability checks, 1-20
	AvailabilityTimeout     time.Duration // per request, 1-60 s

	SpeedSampleBytes      int64         // timed bytes per direction, 1 byte - 64 MiB
	SpeedWarmupBytes      int64         // untimed bytes per direction, 0 - 4 MiB
	SpeedDirectionTimeout time.Duration // per direction, 1-120 s

	DownThreshold int // consecutive definite failures before a down incident, 1-10
	RetentionDays int // history retention, 1-365

	// GeoSource selects where exit-IP geolocation comes from: GeoSourceAuto
	// (try the sources in order), GeoSourceOff, or the ID of one source.
	GeoSource string
}

// Geolocation source choices that are not a concrete source.
const (
	GeoSourceAuto = "auto"
	GeoSourceOff  = "off"
)

// DefaultSettings returns the specified defaults. Both schedules start
// disabled; the intervals are the suggested values (5 minutes, 6 hours).
func DefaultSettings() Settings {
	return Settings{
		Availability:            Schedule{Enabled: false, Interval: 5 * time.Minute},
		Speed:                   Schedule{Enabled: false, Interval: 6 * time.Hour},
		AvailabilityConcurrency: 5,
		AvailabilityTimeout:     10 * time.Second,
		SpeedSampleBytes:        8 << 20,
		SpeedWarmupBytes:        256 << 10,
		SpeedDirectionTimeout:   30 * time.Second,
		DownThreshold:           3,
		RetentionDays:           30,
		GeoSource:               GeoSourceAuto,
	}
}

// SettingsError describes the first invalid setting.
type SettingsError struct{ Field, Message string }

func (e *SettingsError) Error() string { return e.Field + ": " + e.Message }

// Validate checks every bound from the specification.
func (s Settings) Validate() error {
	bad := func(field, msg string) error { return &SettingsError{Field: field, Message: msg} }
	switch {
	case s.Availability.Interval < MinInterval || s.Availability.Interval > MaxInterval:
		return bad("availability_interval", "must be between 1 and 10080 minutes")
	case s.Speed.Interval < MinInterval || s.Speed.Interval > MaxInterval:
		return bad("speed_interval", "must be between 1 and 10080 minutes")
	case s.AvailabilityConcurrency < 1 || s.AvailabilityConcurrency > 20:
		return bad("availability_concurrency", "must be between 1 and 20")
	case s.AvailabilityTimeout < time.Second || s.AvailabilityTimeout > time.Minute:
		return bad("availability_timeout", "must be between 1 and 60 seconds")
	case s.SpeedSampleBytes < 1 || s.SpeedSampleBytes > 64<<20:
		return bad("speed_sample_bytes", "must be between 1 byte and 64 MiB")
	case s.SpeedWarmupBytes < 0 || s.SpeedWarmupBytes > 4<<20:
		return bad("speed_warmup_bytes", "must be between 0 and 4 MiB")
	case s.SpeedDirectionTimeout < time.Second || s.SpeedDirectionTimeout > 120*time.Second:
		return bad("speed_direction_timeout", "must be between 1 and 120 seconds")
	case s.DownThreshold < 1 || s.DownThreshold > 10:
		return bad("down_threshold", "must be between 1 and 10")
	case s.RetentionDays < 1 || s.RetentionDays > 365:
		return bad("retention_days", "must be between 1 and 365")
	case s.GeoSource == "" || len(s.GeoSource) > 32:
		return bad("geo_source", "must be auto, off or the ID of a source")
	}
	return nil
}

// EffectiveSchedule returns the override when present, else the global schedule.
func EffectiveSchedule(global Schedule, override *Schedule) Schedule {
	if override != nil {
		return *override
	}
	return global
}
