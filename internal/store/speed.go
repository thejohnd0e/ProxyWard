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

// AddSpeed stores one speed result. A direction without a completed sample is
// stored with a NULL rate, never zero. ErrNotFound means the proxy is gone.
func (s *Store) AddSpeed(ctx context.Context, sp domain.Speed) error {
	d, u := sp.Download, sp.Upload
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO speed_results (proxy_id, checked_at, provider, method,
		   download_bytes, download_us, download_mbps, download_low_conf, download_fail_code, download_fail_msg,
		   upload_bytes, upload_us, upload_mbps, upload_low_conf, upload_fail_code, upload_fail_msg, total_bytes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(sp.ProxyID), sp.CheckedAt.UTC().UnixMilli(), sp.Provider, sp.Method,
		d.Bytes, d.Duration.Microseconds(), nullFloat(d.Mbps), boolInt(d.LowConfidence), failCode(d.Failure), failMsg(d.Failure),
		u.Bytes, u.Duration.Microseconds(), nullFloat(u.Mbps), boolInt(u.LowConfidence), failCode(u.Failure), failMsg(u.Failure),
		sp.TotalBytes)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrNotFound
		}
		return fmt.Errorf("add speed: %w", err)
	}
	return nil
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func failCode(f *domain.Failure) string {
	if f == nil {
		return ""
	}
	return f.Code
}

func failMsg(f *domain.Failure) string {
	if f == nil {
		return ""
	}
	return f.Message
}

const speedColumns = `proxy_id, checked_at, provider, method,
	download_bytes, download_us, download_mbps, download_low_conf, download_fail_code, download_fail_msg,
	upload_bytes, upload_us, upload_mbps, upload_low_conf, upload_fail_code, upload_fail_msg, total_bytes`

func scanSpeed(r rowScanner) (domain.Speed, error) {
	var (
		sp         domain.Speed
		proxyID    int64
		at         int64
		dMbps      sql.NullFloat64
		uMbps      sql.NullFloat64
		dUS, uUS   int64
		dLow, uLow int
		dCode, dMs string
		uCode, uMs string
	)
	if err := r.Scan(&proxyID, &at, &sp.Provider, &sp.Method,
		&sp.Download.Bytes, &dUS, &dMbps, &dLow, &dCode, &dMs,
		&sp.Upload.Bytes, &uUS, &uMbps, &uLow, &uCode, &uMs, &sp.TotalBytes); err != nil {
		return domain.Speed{}, err
	}
	sp.ProxyID = domain.ProxyID(proxyID)
	sp.CheckedAt = time.UnixMilli(at).UTC()
	fill := func(t *domain.Transfer, us int64, mbps sql.NullFloat64, low int, code, msg string) {
		t.Duration = time.Duration(us) * time.Microsecond
		t.LowConfidence = low == 1
		if mbps.Valid {
			v := mbps.Float64
			t.Mbps = &v
		}
		if code != "" || msg != "" {
			t.Failure = &domain.Failure{Code: code, Message: msg}
		}
	}
	fill(&sp.Download, dUS, dMbps, dLow, dCode, dMs)
	fill(&sp.Upload, uUS, uMbps, uLow, uCode, uMs)
	return sp, nil
}

// LatestSpeed returns the newest speed result for a proxy, or ErrNotFound.
func (s *Store) LatestSpeed(ctx context.Context, id domain.ProxyID) (domain.Speed, error) {
	sp, err := scanSpeed(s.db.QueryRowContext(ctx,
		`SELECT `+speedColumns+` FROM speed_results WHERE proxy_id = ? ORDER BY checked_at DESC, id DESC LIMIT 1`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Speed{}, ErrNotFound
	}
	if err != nil {
		return domain.Speed{}, fmt.Errorf("latest speed: %w", err)
	}
	return sp, nil
}

// ListSpeed returns up to limit results, newest first. Zero from/to leave that
// side open; both bounds are inclusive.
func (s *Store) ListSpeed(ctx context.Context, id domain.ProxyID, from, to time.Time, limit int) ([]domain.Speed, error) {
	if limit <= 0 {
		return nil, nil
	}
	q := `SELECT ` + speedColumns + ` FROM speed_results WHERE proxy_id = ?`
	args := []any{int64(id)}
	if !from.IsZero() {
		q += ` AND checked_at >= ?`
		args = append(args, from.UTC().UnixMilli())
	}
	if !to.IsZero() {
		q += ` AND checked_at <= ?`
		args = append(args, to.UTC().UnixMilli())
	}
	q += ` ORDER BY checked_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list speed: %w", err)
	}
	defer rows.Close()
	var out []domain.Speed
	for rows.Next() {
		sp, err := scanSpeed(rows)
		if err != nil {
			return nil, fmt.Errorf("list speed: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ProviderPause is a provider-wide pause.
type ProviderPause struct {
	Reason  string    // "", "rate_limited" or "forbidden"
	Until   time.Time // zero for a pause that lasts until it is cleared
	Attempt int       // consecutive pauses since the last success
}

// Active reports whether the pause blocks requests at now.
func (p ProviderPause) Active(now time.Time) bool {
	if p.Reason == "" {
		return false
	}
	return p.Until.IsZero() || p.Until.After(now)
}

// GetProviderPause returns the pause for a provider; the zero value means none.
func (s *Store) GetProviderPause(ctx context.Context, provider string) (ProviderPause, error) {
	var (
		p     ProviderPause
		until int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT reason, until_ms, attempt FROM provider_pause WHERE provider = ?`, provider).
		Scan(&p.Reason, &until, &p.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderPause{}, nil
	}
	if err != nil {
		return ProviderPause{}, fmt.Errorf("read provider pause: %w", err)
	}
	if until > 0 {
		p.Until = time.UnixMilli(until).UTC()
	}
	return p, nil
}

// SetProviderPause records a pause and counts one more consecutive pause. A
// zero until pauses until ClearProviderPause.
func (s *Store) SetProviderPause(ctx context.Context, provider, reason string, until time.Time) error {
	var u int64
	if !until.IsZero() {
		u = until.UTC().UnixMilli()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO provider_pause (provider, reason, until_ms, attempt) VALUES (?, ?, ?, 1)
		 ON CONFLICT(provider) DO UPDATE SET reason = excluded.reason, until_ms = excluded.until_ms, attempt = attempt + 1`,
		provider, reason, u); err != nil {
		return fmt.Errorf("set provider pause: %w", err)
	}
	return nil
}

// ClearProviderPause removes the pause and failure streak of a provider.
func (s *Store) ClearProviderPause(ctx context.Context, provider string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM provider_pause WHERE provider = ?`, provider); err != nil {
		return fmt.Errorf("clear provider pause: %w", err)
	}
	return nil
}
