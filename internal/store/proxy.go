package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"proxy-checker/internal/domain"
)

// ErrLimit is returned when a write would exceed domain.MaxProxies.
var ErrLimit = errors.New("proxy limit reached")

// Sealer encrypts proxy passwords and fingerprints credentials. It is
// implemented by *secrets.Cipher.
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
	Fingerprint(username, password string) string
}

type sealerBox struct{ Sealer }

// UseSecrets enables storing and reading proxy credentials.
func (s *Store) UseSecrets(c Sealer) { s.sealer.Store(&sealerBox{c}) }

func (s *Store) secretsSealer() Sealer {
	if b := s.sealer.Load(); b != nil {
		return b.Sealer
	}
	return nil
}

// CredentialUpdate describes how UpdateProxy changes credentials. The zero
// value keeps them unchanged. A non-nil Password, including an empty one,
// replaces the stored password.
type CredentialUpdate struct {
	Remove   bool    // disable authentication and delete stored credentials
	Username *string // nil keeps the stored username
	Password *string // nil keeps the stored password
}

// InsertProxy stores one proxy.
func (s *Store) InsertProxy(ctx context.Context, p domain.Proxy) (domain.ProxyID, error) {
	ids, err := s.ImportProxies(ctx, []domain.Proxy{p})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// ImportProxies stores all proxies in one transaction or none. It fails with
// ErrLimit when the total would exceed domain.MaxProxies and ErrConflict when
// any item duplicates an existing or earlier item.
func (s *Store) ImportProxies(ctx context.Context, proxies []domain.Proxy) ([]domain.ProxyID, error) {
	if len(proxies) == 0 {
		return nil, nil
	}
	rows := make([]proxyRow, len(proxies))
	for i, p := range proxies {
		r, err := s.prepare(p)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows[i] = r
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("insert proxies: %w", err)
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies`).Scan(&existing); err != nil {
		return nil, fmt.Errorf("insert proxies: %w", err)
	}
	if existing+len(rows) > domain.MaxProxies {
		return nil, fmt.Errorf("%w: %d stored, %d requested, maximum %d", ErrLimit, existing, len(rows), domain.MaxProxies)
	}
	now := time.Now().UTC().UnixMilli()
	ids := make([]domain.ProxyID, len(rows))
	for i, r := range rows {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO proxies (name, protocol, host, port, tags, auth_enabled, username, password_enc, credential_digest,
			    availability_enabled, availability_interval_s, speed_enabled, speed_interval_s, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.name, r.protocol, r.host, r.port, r.tags, r.authEnabled, r.username, r.passwordEnc, r.digest,
			r.availEnabled, r.availInterval, r.speedEnabled, r.speedInterval, now, now)
		if err != nil {
			if isConstraint(err) {
				return nil, fmt.Errorf("%w: item %d already exists", ErrConflict, i)
			}
			return nil, fmt.Errorf("insert proxies: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("insert proxies: %w", err)
		}
		ids[i] = domain.ProxyID(id)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("insert proxies: %w", err)
	}
	return ids, nil
}

// proxyRow holds the column values of a proxy ready for SQL.
type proxyRow struct {
	name, protocol, host string
	port                 int
	tags                 string
	authEnabled          int
	username             string
	passwordEnc          []byte
	digest               string
	availEnabled         any
	availInterval        any
	speedEnabled         any
	speedInterval        any
}

func (s *Store) prepare(p domain.Proxy) (proxyRow, error) {
	if !p.Protocol.Valid() || strings.TrimSpace(p.Host) == "" || p.Port == 0 {
		return proxyRow{}, fmt.Errorf("%w: protocol, host and port are required", ErrInvalid)
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return proxyRow{}, fmt.Errorf("encode tags: %w", err)
	}
	r := proxyRow{
		name: p.Name, protocol: string(p.Protocol), host: p.Host, port: int(p.Port), tags: string(tagsJSON),
	}
	if r.availEnabled, r.availInterval, err = scheduleColumns(p.AvailabilitySchedule); err != nil {
		return proxyRow{}, err
	}
	if r.speedEnabled, r.speedInterval, err = scheduleColumns(p.SpeedSchedule); err != nil {
		return proxyRow{}, err
	}
	if p.Auth != nil {
		if p.Auth.Username == "" {
			return proxyRow{}, fmt.Errorf("%w: username is required when authentication is enabled", ErrInvalid)
		}
		if err := s.fillCredentials(&r, p.Auth.Username, p.Auth.Password); err != nil {
			return proxyRow{}, err
		}
	}
	return r, nil
}

func (s *Store) fillCredentials(r *proxyRow, username, password string) error {
	sealer := s.secretsSealer()
	if sealer == nil {
		return ErrAuthUnsupported
	}
	enc, err := sealer.Seal([]byte(password))
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}
	r.authEnabled, r.username, r.passwordEnc = 1, username, enc
	r.digest = sealer.Fingerprint(username, password)
	return nil
}

func scheduleColumns(sch *domain.Schedule) (enabled, interval any, err error) {
	if sch == nil {
		return nil, nil, nil
	}
	if sch.Interval < domain.MinInterval || sch.Interval > domain.MaxInterval {
		return nil, nil, fmt.Errorf("%w: schedule interval must be between 1 and 10080 minutes", ErrInvalid)
	}
	e := 0
	if sch.Enabled {
		e = 1
	}
	return e, int64(sch.Interval / time.Second), nil
}

const proxyColumns = `id, name, protocol, host, port, tags, auth_enabled, username, password_enc,
	availability_enabled, availability_interval_s, speed_enabled, speed_interval_s`

type rowScanner interface{ Scan(dest ...any) error }

func (s *Store) scanProxy(r rowScanner) (domain.Proxy, error) {
	var (
		p                      domain.Proxy
		proto, tagsJSON, user  string
		port, authOn           int
		enc                    []byte
		availOn, speedOn       sql.NullInt64
		availIntvl, speedIntvl sql.NullInt64
	)
	if err := r.Scan(&p.ID, &p.Name, &proto, &p.Host, &port, &tagsJSON, &authOn, &user, &enc,
		&availOn, &availIntvl, &speedOn, &speedIntvl); err != nil {
		return domain.Proxy{}, err
	}
	p.Protocol = domain.Protocol(proto)
	p.Port = uint16(port)
	if err := json.Unmarshal([]byte(tagsJSON), &p.Tags); err != nil {
		return domain.Proxy{}, fmt.Errorf("decode tags: %w", err)
	}
	if availOn.Valid {
		p.AvailabilitySchedule = &domain.Schedule{Enabled: availOn.Int64 == 1, Interval: time.Duration(availIntvl.Int64) * time.Second}
	}
	if speedOn.Valid {
		p.SpeedSchedule = &domain.Schedule{Enabled: speedOn.Int64 == 1, Interval: time.Duration(speedIntvl.Int64) * time.Second}
	}
	if authOn == 1 {
		sealer := s.secretsSealer()
		if sealer == nil {
			return domain.Proxy{}, ErrAuthUnsupported
		}
		plain, err := sealer.Open(enc)
		if err != nil {
			return domain.Proxy{}, fmt.Errorf("decrypt proxy password: %w", err)
		}
		p.Auth = &domain.Credentials{Username: user, Password: string(plain)}
	}
	return p, nil
}

// GetProxy returns one proxy with decrypted credentials, or ErrNotFound.
func (s *Store) GetProxy(ctx context.Context, id domain.ProxyID) (domain.Proxy, error) {
	p, err := s.scanProxy(s.db.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = ?`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Proxy{}, ErrNotFound
	}
	if err != nil {
		return domain.Proxy{}, fmt.Errorf("get proxy: %w", err)
	}
	return p, nil
}

// ListProxies returns all proxies ordered by ID, with decrypted credentials.
func (s *Store) ListProxies(ctx context.Context) ([]domain.Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+proxyColumns+` FROM proxies ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	defer rows.Close()
	var out []domain.Proxy
	for rows.Next() {
		p, err := s.scanProxy(rows)
		if err != nil {
			return nil, fmt.Errorf("list proxies: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountProxies returns the number of stored proxies.
func (s *Store) CountProxies(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count proxies: %w", err)
	}
	return n, nil
}

// ProxyExists reports whether a proxy with the same identity (protocol,
// host, port and credentials) is stored.
func (s *Store) ProxyExists(ctx context.Context, p domain.Proxy) (bool, error) {
	user, digest := "", ""
	if p.Auth != nil {
		sealer := s.secretsSealer()
		if sealer == nil {
			return false, ErrAuthUnsupported
		}
		user, digest = p.Auth.Username, sealer.Fingerprint(p.Auth.Username, p.Auth.Password)
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proxies WHERE protocol = ? AND lower(host) = lower(?) AND port = ? AND username = ? AND credential_digest = ?`,
		string(p.Protocol), p.Host, int(p.Port), user, digest).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check proxy: %w", err)
	}
	return n > 0, nil
}

