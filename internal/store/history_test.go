package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func TestAvailabilityRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id, _ := db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h", Port: 1})

	up := domain.Availability{ProxyID: id, CheckedAt: t0, State: domain.StateUp, ExitIP: "198.51.100.7",
		Endpoint: "ipify", Duration: 1234 * time.Millisecond}
	down := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Minute), State: domain.StateDown, Endpoint: "ipify",
		Duration: 20 * time.Millisecond, Failure: &domain.Failure{Code: "auth_failed", Message: "The proxy rejected the credentials."}}
	for _, a := range []domain.Availability{up, down} {
		if err := db.AddAvailability(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.LatestAvailability(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateDown || got.Failure == nil || got.Failure.Code != "auth_failed" ||
		got.Failure.Message == "" || got.Endpoint != "ipify" || got.Duration != 20*time.Millisecond ||
		!got.CheckedAt.Equal(t0.Add(time.Minute)) || got.ProxyID != id {
		t.Fatalf("latest: %+v failure=%+v", got, got.Failure)
	}

	list, err := db.ListAvailability(ctx, id, time.Time{}, time.Time{}, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if list[0].State != domain.StateDown || list[1].State != domain.StateUp || list[1].Failure != nil ||
		list[1].ExitIP != "198.51.100.7" || list[1].Duration != 1234*time.Millisecond || !list[1].CheckedAt.Equal(t0) {
		t.Fatalf("ordering or fields: %+v", list)
	}
	if !list[1].CheckedAt.Equal(t0) || list[1].CheckedAt.Location() != time.UTC {
		t.Fatalf("timestamps must come back in UTC: %v", list[1].CheckedAt)
	}
}

func TestListAvailabilityFiltersAndLimits(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	a, _ := db.InsertProxy(ctx, domain.Proxy{Name: "a", Protocol: domain.ProtocolHTTP, Host: "a", Port: 1})
	b, _ := db.InsertProxy(ctx, domain.Proxy{Name: "b", Protocol: domain.ProtocolHTTP, Host: "b", Port: 1})
	for i := 0; i < 5; i++ {
		_ = db.AddAvailability(ctx, domain.Availability{ProxyID: a, CheckedAt: t0.Add(time.Duration(i) * time.Hour), State: domain.StateUp})
	}
	_ = db.AddAvailability(ctx, domain.Availability{ProxyID: b, CheckedAt: t0, State: domain.StateDown})

	list, _ := db.ListAvailability(ctx, a, time.Time{}, time.Time{}, 3)
	if len(list) != 3 || !list[0].CheckedAt.Equal(t0.Add(4*time.Hour)) {
		t.Fatalf("limit/newest first: %+v", list)
	}
	list, _ = db.ListAvailability(ctx, a, t0.Add(time.Hour), t0.Add(3*time.Hour), 10)
	if len(list) != 3 {
		t.Fatalf("range [1h,3h] returned %d", len(list))
	}
	list, _ = db.ListAvailability(ctx, b, time.Time{}, time.Time{}, 10)
	if len(list) != 1 || list[0].State != domain.StateDown {
		t.Fatalf("other proxy's rows mixed in: %+v", list)
	}
	if list, err := db.ListAvailability(ctx, a, time.Time{}, time.Time{}, 0); err != nil || len(list) != 0 {
		t.Fatalf("zero limit: %v %v", list, err)
	}
}

func TestAvailabilityErrors(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	if _, err := db.LatestAvailability(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("latest without results: %v", err)
	}
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: 99, CheckedAt: t0, State: domain.StateUp}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("result for a missing proxy: %v", err)
	}
	id, _ := db.InsertProxy(ctx, domain.Proxy{Name: "a", Protocol: domain.ProtocolHTTP, Host: "a", Port: 1})
	if err := db.AddAvailability(ctx, domain.Availability{ProxyID: id, CheckedAt: t0, State: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid state: %v", err)
	}
}
