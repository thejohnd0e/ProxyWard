// Package notify tracks incidents and delivers notifications about them.
package notify

import (
	"context"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// IncidentTracker turns availability results into down and recovery events.
// The incident, the event and the outbox row are written atomically by the
// store, so a restart can neither lose nor duplicate a notification.
type IncidentTracker struct {
	Store *store.Store
}

// Apply records one availability result using the configured down threshold.
// Indeterminate, canceled and interrupted results change nothing.
func (t *IncidentTracker) Apply(ctx context.Context, result domain.Availability) error {
	settings, err := t.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	_, err = t.Store.RecordAvailabilityOutcome(ctx, result, settings.DownThreshold)
	if err == store.ErrNotFound {
		return nil // the proxy was deleted while its check ran
	}
	return err
}
