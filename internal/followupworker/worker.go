// Package followupworker owns scheduled outreach: follow-ups, reminders and
// recovery after an explicit no-show. It never calls the model or calendar.
package followupworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/worker"
	"context"
	"time"
)

type Worker struct{ runtime worker.Worker }

func New(base worker.Worker) *Worker {
	w := &Worker{runtime: base}
	w.runtime.Queue = "followup"
	w.runtime.Planner = nil
	w.runtime.Calendar = nil
	w.runtime.HandleEffect = nil
	w.runtime.Schedule = func(s domain.State, fx domain.Effect, now time.Time) (time.Time, string) {
		return NextContact(w.runtime.Policy, s, fx, now)
	}
	return w
}
func (w *Worker) Run(ctx context.Context)                { w.runtime.Run(ctx) }
func (w *Worker) Once(ctx context.Context) (bool, error) { return w.runtime.Once(ctx) }

// Recheck the current timezone and quiet hours at delivery, including after a
// restart or profile update. Never extend a reminder beyond its original expiry.
func NextContact(p domain.Policy, s domain.State, fx domain.Effect, now time.Time) (time.Time, string) {
	if p.TestPhone != "" || p.DisableFollowups {
		return time.Time{}, "scheduled_outreach_disabled_during_test"
	}
	due := p.NextContact(now, s)
	if !fx.Expires.IsZero() && !due.Before(fx.Expires) {
		return time.Time{}, "contact_window_expired"
	}
	if fx.Purpose == "reminder" && (s.Booking == nil || !due.Before(s.Booking.Start)) {
		return time.Time{}, "meeting_not_upcoming"
	}
	return due, ""
}
