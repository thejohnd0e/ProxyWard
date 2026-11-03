package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func exportRange() (time.Time, time.Time) { return t0, t0.Add(24 * time.Hour) }

func addExportEvent(t *testing.T, db *Store, proxyID domain.ProxyID, kind string, at time.Time, message string) {
	t.Helper()
	if _, err := db.db.ExecContext(context.Background(),
		`INSERT INTO events (proxy_id, kind, message, created_at) VALUES (?, ?, ?, ?)`,
		int64(proxyID), kind, message, at.UTC().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func mbps(v float64) *float64 { return &v }

func TestExportSelectsProxiesAndInclusiveRange(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	from, to := exportRange()

	// Inside for the selected proxy, including both boundaries.
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: ids[0], CheckedAt: from, State: domain.StateUp, ExitIP: "198.51.100.7", Endpoint: "ipify", Duration: 1500 * time.Microsecond}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: ids[0], CheckedAt: to, State: domain.StateDown, Failure: &domain.Failure{Code: "proxy_tls", Message: "secret failure text"}}); err != nil {
		t.Fatal(err)
	}
	// Outside the range.
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: ids[0], CheckedAt: from.Add(-time.Millisecond), State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: ids[0], CheckedAt: to.Add(time.Millisecond), State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}
	// Another proxy inside the range.
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: ids[1], CheckedAt: from, State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}

	if err := db.AddSpeed(ctx, domain.Speed{ProxyID: ids[0], CheckedAt: from.Add(time.Hour), Provider: "cloudflare", Method: "app-stream-v1",
		Download: domain.Transfer{Bytes: 1000, Duration: 2 * time.Second, Mbps: mbps(4)}, Upload: domain.Transfer{Bytes: 500, Duration: time.Second, Mbps: mbps(4)}, TotalBytes: 1500}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSpeed(ctx, domain.Speed{ProxyID: ids[1], CheckedAt: from.Add(time.Hour), Provider: "cloudflare", Method: "app-stream-v1"}); err != nil {
		t.Fatal(err)
	}

	addExportEvent(t, db, ids[0], "proxy_down", from.Add(2*time.Hour), "went down")
	addExportEvent(t, db, ids[1], "proxy_down", from.Add(2*time.Hour), "went down")
	addExportEvent(t, db, ids[0], "proxy_down", from.Add(48*time.Hour), "too old")

	data, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{ids[0]}})
	if err != nil {
		t.Fatal(err)
	}

	if len(data.Proxies) != 1 || data.Proxies[0].ID != int64(ids[0]) {
		t.Fatalf("proxies: %+v", data.Proxies)
	}
	if len(data.Availability) != 2 {
		t.Fatalf("availability outside the range or proxy leaked in: %+v", data.Availability)
	}
	first := data.Availability[0]
	if !first.At.Equal(from) || first.State != "up" || first.ExitIP != "198.51.100.7" || first.Endpoint != "ipify" || first.DurationMS != 1 {
		t.Fatalf("first availability: %+v", first)
	}
	second := data.Availability[1]
	if !second.At.Equal(to) || second.State != "down" || second.FailureCode != "proxy_tls" {
		t.Fatalf("second availability: %+v", second)
	}
	if len(data.Speed) != 1 || data.Speed[0].ProxyID != int64(ids[0]) {
		t.Fatalf("speed: %+v", data.Speed)
	}
	if d := data.Speed[0].Download; d.Bytes != 1000 || d.DurationMS != 2000 || d.Mbps == nil || *d.Mbps != 4 || d.LowConfidence || d.FailureCode != "" {
		t.Fatalf("download: %+v", d)
	}
	if len(data.Events) != 1 || data.Events[0].Kind != "proxy_down" || !data.Events[0].At.Equal(from.Add(2*time.Hour)) {
		t.Fatalf("events: %+v", data.Events)
	}
}

func TestExportEmptyResultHasEmptyArrays(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()

	data, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"events":[]`, `"availability":[]`, `"speed":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("empty arrays must be [] and not null: %s", raw)
		}
	}
}

