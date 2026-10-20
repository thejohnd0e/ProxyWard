package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"proxy-checker/internal/domain"
)

func f64(v float64) *float64 { return &v }

func TestSpeedRoundTripKeepsMissingValuesMissing(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, _ := db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h", Port: 1})
	in := domain.Speed{
		ProxyID: id, CheckedAt: t0, Provider: "cloudflare", Method: "stream-v1",
		Download: domain.Transfer{Bytes: 8 << 20, Duration: 2 * time.Second, Mbps: f64(33.554432)},
		Upload: domain.Transfer{Bytes: 100, Duration: 40 * time.Millisecond, LowConfidence: true,
			Failure: &domain.Failure{Code: "invalid_response", Message: "The response was not valid."}},
		TotalBytes: 9<<20 + 500,
	}
	if err := db.AddSpeed(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := db.LatestSpeed(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "cloudflare" || got.Method != "stream-v1" || got.TotalBytes != in.TotalBytes || !got.CheckedAt.Equal(t0) {
		t.Fatalf("%+v", got)
	}
	d, u := got.Download, got.Upload
	if d.Mbps == nil || *d.Mbps != 33.554432 || d.Bytes != 8<<20 || d.Duration != 2*time.Second || d.LowConfidence || d.Failure != nil {
		t.Fatalf("download %+v", d)
	}
	if u.Mbps != nil {
		t.Fatalf("a failed direction must have no Mbps (not zero): %v", *u.Mbps)
	}
	if !u.LowConfidence || u.Failure == nil || u.Failure.Code != "invalid_response" || u.Bytes != 100 || u.Duration != 40*time.Millisecond {
		t.Fatalf("upload %+v", u)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM speed_results WHERE upload_mbps IS NULL`); n != 1 {
		t.Fatal("missing Mbps must be stored as NULL")
	}
}

func TestListSpeedOrderLimitAndRange(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, _ := db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h", Port: 1})
	for i := 0; i < 4; i++ {
		_ = db.AddSpeed(ctx, domain.Speed{ProxyID: id, CheckedAt: t0.Add(time.Duration(i) * time.Hour), Provider: "p", Method: "m"})
	}
	list, err := db.ListSpeed(ctx, id, time.Time{}, time.Time{}, 3)
	if err != nil || len(list) != 3 || !list[0].CheckedAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("%+v %v", list, err)
	}
	list, _ = db.ListSpeed(ctx, id, t0.Add(time.Hour), t0.Add(2*time.Hour), 10)
	if len(list) != 2 {
		t.Fatalf("range returned %d", len(list))
	}
	if _, err := db.LatestSpeed(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest for an unknown proxy: %v", err)
	}
	if err := db.AddSpeed(ctx, domain.Speed{ProxyID: 999, CheckedAt: t0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("result for a missing proxy: %v", err)
	}
}

func TestProviderPauseLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	p, err := db.GetProviderPause(ctx, "cloudflare")
	if err != nil || p.Reason != "" || p.Active(now) {
		t.Fatalf("empty: %+v %v", p, err)
	}
	if err := db.SetProviderPause(ctx, "cloudflare", "rate_limited", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, _ = db.GetProviderPause(ctx, "cloudflare")
	if p.Reason != "rate_limited" || !p.Active(now) || p.Active(now.Add(time.Minute)) || p.Attempt != 1 || !p.Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("%+v", p)
	}
	_ = db.SetProviderPause(ctx, "cloudflare", "rate_limited", now.Add(2*time.Minute))
	if p, _ := db.GetProviderPause(ctx, "cloudflare"); p.Attempt != 2 {
		t.Fatalf("attempt = %d", p.Attempt)
	}
	// Other providers are independent.
	if p, _ := db.GetProviderPause(ctx, "other"); p.Reason != "" {
		t.Fatalf("pause leaked to another provider: %+v", p)
	}
	// A forbidden pause has no end until it is cleared.
	_ = db.SetProviderPause(ctx, "cloudflare", "forbidden", time.Time{})
	p, _ = db.GetProviderPause(ctx, "cloudflare")
	if !p.Active(now.Add(1000*time.Hour)) || p.Reason != "forbidden" {
		t.Fatalf("%+v", p)
	}
	if err := db.ClearProviderPause(ctx, "cloudflare"); err != nil {
		t.Fatal(err)
	}
	p, _ = db.GetProviderPause(ctx, "cloudflare")
	if p.Active(now) || p.Attempt != 0 || p.Reason != "" {
		t.Fatalf("after clear: %+v", p)
	}
	if err := db.ClearProviderPause(ctx, "never-set"); err != nil {
		t.Fatalf("clearing an unknown pause: %v", err)
	}
}
