// Package followupworker owns scheduled outreach: follow-ups, reminders and
// recovery after an explicit no-show. It never calls the model or calendar.
package followupworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/remindermedia"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"arkom/bia/internal/worker"
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	runtime      worker.Worker
	Audio        remindermedia.Asset
	VoiceEnabled bool
}

func New(base worker.Worker) *Worker {
	w := &Worker{runtime: base}
	w.runtime.Queue = "followup"
	w.runtime.Planner = nil
	w.runtime.Calendar = nil
	w.runtime.HandleEffect = w.effect
	w.runtime.AfterSend = afterSend
	w.runtime.Schedule = func(s domain.State, fx domain.Effect, now time.Time) (time.Time, string) {
		due, why := NextContact(w.runtime.Policy, s, fx, now)
		if why != "" || due.After(now) || !domain.IsMeetingReminder(fx) {
			return due, why
		}
		if fx.Purpose == "meeting_preparation" || fx.Purpose == "meeting_start" {
			return due, ""
		}
		if fx.Purpose == "meeting_sms" {
			if w.runtime.SMSSender == nil {
				return time.Time{}, "sms_not_enabled"
			}
			return due, ""
		}
		if s.LastInbound.IsZero() || now.Sub(s.LastInbound) >= 23*time.Hour {
			return time.Time{}, "approved_template_required_outside_session"
		}
		if fx.Purpose == "meeting_audio" && !w.Audio.Ready() {
			return time.Time{}, "reminder_audio_not_ready"
		}
		if fx.Purpose == "meeting_call" && !w.VoiceEnabled {
			return time.Time{}, "voice_not_enabled"
		}
		return due, ""
	}
	return w
}
func (w *Worker) Run(ctx context.Context)                { w.runtime.Run(ctx) }
func (w *Worker) Once(ctx context.Context) (bool, error) { return w.runtime.Once(ctx) }

// RunScheduler runs once per process; consumers may be parallel. Recovery also
// covers bookings created before this cadence was enabled.
func (w *Worker) RunScheduler(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		if w.runtime.Policy.MeetingStart {
			step, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := w.runtime.DB.ReconcileMeetingStart(step, time.Now().UTC(), w.runtime.Live); err != nil && ctx.Err() == nil {
				slog.Error("meeting start reconciliation failed")
			}
			cancel()
		}
		if w.runtime.Policy.MeetingPreparation {
			step, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := w.runtime.DB.ReconcilePreparation(step, time.Now().UTC(), w.runtime.Live); err != nil && ctx.Err() == nil {
				slog.Error("preparation reconciliation failed")
			}
			cancel()
		}
		if w.runtime.Policy.SMSReminders {
			step, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := w.runtime.DB.ReconcileSMSReminders(step, time.Now().UTC(), w.runtime.Live); err != nil && ctx.Err() == nil {
				slog.Error("SMS reminder reconciliation failed")
			}
			cancel()
		}
		if w.runtime.Policy.MeetingReminders {
			step, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := w.runtime.DB.ReconcileMeetingReminders(step, time.Now().UTC(), w.runtime.Live); err != nil && ctx.Err() == nil {
				slog.Error("meeting reminder reconciliation failed")
			}
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func afterSend(s domain.State, fx domain.Effect, now time.Time) []domain.Effect {
	if fx.Kind != "send" || fx.Purpose != "meeting_call" {
		return nil
	}
	// Committed in the same transaction as the successful text receipt.
	next := fx
	next.ID += ":permission"
	next.Kind = "request_call"
	next.Due = now
	return []domain.Effect{next}
}

func (w *Worker) effect(ctx context.Context, j store.Job, s domain.State, fx domain.Effect, now time.Time) (domain.State, []domain.Effect, any, error) {
	switch fx.Kind {
	case "prepare_audio":
		next := fx
		next.ID += ":media"
		next.Kind = "send_audio"
		next.MediaURL = w.Audio.URL()
		next.Due = now
		return s, []domain.Effect{next}, map[string]string{"queued": "media"}, nil
	case "request_call":
		// Voice request IDs cannot contain slashes. The stable job ID remains
		// unique per booking and time, including after process restarts.
		id := "meeting:" + domain.LeadKey(j.ID)[5:]
		inserted, err := (voiceworker.Repository{DB: w.runtime.DB}).CreateScheduled(ctx, id, s.ID, j.Mode, w.runtime.VoiceSender, fx)
		if errors.Is(err, voiceworker.ErrBlocked) {
			return s, nil, nil, &worker.SkipEffect{Reason: "voice_request_blocked"}
		}
		return s, nil, map[string]any{"voice_request_id": id, "created": inserted}, err
	default:
		return s, nil, nil, &providers.Failure{Code: "unsupported_followup_effect"}
	}
}

// Recheck the current timezone and quiet hours at delivery, including after a
// restart or profile update. Never extend a reminder beyond its original expiry.
func NextContact(p domain.Policy, s domain.State, fx domain.Effect, now time.Time) (time.Time, string) {
	// Explicit demo actions bypass only the cadence and contact hours. Consent,
	// booking, session, provider limits and delivery checks still apply.
	if domain.IsMeetingReminder(fx) && fx.ManualReminder {
		if ok, why := domain.Deliverable(s, fx, now); !ok {
			return time.Time{}, why
		}
		if p.TestPhone != "" && s.Profile.Phone != p.TestPhone {
			return time.Time{}, "test_phone_mismatch"
		}
		return now, ""
	}
	if fx.Purpose == "meeting_start" {
		if !p.MeetingStart || p.TestPhone != "" {
			return time.Time{}, "meeting_start_disabled"
		}
		if ok, why := domain.Deliverable(s, fx, now); !ok {
			return time.Time{}, why
		}
		return now, ""
	}
	if fx.Purpose == "meeting_sms" {
		if !p.SMSReminders || p.TestPhone != "" {
			return time.Time{}, "sms_reminders_disabled"
		}
		// This is a requested reminder for the booked time, including 07:55
		// for an 08:00 meeting. It is not an unsolicited follow-up cadence.
		if ok, why := domain.Deliverable(s, fx, now); !ok {
			return time.Time{}, why
		}
		return now, ""
	} else if fx.Purpose == "meeting_preparation" {
		if !p.MeetingPreparation || p.TestPhone != "" {
			return time.Time{}, "meeting_preparation_disabled"
		}
	} else if domain.IsMeetingReminder(fx) {
		if !p.MeetingReminders || p.TestPhone != "" {
			return time.Time{}, "meeting_reminders_disabled"
		}
	} else if p.TestPhone != "" || p.DisableFollowups {
		return time.Time{}, "scheduled_outreach_disabled_during_test"
	}
	if fx.Purpose == "reminder" && p.MeetingReminders {
		return time.Time{}, "replaced_by_meeting_sequence"
	}
	due := p.NextContact(now, s)
	if !fx.Expires.IsZero() && !due.Before(fx.Expires) {
		return time.Time{}, "contact_window_expired"
	}
	if (fx.Purpose == "reminder" || domain.IsMeetingReminder(fx)) && (s.Booking == nil || !due.Before(s.Booking.Start)) {
		return time.Time{}, "meeting_not_upcoming"
	}
	return due, ""
}
