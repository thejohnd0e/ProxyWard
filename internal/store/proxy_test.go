package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
)

func openWithSecrets(t *testing.T) (*Store, *secrets.Cipher) {
	t.Helper()
	db := openMigrated(t)
	c, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db.UseSecrets(c)
	return db, c
}

func httpProxy(host string, port uint16) domain.Proxy {
	return domain.Proxy{Name: host, Protocol: domain.ProtocolHTTP, Host: host, Port: port}
}

func withAuth(p domain.Proxy, user, pass string) domain.Proxy {
	p.Auth = &domain.Credentials{Username: user, Password: pass}
	return p
}

func TestAuthenticatedProxyIsEncryptedAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	id, err := db.InsertProxy(ctx, withAuth(httpProxy("10.0.0.1", 3128), "alice", "S3cret-Pass"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetProxy(ctx, id)
	if err != nil || got.Auth == nil || got.Auth.Username != "alice" || got.Auth.Password != "S3cret-Pass" {
		t.Fatalf("round trip: %+v %v", got.Auth, err)
	}
	var enc []byte
	var digest string
	if err := db.db.QueryRow(`SELECT password_enc, credential_digest FROM proxies WHERE id = ?`, int64(id)).Scan(&enc, &digest); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("S3cret-Pass")) || len(enc) == 0 {
		t.Fatal("password is not encrypted at rest")
	}
	if digest == "" || digest == "S3cret-Pass" {
		t.Fatalf("bad digest %q", digest)
	}
	list, err := db.ListProxies(ctx)
	if err != nil || len(list) != 1 || list[0].Auth == nil || list[0].Auth.Password != "S3cret-Pass" {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestAuthenticatedProxyNeedsSecretsAndUsername(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.InsertProxy(ctx, withAuth(httpProxy("h", 1), "u", "p")); !errors.Is(err, ErrAuthUnsupported) {
		t.Fatalf("without sealer: %v", err)
	}
	db2, _ := openWithSecrets(t)
	if _, err := db2.InsertProxy(ctx, withAuth(httpProxy("h", 1), "", "p")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty username: %v", err)
	}
	if _, err := db2.InsertProxy(ctx, withAuth(httpProxy("h", 1), "u", "")); err != nil {
		t.Fatalf("empty password must be allowed: %v", err)
	}
}

func TestDuplicateIdentityIncludesCredentials(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	base := httpProxy("Example.com", 8080)
	if _, err := db.InsertProxy(ctx, withAuth(base, "u", "p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertProxy(ctx, withAuth(base, "u", "p2")); err != nil {
		t.Fatalf("different password at one endpoint rejected: %v", err)
	}
	if _, err := db.InsertProxy(ctx, withAuth(base, "v", "p1")); err != nil {
		t.Fatalf("different username rejected: %v", err)
	}
	if _, err := db.InsertProxy(ctx, base); err != nil {
		t.Fatalf("unauthenticated twin rejected: %v", err)
	}
	same := withAuth(base, "u", "p1")
	same.Host = "example.COM"
	if _, err := db.InsertProxy(ctx, same); !errors.Is(err, ErrConflict) {
		t.Fatalf("exact duplicate: %v", err)
	}
	for name, p := range map[string]domain.Proxy{
		"identical":    withAuth(base, "u", "p1"),
		"other host":   withAuth(httpProxy("other", 8080), "u", "p1"),
		"other pass":   withAuth(base, "u", "zzz"),
		"unauth twin":  base,
		"unauth other": httpProxy("nope", 1),
	} {
		ok, err := db.ProxyExists(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		want := name == "identical" || name == "unauth twin"
		if ok != want {
			t.Errorf("ProxyExists(%s) = %v, want %v", name, ok, want)
		}
	}
}

func TestImportProxiesIsAtomic(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	batch := []domain.Proxy{httpProxy("a", 1), httpProxy("b", 1), httpProxy("a", 1)}
	if _, err := db.ImportProxies(ctx, batch); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate inside batch: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxies`); n != 0 {
		t.Fatalf("partial import left %d rows", n)
	}
	invalid := []domain.Proxy{httpProxy("a", 1), {Protocol: "ftp", Host: "x", Port: 1}}
	if _, err := db.ImportProxies(ctx, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid item: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxies`); n != 0 {
		t.Fatalf("partial import left %d rows", n)
	}
	ids, err := db.ImportProxies(ctx, batch[:2])
	if err != nil || len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("ids = %v err = %v", ids, err)
	}
	if ids, err := db.ImportProxies(ctx, nil); err != nil || len(ids) != 0 {
		t.Fatalf("empty import: %v %v", ids, err)
	}
}

func fill(t *testing.T, db *Store, n int) {
	t.Helper()
	batch := make([]domain.Proxy, n)
	for i := range batch {
		batch[i] = httpProxy(fmt.Sprintf("seed-%d.lan", i), 80)
	}
	if _, err := db.ImportProxies(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestProxyLimitIs100(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	fill(t, db, 99)
	two := []domain.Proxy{httpProxy("x1", 1), httpProxy("x2", 1)}
	if _, err := db.ImportProxies(ctx, two); !errors.Is(err, ErrLimit) {
		t.Fatalf("99 + 2: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxies`); n != 99 {
		t.Fatalf("rejected import changed the count to %d", n)
	}
	if _, err := db.InsertProxy(ctx, httpProxy("x1", 1)); err != nil {
		t.Fatalf("100th proxy: %v", err)
	}
	if _, err := db.InsertProxy(ctx, httpProxy("x2", 1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("101st proxy: %v", err)
	}
	if n, err := db.CountProxies(ctx); err != nil || n != 100 {
		t.Fatalf("count = %d err = %v", n, err)
	}
}

func TestConcurrentImportsRespectLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	const workers, per = 10, 20
	var wg sync.WaitGroup
	results := make([]error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch := make([]domain.Proxy, per)
			for i := range batch {
				batch[i] = httpProxy(fmt.Sprintf("w%d-%d.lan", w, i), 80)
			}
			_, results[w] = db.ImportProxies(ctx, batch)
		}()
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrLimit):
			limited++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 5 || limited != 5 {
		t.Fatalf("ok=%d limited=%d, want 5/5", ok, limited)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxies`); n != 100 {
		t.Fatalf("rows = %d, want exactly 100 (no partial batches)", n)
	}
}

func TestUpdateProxyCredentialSemantics(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	str := func(s string) *string { return &s }
	id, err := db.InsertProxy(ctx, withAuth(httpProxy("h.lan", 3128), "alice", "pw1"))
	if err != nil {
		t.Fatal(err)
	}
	cur := func() domain.Proxy {
		p, err := db.GetProxy(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := cur()
	p.Name = "renamed"
	if err := db.UpdateProxy(ctx, id, p, CredentialUpdate{}); err != nil {
		t.Fatal(err)
	}
	if got := cur(); got.Name != "renamed" || got.Auth == nil || got.Auth.Password != "pw1" || got.Auth.Username != "alice" {
		t.Fatalf("omitted credentials must be kept: %+v", got)
	}

	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Username: str("bob")}); err != nil {
		t.Fatal(err)
	}
	if got := cur(); got.Auth.Username != "bob" || got.Auth.Password != "pw1" {
		t.Fatalf("username-only update: %+v", got.Auth)
	}
	// The fingerprint follows the stored credentials: bob/pw1 is now taken.
	if ok, _ := db.ProxyExists(ctx, withAuth(httpProxy("h.lan", 3128), "bob", "pw1")); !ok {
		t.Fatal("fingerprint not updated after username change")
	}
	if ok, _ := db.ProxyExists(ctx, withAuth(httpProxy("h.lan", 3128), "alice", "pw1")); ok {
		t.Fatal("old credentials still match")
	}

	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Password: str("")}); err != nil {
		t.Fatal(err)
	}
	if got := cur(); got.Auth == nil || got.Auth.Password != "" || got.Auth.Username != "bob" {
		t.Fatalf("empty password must replace: %+v", got.Auth)
	}

	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Remove: true}); err != nil {
		t.Fatal(err)
	}
	if got := cur(); got.Auth != nil {
		t.Fatalf("auth not removed: %+v", got.Auth)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxies WHERE password_enc IS NOT NULL OR username <> '' OR credential_digest <> ''`); n != 0 {
		t.Fatal("credential columns not cleared")
	}

	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Password: str("x")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("enabling auth without username: %v", err)
	}
	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Username: str("carol"), Password: str("pw3")}); err != nil {
		t.Fatal(err)
	}
	if got := cur(); got.Auth == nil || got.Auth.Username != "carol" || got.Auth.Password != "pw3" {
		t.Fatalf("enabling auth: %+v", got.Auth)
	}
	if err := db.UpdateProxy(ctx, id, cur(), CredentialUpdate{Username: str("")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty username: %v", err)
	}
}

func TestUpdateProxyConflictsNotFoundAndEndpoint(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	a, _ := db.InsertProxy(ctx, httpProxy("a.lan", 1))
	b, _ := db.InsertProxy(ctx, httpProxy("b.lan", 1))

	p, _ := db.GetProxy(ctx, b)
	p.Host = "A.lan"
	p.Port = 1
	if err := db.UpdateProxy(ctx, b, p, CredentialUpdate{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("update into a duplicate: %v", err)
	}
	p.Host, p.Protocol, p.Port = "c.lan", domain.ProtocolSOCKS5, 1080
	p.Tags = []string{"x", "y"}
	if err := db.UpdateProxy(ctx, b, p, CredentialUpdate{}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetProxy(ctx, b)
	if got.Host != "c.lan" || got.Protocol != domain.ProtocolSOCKS5 || got.Port != 1080 || len(got.Tags) != 2 {
		t.Fatalf("endpoint update: %+v", got)
	}
	if err := db.UpdateProxy(ctx, 9999, p, CredentialUpdate{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: %v", err)
	}
	bad := p
	bad.Port = 0
	if err := db.UpdateProxy(ctx, a, bad, CredentialUpdate{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid endpoint: %v", err)
	}
}

func TestScheduleOverridesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	p := httpProxy("s.lan", 1)
	p.AvailabilitySchedule = &domain.Schedule{Enabled: true, Interval: 5 * time.Minute}
	p.SpeedSchedule = &domain.Schedule{Enabled: false, Interval: 6 * time.Hour}
	id, err := db.InsertProxy(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetProxy(ctx, id)
	if got.AvailabilitySchedule == nil || !got.AvailabilitySchedule.Enabled || got.AvailabilitySchedule.Interval != 5*time.Minute ||
		got.SpeedSchedule == nil || got.SpeedSchedule.Enabled || got.SpeedSchedule.Interval != 6*time.Hour {
		t.Fatalf("overrides: %+v %+v", got.AvailabilitySchedule, got.SpeedSchedule)
	}
	got.AvailabilitySchedule = nil // back to inherit
	if err := db.UpdateProxy(ctx, id, got, CredentialUpdate{}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetProxy(ctx, id)
	if got.AvailabilitySchedule != nil || got.SpeedSchedule == nil {
		t.Fatalf("clearing one override: %+v %+v", got.AvailabilitySchedule, got.SpeedSchedule)
	}
	for name, iv := range map[string]time.Duration{"short": 30 * time.Second, "long": 10081 * time.Minute, "zero": 0} {
		bad := httpProxy("bad-"+name, 1)
		bad.SpeedSchedule = &domain.Schedule{Enabled: true, Interval: iv}
		if _, err := db.InsertProxy(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s interval accepted: %v", name, err)
		}
	}
}

func TestDeleteProxyRemovesAssociatedData(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	id, _ := db.InsertProxy(ctx, httpProxy("d.lan", 1))
	other, _ := db.InsertProxy(ctx, httpProxy("keep.lan", 1))
	for _, q := range []string{
		`INSERT INTO jobs (proxy_id, kind, status, source, due_at, created_at) VALUES (%d, 'speed', 'pending', 'manual', 0, 0)`,
		`INSERT INTO availability_results (proxy_id, checked_at, state) VALUES (%d, 1, 'up')`,
		`INSERT INTO speed_results (proxy_id, checked_at, provider, method) VALUES (%d, 1, 'p', 'm')`,
		`INSERT INTO incidents (proxy_id, started_at) VALUES (%d, 1)`,
		`INSERT INTO events (proxy_id, kind, message, created_at) VALUES (%d, 'k', 'm', 1)`,
	} {
		if _, err := db.db.Exec(fmt.Sprintf(q, id)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(fmt.Sprintf(q, other)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.DeleteProxy(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"jobs", "availability_results", "speed_results", "incidents"} {
		if n := countRows(t, db, `SELECT COUNT(*) FROM `+table+` WHERE proxy_id = ?`, int64(id)); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
		if n := countRows(t, db, `SELECT COUNT(*) FROM `+table+` WHERE proxy_id = ?`, int64(other)); n != 1 {
			t.Errorf("%s: other proxy's rows = %d", table, n)
		}
	}
	// Events are history: they stay, detached from the deleted proxy.
	if n := countRows(t, db, `SELECT COUNT(*) FROM events WHERE proxy_id IS NULL`); n != 1 {
		t.Errorf("detached events = %d", n)
	}
	if err := db.DeleteProxy(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := db.GetProxy(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestReadingWithWrongKeyFailsCleanly(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	id, _ := db.InsertProxy(ctx, withAuth(httpProxy("k.lan", 1), "u", "p"))
	other, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db.UseSecrets(other)
	if _, err := db.GetProxy(ctx, id); err == nil {
		t.Fatal("decrypting with a foreign key succeeded")
	}
}
