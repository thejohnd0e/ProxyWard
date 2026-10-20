package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"proxy-checker/internal/domain"
)

// ErrSecretsUnavailable is returned when a secret must be stored but no
// encryption key is configured.
var ErrSecretsUnavailable = errors.New("secret storage is not available")

const geoKeySetting = "geo.api_key"

// SetGeoKey stores the geolocation API key encrypted. An empty key removes it.
func (s *Store) SetGeoKey(ctx context.Context, key string, now time.Time) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return s.DeleteSetting(ctx, geoKeySetting)
	}
	sealer := s.secretsSealer()
	if sealer == nil {
		return ErrSecretsUnavailable
	}
	sealed, err := sealer.Seal([]byte(key))
	if err != nil {
		return fmt.Errorf("encrypt geolocation key: %w", err)
	}
	return s.SetSetting(ctx, geoKeySetting, Setting{Value: base64.StdEncoding.EncodeToString(sealed), Secret: true}, now)
}

// GeoKey returns the decrypted geolocation key, if one is configured.
func (s *Store) GeoKey(ctx context.Context) (string, bool, error) {
	v, err := s.GetSetting(ctx, geoKeySetting)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sealer := s.secretsSealer()
	if sealer == nil {
		return "", false, ErrSecretsUnavailable
	}
	sealed, err := base64.StdEncoding.DecodeString(v.Value)
	if err != nil {
		return "", false, errors.New("stored geolocation key is corrupt")
	}
	plain, err := sealer.Open(sealed)
	if err != nil {
		return "", false, fmt.Errorf("decrypt geolocation key: %w", err)
	}
	return string(plain), true, nil
}

// GeoKeyFingerprint returns a keyed fingerprint identifying a key without
// revealing it, or "" for an empty key or when secrets are unavailable.
func (s *Store) GeoKeyFingerprint(key string) string {
	sealer := s.secretsSealer()
	if sealer == nil || key == "" {
		return ""
	}
	return sealer.Fingerprint("ipapi-key", key)
}

// GetGeoCache returns the cached record for ip, or ErrNotFound.
func (s *Store) GetGeoCache(ctx context.Context, ip string) (domain.Geo, error) {
	var (
		g       domain.Geo
		asn     sql.NullInt64
		fetched int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT ip, country_code, country, city, asn, organization, fetched_at FROM geo_cache WHERE ip = ?`, ip).
		Scan(&g.IP, &g.CountryCode, &g.Country, &g.City, &asn, &g.Organization, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Geo{}, ErrNotFound
	}
	if err != nil {
		return domain.Geo{}, fmt.Errorf("read geo cache: %w", err)
	}
	if asn.Valid {
		v := uint32(asn.Int64)
		g.ASN = &v
	}
	g.FetchedAt = time.UnixMilli(fetched).UTC()
	return g, nil
}

// PutGeoCache inserts or replaces the cached record for g.IP.
func (s *Store) PutGeoCache(ctx context.Context, g domain.Geo) error {
	var asn any
	if g.ASN != nil {
		asn = int64(*g.ASN)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO geo_cache (ip, country_code, country, city, asn, organization, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ip) DO UPDATE SET country_code = excluded.country_code, country = excluded.country,
		   city = excluded.city, asn = excluded.asn, organization = excluded.organization, fetched_at = excluded.fetched_at`,
		g.IP, g.CountryCode, g.Country, g.City, asn, g.Organization, g.FetchedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("write geo cache: %w", err)
	}
	return nil
}

// Geo pause and gate reasons.
const (
	GateRateLimited = "rate_limited"
	GateForbidden   = "forbidden"
	GateUnavailable = "unavailable"
	GateQuota       = "quota_exhausted"
)

// GeoState is the persisted provider state.
type GeoState struct {
	BudgetDay      string    // UTC date the usage counter belongs to
	Used           int       // attempts made on BudgetDay
	PauseUntil     time.Time // zero when not time-limited
	PauseReason    string    // "", GateRateLimited, GateForbidden or GateUnavailable
	BackoffAttempt int       // consecutive failures since the last success
	KeyFingerprint string    // fingerprint of the key a forbidden pause applies to
}

