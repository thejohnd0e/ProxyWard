package store

import (
	"context"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func seedHistory(t *testing.T, db *Store, id domain.ProxyID) {
	t.Helper()
	ctx := context.Background()
	states := []domain.State{domain.StateUp, domain.StateUp, domain.StateDown, domain.StateIndeterminate, domain.StateUp}
	for i, st := range states {
		a := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Duration(i) * time.Hour), State: st, ExitIP: "198.51.100.7",
			Endpoint: "ipify", Duration: time.Duration(100*(i+1)) * time.Millisecond}
		if st == domain.StateDown {
			a.Failure = &domain.Failure{Code: "auth_failed", Message: "m"}
		}
		if err := db.AddAvailability(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		m := float64(10 * (i + 1))
		sp := domain.Speed{ProxyID: id, CheckedAt: t0.Add(time.Duration(i) * 2 * time.Hour), Provider: "cloudflare", Method: "m",
			Download: domain.Transfer{Bytes: 1, Duration: time.Second, Mbps: &m}, Upload: domain.Transfer{Bytes: 1, Duration: time.Second}}
		if err := db.AddSpeed(ctx, sp); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLatestAllAndUptime(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	seedHistory(t, db, ids[0])
	_ = db.AddAvailability(ctx, domain.Availability{ProxyID: ids[1], CheckedAt: t0, State: domain.StateDown})

	latest, err := db.LatestAvailabilityAll(ctx)
	if err != nil || len(latest) != 2 || latest[ids[0]].State != domain.StateUp || !latest[ids[0]].CheckedAt.Equal(t0.Add(4*time.Hour)) ||
		latest[ids[1]].State != domain.StateDown {
		t.Fatalf("%+v %v", latest, err)
	}
	if _, ok := latest[ids[2]]; ok {
		t.Fatal("a proxy without results must be absent, not unknown-with-data")
	}
	speeds, err := db.LatestSpeedAll(ctx)
	if err != nil || len(speeds) != 1 || speeds[ids[0]].Download.Mbps == nil || *speeds[ids[0]].Download.Mbps != 30 || speeds[ids[0]].Upload.Mbps != nil {
		t.Fatalf("%+v %v", speeds, err)
	}

	up, err := db.UptimeSince(ctx, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Three up and one down count; the indeterminate result does not.
	if u := up[ids[0]]; u.Up != 3 || u.Down != 1 || u.Samples() != 4 || u.Percent() == nil || *u.Percent() != 75 {
		t.Fatalf("%+v", u)
	}
	if u := up[ids[1]]; u.Percent() == nil || *u.Percent() != 0 || u.Samples() != 1 {
		t.Fatalf("%+v", u)
	}
	if u := up[ids[2]]; u.Samples() != 0 || u.Percent() != nil {
		t.Fatalf("no samples must give no percentage: %+v", u)
	}
	// Only the window counts.
	recent, _ := db.UptimeSince(ctx, t0.Add(3*time.Hour))
	if u := recent[ids[0]]; u.Up != 1 || u.Down != 0 {
		t.Fatalf("%+v", u)
	}
}

func TestHistoryPagesUseACursor(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	seedHistory(t, db, id)

	var seen []int64
	var cur *HistoryCursor
	for i := 0; i < 10; i++ {
		page, next, err := db.PageAvailability(ctx, id, time.Time{}, time.Time{}, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			seen = append(seen, r.ID)
		}
		if next == nil {
			break
		}
		cur = next
	}
	if len(seen) != 5 {
		t.Fatalf("paging saw %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("not newest first: %v", seen)
		}
	}
	page, next, _ := db.PageAvailability(ctx, id, t0.Add(time.Hour), t0.Add(3*time.Hour), nil, 10)
	if len(page) != 3 || next != nil || page[0].Availability.State != domain.StateIndeterminate || page[2].Availability.State != domain.StateUp {
		t.Fatalf("%+v", page)
	}
	if page[0].Availability.Failure != nil || page[1].Availability.Failure == nil || page[1].Availability.Failure.Code != "auth_failed" {
		t.Fatalf("failures not carried: %+v", page)
	}

	sp, snext, err := db.PageSpeed(ctx, id, time.Time{}, time.Time{}, nil, 2)
	if err != nil || len(sp) != 2 || snext == nil || sp[0].Speed.Download.Mbps == nil || *sp[0].Speed.Download.Mbps != 30 {
		t.Fatalf("%+v %v %v", sp, snext, err)
	}
	rest, snext, _ := db.PageSpeed(ctx, id, time.Time{}, time.Time{}, snext, 2)
	if len(rest) != 1 || snext != nil {
		t.Fatalf("%+v", rest)
	}
	if empty, _, _ := db.PageAvailability(ctx, id, time.Time{}, time.Time{}, nil, 0); len(empty) != 0 {
		t.Fatal("zero limit returned rows")
	}
}

func TestChartBucketsKeepGapsAndCounts(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	seedHistory(t, db, id)

	from, to := t0, t0.Add(5*time.Hour)
	b, err := db.ChartAvailability(ctx, id, from, to, 5)
	if err != nil || len(b) != 5 {
		t.Fatalf("%d buckets: %v", len(b), err)
	}
	// One result per hour: up, up, down, indeterminate, up.
	want := []struct{ up, down, ind int }{{1, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1}, {1, 0, 0}}
	for i, w := range want {
		if b[i].Up != w.up || b[i].Down != w.down || b[i].Indeterminate != w.ind || b[i].Samples != 1 || !b[i].Start.Equal(from.Add(time.Duration(i)*time.Hour)) {
			t.Fatalf("bucket %d: %+v", i, b[i])
		}
	}
	if b[0].AvgDurationMs == nil || *b[0].AvgDurationMs != 100 {
		t.Fatalf("%+v", b[0])
	}

	// Coarser buckets merge results; empty buckets stay present with no values.
	b2, _ := db.ChartAvailability(ctx, id, from, to.Add(5*time.Hour), 5)
	if len(b2) != 5 {
		t.Fatalf("%d", len(b2))
	}
	if b2[0].Samples != 2 || b2[0].Up != 2 || b2[1].Samples != 2 || b2[1].Down != 1 || b2[1].Indeterminate != 1 || b2[2].Samples != 1 {
		t.Fatalf("%+v", b2)
	}
	if b2[4].Samples != 0 || b2[4].AvgDurationMs != nil {
		t.Fatalf("an empty bucket must carry no value: %+v", b2[4])
	}

	sp, err := db.ChartSpeed(ctx, id, from, to, 5)
	if err != nil || len(sp) != 5 {
		t.Fatalf("%v %v", sp, err)
	}
	if sp[0].Download == nil || *sp[0].Download != 10 || sp[1].Samples != 0 || sp[1].Download != nil || sp[2].Download == nil || *sp[2].Download != 20 {
		t.Fatalf("%+v", sp)
	}
	if sp[0].Upload != nil || sp[0].UploadSamples != 0 || sp[0].DownloadSamples != 1 {
		t.Fatalf("a direction without a completed sample must have no value: %+v", sp[0])
	}
	if _, err := db.ChartAvailability(ctx, id, to, from, 5); err == nil {
		t.Fatal("inverted range accepted")
	}
	if _, err := db.ChartAvailability(ctx, id, from, to, 0); err == nil {
		t.Fatal("zero buckets accepted")
	}
	if _, err := db.ChartAvailability(ctx, id, from, to, 501); err == nil {
		t.Fatal("more than 500 buckets accepted")
	}
}
