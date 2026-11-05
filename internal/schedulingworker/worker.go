// Package schedulingworker owns booking mutations and calendar lifecycle events.
package schedulingworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/worker"
	"context"
	"log/slog"
	"strings"
	"time"
)

type Worker struct {
	runtime  worker.Worker
	calendar providers.Calendar
}

func New(base worker.Worker, calendar providers.Calendar) *Worker {
	w := &Worker{runtime: base, calendar: calendar}
	w.runtime.Queue = "scheduling"
	w.runtime.Planner = nil
	w.runtime.Calendar = nil
	w.runtime.Schedule = nil
	w.runtime.HandleEffect = w.apply
	return w
}
func (w *Worker) Run(ctx context.Context) {
	// Independent mailbox loop avoids waiting behind an external calendar mutation.
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, e := w.answerAgenda(ctx); e != nil && ctx.Err() == nil {
					slog.Error("agenda request failed")
				}
			}
		}
	}()
	w.runtime.Run(ctx)
	<-done
}
func (w *Worker) Once(ctx context.Context) (bool, error) {
	if ok, e := w.answerAgenda(ctx); ok || e != nil {
		return ok, e
	}
	return w.runtime.Once(ctx)
}
func (w *Worker) apply(ctx context.Context, j store.Job, s domain.State, fx domain.Effect, now time.Time) (domain.State, []domain.Effect, any, error) {
	switch fx.Kind {
	case "book":
		if fx.Slot == nil {
			return s, nil, nil, &providers.Failure{Code: "missing_slot"}
		}
		operation := j.ID
		if fx.Purpose == "form_booking" {
			operation = strings.TrimPrefix(j.ID, "booking-event/")
		}
		b, e := w.calendar.Book(ctx, operation, s, *fx.Slot, fx.Booking)
		if e != nil {
			if fx.Purpose == "form_booking" {
				if failure, ok := e.(*providers.Failure); ok && !failure.Ambiguous && !failure.Retryable {
					return s, nil, nil, &worker.SkipEffect{Reason: failure.Code}
				}
			}
			if failure, ok := e.(*providers.Failure); ok && failure.Code == "google_slot_no_longer_available" && fx.Purpose != "form_booking" {
				s.Offered = nil
				s.OfferVersion = ""
				text := "Esse horário acabou de ficar indisponível. Quer que eu procure outro?"
				if s.Booking != nil {
					text = "Esse horário acabou de ficar indisponível. Sua reserva atual continua confirmada. Quer que eu procure outro?"
				}
				return s, []domain.Effect{{ID: j.ID + ":slot_taken", LeadID: s.ID, Kind: "send", Purpose: "reply", Text: text, Due: now, Expires: now.Add(30 * time.Minute)}}, map[string]string{"status": "slot_unavailable"}, nil
			}
			return s, nil, nil, e
		}
		if b.GoogleSynced {
			return s, nil, b, nil
		}
		ev := domain.Event{ID: j.ID + ":result", LeadID: s.ID, Kind: "booking.created", At: now, Booking: &b}
		if fx.Booking != nil {
			ev.Kind = "booking.rescheduled"
		}
		r := domain.Reduce(s, ev, domain.Decision{}, nil, w.runtime.Policy, now)
		return r.State, r.Effects, b, nil
	case "cancel":
		if fx.Booking == nil {
			return s, nil, nil, &providers.Failure{Code: "missing_booking"}
		}
		if e := w.calendar.Cancel(ctx, j.ID, *fx.Booking); e != nil {
			return s, nil, nil, e
		}
		if fx.Booking.GoogleSynced {
			return s, nil, map[string]bool{"cancelled": true}, nil
		}
		r := domain.Reduce(s, domain.Event{ID: j.ID + ":result", LeadID: s.ID, Kind: "booking.cancelled", BookingID: fx.Booking.ID, At: now}, domain.Decision{}, nil, w.runtime.Policy, now)
		r.Effects = append(r.Effects, domain.Effect{ID: j.ID + ":confirmed", LeadID: s.ID, Kind: "send", Purpose: "reply", Text: "Sua reserva foi cancelada. Se quiser retomar, posso ajudar a encontrar outro horário.", Due: now, Expires: now.Add(30 * time.Minute)})
		return r.State, r.Effects, map[string]bool{"cancelled": true}, nil
	default:
		return s, nil, nil, &providers.Failure{Code: "unsupported_scheduling_effect"}
	}
}
