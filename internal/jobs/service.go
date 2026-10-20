package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// DefaultTickEvery is how often schedules are reconciled.
const DefaultTickEvery = 10 * time.Second

// Config wires a Service.
type Config struct {
	Store        *store.Store
	Clock        domain.Clock
	Availability AvailabilityChecker
	Speed        SpeedMeasurer
	Incidents    IncidentRecorder
	NewClient    ClientFactory
	Logger       *slog.Logger
	Poll         time.Duration // idle worker poll interval; default 1 s
	TickEvery    time.Duration // schedule reconcile interval; default 10 s
}

// Service ties the queue, scheduler and runner together.
type Service struct {
	Queue     *Queue
	Scheduler *Scheduler
	Runner    *Runner

	clock     domain.Clock
	tickEvery time.Duration
	logger    *slog.Logger
}

// NewService builds a Service; call Run to start it.
func NewService(c Config) *Service {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.TickEvery <= 0 {
		c.TickEvery = DefaultTickEvery
	}
	sched := NewScheduler(c.Store, c.Clock)
	runner := &Runner{
		Store: c.Store, Clock: c.Clock, Availability: c.Availability, Speed: c.Speed, Incidents: c.Incidents,
		NewClient: c.NewClient, Scheduler: sched, Logger: c.Logger, PollInterval: c.Poll,
	}
	runner.init()
	sched.Wake = runner.Wake
	return &Service{
		Queue:     &Queue{Store: c.Store, Clock: c.Clock, Wake: runner.Wake, Canceler: runner},
		Scheduler: sched, Runner: runner,
		clock: c.Clock, tickEvery: c.TickEvery, logger: c.Logger,
	}
}

// Reconcile applies schedule changes immediately instead of at the next tick.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.Scheduler.Tick(ctx, s.clock.Now()); err != nil {
		return err
	}
	s.Runner.Wake()
	return nil
}

// Run recovers jobs interrupted by a previous process, then runs the workers
// and the schedule loop until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	n, err := s.Runner.Store.RecoverInterrupted(ctx, s.clock.Now())
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.Info("marked jobs left running by a previous process as interrupted", "count", n)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Runner.Run(ctx)
	}()
	for ctx.Err() == nil {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("schedule reconcile failed", "error", err)
		}
		select {
		case <-s.clock.After(s.tickEvery):
		case <-ctx.Done():
		}
	}
	<-done
	return nil
}