// GeoState returns the persisted provider state.
func (s *Store) GeoState(ctx context.Context) (GeoState, error) {
	return s.geoState(ctx, s.db)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) geoState(ctx context.Context, q queryer) (GeoState, error) {
	var (
		st    GeoState
		until int64
	)
	err := q.QueryRowContext(ctx,
		`SELECT budget_day, budget_used, pause_until, pause_reason, backoff_attempt, key_fingerprint FROM geo_state WHERE id = 1`).
		Scan(&st.BudgetDay, &st.Used, &until, &st.PauseReason, &st.BackoffAttempt, &st.KeyFingerprint)
	if err != nil {
		return GeoState{}, fmt.Errorf("read geo state: %w", err)
	}
	if until > 0 {
		st.PauseUntil = time.UnixMilli(until).UTC()
	}
	return st, nil
}

// GeoGate is the decision about starting a provider request.
type GeoGate struct {
	Allowed bool
	Reason  string    // why a request is not allowed (Gate* constant)
	RetryAt time.Time // when it may be allowed again; zero when unknown
	Used    int       // attempts counted for the current UTC day
}

// GeoBeginAttempt atomically decides whether a provider request may start now
// and, when it may, counts it against the daily budget. Pauses and the budget
// are persisted, so restarts neither forget a pause nor reset usage. A
// "forbidden" pause only ends when keyFP differs from the rejected key's.
func (s *Store) GeoBeginAttempt(ctx context.Context, now time.Time, keyFP string, limit int) (GeoGate, error) {
	now = now.UTC()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GeoGate{}, fmt.Errorf("geo attempt: %w", err)
	}
	defer tx.Rollback()
	st, err := s.geoState(ctx, tx)
	if err != nil {
		return GeoGate{}, err
	}

	if st.PauseReason == GateForbidden && st.KeyFingerprint != keyFP {
		if _, err := tx.ExecContext(ctx,
			`UPDATE geo_state SET pause_until = 0, pause_reason = '', backoff_attempt = 0, key_fingerprint = '' WHERE id = 1`); err != nil {
			return GeoGate{}, fmt.Errorf("geo attempt: %w", err)
		}
		st.PauseReason, st.PauseUntil, st.BackoffAttempt = "", time.Time{}, 0
	}
	day := now.Format("2006-01-02")
	if st.BudgetDay != day {
		st.BudgetDay, st.Used = day, 0
	}
	gate := GeoGate{Used: st.Used}
	switch {
	case st.PauseReason == GateForbidden:
		gate.Reason = GateForbidden
	case st.PauseReason != "" && st.PauseUntil.After(now):
		gate.Reason, gate.RetryAt = st.PauseReason, st.PauseUntil
	case st.Used >= limit:
		gate.Reason = GateQuota
		gate.RetryAt = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	default:
		gate.Allowed = true
		gate.Used = st.Used + 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE geo_state SET budget_day = ?, budget_used = ? WHERE id = 1`, day, gate.Used); err != nil {
		return GeoGate{}, fmt.Errorf("geo attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return GeoGate{}, fmt.Errorf("geo attempt: %w", err)
	}
	return gate, nil
}

// GeoSetPause records a pause and counts one more consecutive failure. A zero
// until is only meaningful for GateForbidden (pause until the key changes).
func (s *Store) GeoSetPause(ctx context.Context, now time.Time, reason string, until time.Time, keyFP string) error {
	var u int64
	if !until.IsZero() {
		u = until.UTC().UnixMilli()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE geo_state SET pause_until = ?, pause_reason = ?, backoff_attempt = backoff_attempt + 1, key_fingerprint = ? WHERE id = 1`,
		u, reason, keyFP); err != nil {
		return fmt.Errorf("set geo pause: %w", err)
	}
	return nil
}

// GeoClearPause clears any pause and the failure streak after a success.
func (s *Store) GeoClearPause(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE geo_state SET pause_until = 0, pause_reason = '', backoff_attempt = 0, key_fingerprint = '' WHERE id = 1`); err != nil {
		return fmt.Errorf("clear geo pause: %w", err)
	}
	return nil
}
