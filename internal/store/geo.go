package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// ErrSecretsUnavailable is returned when a secret must be stored but no
// encryption key is configured.
var ErrSecretsUnavailable = errors.New("secret storage is not available")

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
