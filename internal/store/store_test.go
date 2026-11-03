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

func TestJobCancellationIntentSurvivesReopenAndShutdownRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertProxy(ctx, domain.Proxy{Name: "cancel", Protocol: "http", Host: "cancel.lan", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, time.Now(), time.Now())
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, time.Now().Add(time.Second))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	requestedAt := time.Now().UTC().Truncate(time.Millisecond)
	requested, err := db.RequestJobCancel(ctx, job.ID, requestedAt)
	if err != nil || requested.CancelRequestedAt == nil || !requested.CancelRequestedAt.Equal(requestedAt) {
		t.Fatalf("cancel intent: %+v %v", requested, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecoverInterrupted(ctx, requestedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.GetJob(ctx, job.ID)
	if err != nil || recovered.Status != domain.JobInterrupted || recovered.CancelRequestedAt == nil || !recovered.CancelRequestedAt.Equal(requestedAt) {
		t.Fatalf("shutdown recovery lost cancellation or changed terminal meaning: %+v %v", recovered, err)
	}
}
