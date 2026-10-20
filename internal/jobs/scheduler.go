// Package jobs queues, schedules and runs availability and speed checks.
package jobs

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

const (
	// maxJitter bounds the random delay that spreads work out after downtime
	// or when a schedule is first enabled.
	maxJitter = 30 * time.Second
	// overdueGrace is how late a run may be before it counts as overdue
	// (downtime) rather than ordinary tick latency.
	overdueGrace = time.Minute
)

var kinds = []domain.CheckKind{domain.CheckAvailability, domain.CheckSpeed}

// Scheduler turns schedules into pending jobs. It holds no state of its own:
// next-run times are persisted per proxy and kind.
type Scheduler struct {
	Store *store.Store
	Clock domain.Clock
	// Jitter returns a delay in [0, 30s). Tests replace it.
	Jitter func() time.Duration
	// Wake is called after jobs were queued; may be nil.
	Wake func()
}

// NewScheduler returns a Scheduler with random jitter.
func NewScheduler(st *store.Store, clock domain.Clock) *Scheduler {
	return &Scheduler{Store: st, Clock: clock, Jitter: func() time.Duration {
		return time.Duration(rand.Int64N(int64(maxJitter)))
	}}
}

func scheduleOf(st store.ScheduleState, kind domain.CheckKind) *domain.Schedule {
	if kind == domain.CheckSpeed {
		return st.Speed
	}
	return st.Availability
}

func nextOf(st store.ScheduleState, kind domain.CheckKind) *time.Time {
	if kind == domain.CheckSpeed {
		return st.NextSpeed
	}
	return st.NextAvailability
}

func globalOf(s domain.Settings, kind domain.CheckKind) domain.Schedule {
	if kind == domain.CheckSpeed {
		return s.Speed
	}
	return s.Availability
}

// Tick reconciles jobs with the schedules at now:
//
//   - a proxy whose effective schedule is enabled and whose next run is due
//     (or never set) gets one scheduled job; work that is overdue by more than
//     a minute, or never scheduled, is delayed by random jitter, and missed
//     intervals are never replayed;
//   - pending scheduled jobs of disabled schedules are removed, as are their
//     stored next-run times; manual jobs are left alone.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	states, err := s.Store.ListSchedules(ctx)
	if err != nil {
		return err
	}
	queued := false
	for _, kind := range kinds {
		var enabled []domain.ProxyID
		for _, st := range states {
			if err := ctx.Err(); err != nil {
				return err
			}
			eff := domain.EffectiveSchedule(globalOf(settings, kind), scheduleOf(st, kind))
			next := nextOf(st, kind)
			if !eff.Enabled {
				if next != nil {
					if err := s.Store.SetNextRun(ctx, st.ProxyID, kind, nil); err != nil && err != store.ErrNotFound {
						return err
					}
				}
				continue
			}
			enabled = append(enabled, st.ProxyID)
			if next != nil && next.After(now) {
				continue
			}
			due := now
			switch {
			case next == nil || now.Sub(*next) > overdueGrace:
				due = now.Add(s.Jitter())
			default:
				due = *next
			}
			res, err := s.Store.EnqueueJobs(ctx, []domain.ProxyID{st.ProxyID}, kind, domain.SourceScheduled, due, now)
			if err != nil {
				return err
			}
			queued = queued || len(res.New) > 0
		}
		if _, err := s.Store.RemoveScheduledPending(ctx, kind, enabled); err != nil {
			return err
		}
	}
	if queued && s.Wake != nil {
		s.Wake()
	}
	return nil
}

// Completed records the next run after a job of kind finished at now, for
// scheduled and manual jobs alike. Without an enabled schedule the next run
// stays unset.
func (s *Scheduler) Completed(ctx context.Context, id domain.ProxyID, kind domain.CheckKind, now time.Time) error {
	settings, err := s.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	states, err := s.Store.ListSchedules(ctx)
	if err != nil {
		return err
	}
	for _, st := range states {
		if st.ProxyID != id {
			continue
		}
		eff := domain.EffectiveSchedule(globalOf(settings, kind), scheduleOf(st, kind))
		if !eff.Enabled {
			return s.Store.SetNextRun(ctx, id, kind, nil)
		}
		next := now.Add(eff.Interval)
		return s.Store.SetNextRun(ctx, id, kind, &next)
	}
	return store.ErrNotFound
}
