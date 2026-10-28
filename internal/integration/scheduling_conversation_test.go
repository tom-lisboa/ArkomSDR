package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
)

func TestRecurringCalendarNaturalChoiceAndDatabaseOnlyReservation(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cal := &providers.LocalCalendar{Pool: db.Pool}
	a := providers.DailyAvailability{Enabled: true, TimeZone: "America/Cuiaba", Windows: "08:00-12:00,14:00-20:00", DurationMinutes: 45, Days: 3, CloserID: 1}
	if e := cal.EnsureAvailability(ctx, a, now); e != nil {
		t.Fatal(e)
	}
	var before, after int
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.slots`).Scan(&before)
	if e := cal.EnsureAvailability(ctx, a, now); e != nil {
		t.Fatal(e)
	}
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.slots`).Scan(&after)
	if before == 0 || before != after {
		t.Fatal("duplicate availability", before, after)
	}
	s := testState("database-only-booking", now)
	slots, e := cal.Slots(ctx, s, now)
	if e != nil || len(slots) < 2 {
		t.Fatal(e)
	}
	s.Profile.Goal = "Reduzir faltas"
	r := domain.Reduce(s, domain.Event{ID: "initial", Kind: "message.received", At: now, Text: "Claro"}, domain.Decision{Action: "offer_slots"}, slots, policy(), now)
	s = r.State
	r = domain.Reduce(s, domain.Event{ID: "choice", Kind: "message.received", At: now, Text: "O primeiro"}, domain.Decision{Action: "book", SlotID: s.Offered[0].ID, Evidence: "O primeiro"}, slots, policy(), now)
	if len(r.Effects) != 1 || r.Effects[0].Kind != "book" {
		t.Fatal(r.Effects)
	}
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	w.Policy.DisableFollowups = true
	w.Policy.Reminders = nil
	for i := range r.Effects {
		r.Effects[i].Due = now.Add(-time.Second)
	}
	publishEffects(t, db, "seed-choice", r.State, r.Effects...)
	sw := schedulingworker.New(*w, cal)
	step(t, w, false)
	step(t, sw, true)
	s, e = db.Lead(ctx, s.ID)
	if e != nil || s.Booking == nil || s.Booking.MeetingURL != "" || s.Booking.End.Sub(s.Booking.Start) != 45*time.Minute {
		t.Fatal("reservation missing", s.Booking, e)
	}
	var reserved string
	if e = db.Pool.QueryRow(ctx, `SELECT reserved_by FROM bia_go.slots WHERE id=$1`, slots[0].ID).Scan(&reserved); e != nil || reserved != s.Booking.ID {
		t.Fatal("slot not reserved", e)
	}
	var confirmation string
	if e = db.Pool.QueryRow(ctx, `SELECT payload->>'text' FROM bia_go.jobs WHERE payload->>'purpose'='booking_confirmation'`).Scan(&confirmation); e != nil || strings.Contains(confirmation, "http") {
		t.Fatal(confirmation, e)
	}
	if e = cal.EnsureAvailability(ctx, a, now); e != nil {
		t.Fatal(e)
	}
	if e = db.Pool.QueryRow(ctx, `SELECT reserved_by FROM bia_go.slots WHERE id=$1`, slots[0].ID).Scan(&reserved); e != nil || reserved != s.Booking.ID {
		t.Fatal("reconciliation overwrote reservation", e)
	}
	drainRole(t, sw)
	drain(t, w)
	if m.Count.Load() != 1 {
		t.Fatal("confirmation not delivered", m.Count.Load())
	}
}
