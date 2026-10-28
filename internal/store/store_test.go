package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func TestProxySurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "local", Protocol: "http", Host: "127.0.0.1", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.GetProxy(ctx, id)
	if err != nil || p.Host != "127.0.0.1" {
		t.Fatalf("host=%s err=%v", p.Host, err)
	}
}

func TestMonitoringPauseSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "paused", Protocol: "http", Host: "pause.lan", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMonitoringPaused(ctx, id, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.GetProxy(ctx, id)
	if err != nil || !p.MonitoringPaused {
		t.Fatalf("pause lost after reopen: %+v %v", p, err)
	}
}