// UpdateProxy replaces a proxy's endpoint, name, tags and schedule overrides
// with p's values and applies cu to its credentials. Per-field omission
// semantics for the API are resolved by the caller before this call.
func (s *Store) UpdateProxy(ctx context.Context, id domain.ProxyID, p domain.Proxy, cu CredentialUpdate) error {
	p.Auth = nil
	r, err := s.prepare(p)
	if err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("update proxy: %w", err)
	}
	defer tx.Rollback()

	var (
		authOn  int
		oldUser string
		oldEnc  []byte
	)
	err = tx.QueryRowContext(ctx, `SELECT auth_enabled, username, password_enc FROM proxies WHERE id = ?`, int64(id)).
		Scan(&authOn, &oldUser, &oldEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("update proxy: %w", err)
	}

	switch {
	case cu.Remove:
		// Leave r without credentials.
	case authOn == 0 && cu.Username == nil && cu.Password == nil:
		// Still unauthenticated.
	default:
		user, pass, ok, err := s.mergeCredentials(authOn == 1, oldUser, oldEnc, cu)
		if err != nil {
			return err
		}
		if ok {
			if err := s.fillCredentials(&r, user, pass); err != nil {
				return err
			}
		}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE proxies SET name = ?, protocol = ?, host = ?, port = ?, tags = ?, auth_enabled = ?, username = ?,
		    password_enc = ?, credential_digest = ?, availability_enabled = ?, availability_interval_s = ?,
		    speed_enabled = ?, speed_interval_s = ?, updated_at = ? WHERE id = ?`,
		r.name, r.protocol, r.host, r.port, r.tags, r.authEnabled, r.username, r.passwordEnc, r.digest,
		r.availEnabled, r.availInterval, r.speedEnabled, r.speedInterval, time.Now().UTC().UnixMilli(), int64(id))
	if err != nil {
		if isConstraint(err) {
			return fmt.Errorf("%w: proxy already exists", ErrConflict)
		}
		return fmt.Errorf("update proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// mergeCredentials resolves the credentials after applying cu to the stored
// ones. ok is false when the result has no authentication.
func (s *Store) mergeCredentials(hadAuth bool, oldUser string, oldEnc []byte, cu CredentialUpdate) (user, pass string, ok bool, err error) {
	if hadAuth {
		user = oldUser
		if cu.Password == nil {
			sealer := s.secretsSealer()
			if sealer == nil {
				return "", "", false, ErrAuthUnsupported
			}
			plain, err := sealer.Open(oldEnc)
			if err != nil {
				return "", "", false, fmt.Errorf("decrypt proxy password: %w", err)
			}
			pass = string(plain)
		}
	}
	if cu.Username != nil {
		user = *cu.Username
	}
	if cu.Password != nil {
		pass = *cu.Password
	}
	if user == "" {
		return "", "", false, fmt.Errorf("%w: username is required when authentication is enabled", ErrInvalid)
	}
	return user, pass, true, nil
}

// DeleteProxy removes a proxy together with its jobs, results and incidents.
// Events are kept and detached. It returns ErrNotFound for unknown IDs.
func (s *Store) DeleteProxy(ctx context.Context, id domain.ProxyID) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, int64(id))
	if err != nil {
		return fmt.Errorf("delete proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func isConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}