func TestExportOrdersByTimeThenID(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()

	for i := 0; i < 5; i++ {
		if err := db.AddAvailability(context.Background(), domain.Availability{ProxyID: id, CheckedAt: from, State: domain.StateUp}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddAvailability(context.Background(), domain.Availability{ProxyID: id, CheckedAt: from.Add(-time.Second), State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}

	data, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Availability) != 5 {
		t.Fatalf("expected the five in-range rows, got %d", len(data.Availability))
	}
	for i := 1; i < len(data.Availability); i++ {
		if !data.Availability[i].At.Equal(data.Availability[i-1].At) {
			t.Fatal("rows with equal timestamps must stay together")
		}
	}
}

func TestExportKeepsPartialSpeedSamplesExplicit(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()
	if err := db.AddSpeed(context.Background(), domain.Speed{ProxyID: id, CheckedAt: from, Provider: "cloudflare", Method: "app-stream-v1",
		Download: domain.Transfer{Bytes: 10, Duration: 3 * time.Second, Failure: &domain.Failure{Code: "download_failed", Message: "secret upload text"}}}); err != nil {
		t.Fatal(err)
	}

	data, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if !strings.Contains(string(raw), `"mbps":null`) {
		t.Fatalf("a missing rate must be null, never zero: %s", raw)
	}
	if !strings.Contains(string(raw), `"failure_code":"download_failed"`) || !strings.Contains(string(raw), `"failure_code":""`) {
		t.Fatalf("failure codes must be explicit: %s", raw)
	}
}

func TestExportExcludesEventsOfDeletedProxies(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	from, to := exportRange()
	addExportEvent(t, db, ids[0], "proxy_down", from, "went down")
	addExportEvent(t, db, ids[1], "proxy_down", from, "went down")
	if err := db.DeleteProxy(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}

	data, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{ids[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Events) != 1 || data.Events[0].ProxyID != int64(ids[0]) {
		t.Fatalf("events of a deleted proxy cannot be attributed: %+v", data.Events)
	}
	// A deleted proxy is no longer selectable: the caller refreshes its list
	// instead of receiving events that can no longer be attributed.
	if _, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{ids[1]}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted proxy: got %v, want ErrNotFound", err)
	}
}

func TestExportRejectsInvalidFilters(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 3)[0]
	from, to := exportRange()
	cases := []struct {
		name string
		f    ExportFilter
		want error
	}{
		{"no proxies", ExportFilter{From: from, To: to}, ErrInvalid},
		{"duplicate proxies", ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id, id}}, ErrInvalid},
		{"negative id", ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{-1}}, ErrInvalid},
		{"reversed range", ExportFilter{From: to, To: from, ProxyIDs: []domain.ProxyID{id}}, ErrInvalid},
		{"empty range", ExportFilter{From: from, To: from, ProxyIDs: []domain.ProxyID{id}}, ErrInvalid},
		{"unknown proxy", ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{9999}}, ErrNotFound},
	}
	for _, c := range cases {
		if _, err := db.Export(context.Background(), c.f); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestExportRejectsMoreThanOneHundredProxies(t *testing.T) {
	db := openMigrated(t)
	from, to := exportRange()
	ids := make([]domain.ProxyID, 101)
	for i := range ids {
		ids[i] = domain.ProxyID(i + 1)
	}
	if _, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: ids}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

func TestExportRecordLimit(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()
	insert := func(n int) {
		if _, err := db.db.ExecContext(context.Background(),
			`INSERT INTO availability_results (proxy_id, checked_at, state)
			 SELECT ?, ?, 'up' FROM (WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < ?) SELECT x FROM c)`,
			int64(id), from.UTC().UnixMilli(), n); err != nil {
			t.Fatal(err)
		}
	}

	insert(ExportMaxRecords)
	if _, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}}); err != nil {
		t.Fatalf("exactly the limit must be exported: %v", err)
	}

	insert(1)
	if _, err := db.Export(context.Background(), ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}}); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("got %v, want ErrExportTooLarge", err)
	}
}

func TestExportOmitsSecretsAndFreeText(t *testing.T) {
	ctx := context.Background()
	db, _ := openWithSecrets(t)
	const sentinel = "SECRET-SENTINEL"
	id, err := db.InsertProxy(ctx, withAuth(httpProxy("secret.lan", 8080), "user-"+sentinel, "pass-"+sentinel))
	if err != nil {
		t.Fatal(err)
	}
	from, to := exportRange()
	addExportEvent(t, db, id, "proxy_down", from, "message "+sentinel)
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: from, State: domain.StateDown, Failure: &domain.Failure{Code: "proxy_tls", Message: sentinel}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSpeed(ctx, domain.Speed{ProxyID: id, CheckedAt: from, Provider: "p", Method: "m", Upload: domain.Transfer{Failure: &domain.Failure{Code: "upload_failed", Message: sentinel}}}); err != nil {
		t.Fatal(err)
	}

	data, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("the export leaked a credential or free-form text: %s", raw)
	}
}

func TestExportHonoursCancellation(t *testing.T) {
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestExportRunsWhileOtherWritersWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	from, to := exportRange()
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: from, State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			a := domain.Availability{ProxyID: id, CheckedAt: from.Add(time.Duration(i) * time.Millisecond), State: domain.StateUp}
			if err := db.AddAvailability(ctx, a); err != nil {
				return
			}
		}
	}()

	for i := 0; i < 20; i++ {
		data, err := db.Export(ctx, ExportFilter{From: from, To: to, ProxyIDs: []domain.ProxyID{id}})
		if err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		seen := map[int64]bool{}
		for _, a := range data.Availability {
			if seen[a.ID] {
				cancel()
				<-done
				t.Fatalf("duplicate row in one export: %d", a.ID)
			}
			seen[a.ID] = true
		}
	}
	cancel()
	<-done
}
