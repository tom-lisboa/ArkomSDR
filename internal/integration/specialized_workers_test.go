package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
	"arkom/bia/internal/store"
	"arkom/bia/internal/worker"
	"github.com/jackc/pgx/v5"
)

type stepper interface {
	Once(context.Context) (bool, error)
}

func step(t *testing.T, w stepper, want bool) {
	t.Helper()
	worked, e := w.Once(context.Background())
	if e != nil || worked != want {
		t.Fatalf("worked=%v want=%v error=%v", worked, want, e)
	}
}
func drainRole(t *testing.T, w stepper) {
	t.Helper()
	for range 50 {
		worked, e := w.Once(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			return
		}
	}
	t.Fatal("role did not drain")
}

// Publish through the normal transactional outbox, not directly into the queue.
func publishEffects(t *testing.T, db *store.DB, id string, s domain.State, effects ...domain.Effect) {
	t.Helper()
	ctx := context.Background()
	submit(t, db, domain.Event{ID: id, LeadID: s.ID, Kind: "human.release", At: time.Now()}, "live")
	j, _, e := db.ClaimQueue(ctx, "execution")
	if e != nil {
		t.Fatal(e)
	}
	if j.ID != id {
		t.Fatalf("unexpected pending job %s", j.ID)
	}
	if e = db.Finish(ctx, j, s, effects, "done", nil); e != nil {
		t.Fatal(e)
	}
}
func testState(id string, now time.Time) domain.State {
	return domain.State{ID: id, Owner: "go", CreatedAt: now, FirstContactAt: &now, Profile: domain.Form{Name: "Teste", Phone: "+5565999999900", Consent: true, TimeZone: "America/Cuiaba"}}
}
func scheduledMessage(id string, s domain.State, purpose string, now time.Time) domain.Effect {
	fx := domain.Effect{ID: "fx/" + id, LeadID: s.ID, Kind: "send", Purpose: purpose, Text: "Mensagem de teste", Due: time.Now().Add(-time.Second), Expires: now.Add(48 * time.Hour), OfferVersion: s.OfferVersion}
	if s.Booking != nil {
		fx.BookingID = s.Booking.ID
		fx.BookingStart = s.Booking.Start
	}
	return fx
}
func runtimeWorker(db *store.DB, now time.Time, m *countingMessenger) *worker.Worker {
	w := baseWorker(db)
	w.Live = true
	w.Messenger = m
	w.Now = func() time.Time { return now }
	w.Policy.QuietStart = 0
	w.Policy.QuietEnd = 24
	return w
}
func assertJob(t *testing.T, db *store.DB, id, queue, status string) {
	t.Helper()
	j, e := db.Job(context.Background(), id)
	if e != nil || j.Queue != queue || j.Status != status {
		t.Fatalf("%s: queue=%s status=%s err=%v", id, j.Queue, j.Status, e)
	}
}

func TestSchedulingOwnsBookRescheduleCancel(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := testState("booking-worker-lead", now)
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	cal := &providers.LocalCalendar{Pool: db.Pool}
	sw := schedulingworker.New(*w, cal)
	fw := followupworker.New(*w)
	slot := providers.LocalSlot{Slot: domain.Slot{ID: "slot-one", Provider: "local", CloserID: 10, Start: now.Add(48 * time.Hour), End: now.Add(49 * time.Hour)}, MeetingURL: "https://meet.example/one"}
	if e := cal.Add(ctx, slot); e != nil {
		t.Fatal(e)
	}
	book := domain.Effect{ID: "fx/book-one", LeadID: s.ID, Kind: "book", Purpose: "booking", Slot: &slot.Slot, Due: now.Add(-time.Second), Expires: now.Add(time.Hour)}
	publishEffects(t, db, "seed-book", s, book)
	step(t, w, false)
	step(t, fw, false)
	step(t, sw, true)
	assertJob(t, db, book.ID, "scheduling", "done")
	s, e := db.Lead(ctx, s.ID)
	if e != nil || s.Booking == nil {
		t.Fatal("reservation missing", e)
	}
	first := *s.Booking
	// Confirmation belongs to scheduling; reminder timestamps belong to followup.
	var reminders, confirmations int
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE queue='followup'),count(*) FILTER(WHERE queue='scheduling' AND payload->>'purpose'='booking_confirmation') FROM bia_go.jobs WHERE status='pending'`).Scan(&reminders, &confirmations); e != nil || reminders != 3 || confirmations != 1 {
		t.Fatal(reminders, confirmations, e)
	}
	drainRole(t, sw)
	drain(t, w)
	if m.Count.Load() != 1 {
		t.Fatal("confirmation missing", m.Count.Load())
	}
	second := slot
	second.ID = "slot-two"
	second.Start = slot.Start.Add(24 * time.Hour)
	second.End = slot.End.Add(24 * time.Hour)
	if e = cal.Add(ctx, second); e != nil {
		t.Fatal(e)
	}
	book.ID = "fx/reschedule"
	book.Slot = &second.Slot
	book.Booking = &first
	book.BookingID = first.ID
	s, _ = db.Lead(ctx, s.ID)
	publishEffects(t, db, "seed-reschedule", s, book)
	step(t, w, false)
	step(t, sw, true)
	drainRole(t, sw)
	drain(t, w)
	s, _ = db.Lead(ctx, s.ID)
	if s.Booking == nil || !s.Booking.Start.Equal(second.Start) || s.Booking.CloserID != first.CloserID {
		t.Fatal("reschedule failed", s.Booking)
	}
	cancel := domain.Effect{ID: "fx/cancel", LeadID: s.ID, Kind: "cancel", Purpose: "cancel_booking", Booking: s.Booking, BookingID: s.Booking.ID, Due: now.Add(-time.Second), Expires: now.Add(time.Hour)}
	publishEffects(t, db, "seed-cancel", s, cancel)
	step(t, sw, true)
	drain(t, w)
	s, _ = db.Lead(ctx, s.ID)
	if s.Booking != nil {
		t.Fatal("cancel not applied")
	}
	if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE queue='followup' AND status='pending'`); e != nil {
		t.Fatal(e)
	}
	before := m.Count.Load()
	drainRole(t, fw)
	if m.Count.Load() != before {
		t.Fatal("sent obsolete reminder")
	}
	var skipped int
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE queue='followup' AND status='skipped'`).Scan(&skipped); e != nil || skipped != 6 {
		t.Fatal("old reminders not invalidated", skipped, e)
	}
	slots, e := cal.Slots(ctx, s, now)
	if e != nil || len(slots) != 2 {
		t.Fatal("reservation capacity not released", slots, e)
	}
}

func TestCrossQueueEventsPrecedeReminder(t *testing.T) {
	for _, kind := range []string{"booking.cancelled", "contact.opt_out", "human.takeover"} {
		t.Run(kind, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("priority-lead", now)
			s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}
			fx := scheduledMessage("reminder", s, "reminder", now)
			publishEffects(t, db, "seed", s, fx)
			submit(t, db, domain.Event{ID: "change", LeadID: s.ID, Kind: kind, At: now, BookingID: "b"}, "live")
			m := &countingMessenger{}
			w := runtimeWorker(db, now, m)
			fw := followupworker.New(*w)
			sw := schedulingworker.New(*w, &providers.LocalCalendar{Pool: db.Pool})
			step(t, fw, false)
			if kind == "booking.cancelled" {
				step(t, w, false)
				step(t, sw, true)
			} else {
				step(t, sw, false)
				step(t, w, true)
			}
			step(t, fw, true)
			assertJob(t, db, fx.ID, "followup", "skipped")
			if m.Count.Load() != 0 {
				t.Fatal("sent before applying event")
			}
			// Administrative event order spans all queues, including cadence.resume.
			submit(t, db, domain.Event{ID: "preference", LeadID: s.ID, Kind: "schedule.preference", Preference: "amanhã", At: now}, "live")
			submit(t, db, domain.Event{ID: "resume", LeadID: s.ID, Kind: "cadence.resume", At: now}, "live")
			step(t, fw, false)
			step(t, sw, true)
			step(t, fw, true)
			if _, _, e := db.ClaimQueue(ctx, "all"); e == nil {
				t.Fatal("wildcard consumer accepted")
			}
		})
	}
}

func TestFollowupQuietHoursAndExpiry(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	zone, _ := time.LoadLocation("America/Cuiaba")
	day := time.Now().In(zone)
	now := time.Date(day.Year(), day.Month(), day.Day(), 23, 0, 0, 0, zone)
	s := testState("quiet-lead", now)
	s.OfferVersion = "offer"
	fx := scheduledMessage("quiet", s, "followup", now)
	fx.Expires = now.Add(24 * time.Hour)
	publishEffects(t, db, "seed", s, fx)
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	w.Policy = policy()
	fw := followupworker.New(*w)
	step(t, fw, true)
	j, e := db.Job(ctx, fx.ID)
	if e != nil || j.Status != "pending" || j.Attempts != 0 {
		t.Fatal("policy wait spent retry budget", j, e)
	}
	var due time.Time
	if e = db.Pool.QueryRow(ctx, `SELECT due_at FROM bia_go.jobs WHERE id=$1`, fx.ID).Scan(&due); e != nil {
		t.Fatal(e)
	}
	tomorrow := now.AddDate(0, 0, 1)
	if !due.Equal(time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 8, 0, 0, 0, zone)) {
		t.Fatal(due)
	}
	// A short reminder cannot be moved into tomorrow after the meeting.
	s.Booking = &domain.Booking{ID: "short", Start: now.Add(time.Hour)}
	short := scheduledMessage("short", s, "reminder", now)
	short.Expires = now.Add(20 * time.Minute)
	publishEffects(t, db, "seed-short", s, short)
	step(t, fw, true)
	assertJob(t, db, short.ID, "followup", "skipped")
	if m.Count.Load() != 0 {
		t.Fatal("quiet hours bypassed")
	}
}

func TestFollowupDeliveryReceiptAndNoShowRecovery(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := testState("followup-lead", now)
	s.OfferVersion = "offer"
	fx := scheduledMessage("followup-send", s, "followup", now)
	publishEffects(t, db, "seed", s, fx)
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	fw := followupworker.New(*w)
	sw := schedulingworker.New(*w, &providers.LocalCalendar{Pool: db.Pool})
	step(t, w, false)
	step(t, sw, false)
	step(t, fw, true)
	step(t, fw, false)
	assertJob(t, db, fx.ID, "followup", "done")
	s, e := db.Lead(ctx, s.ID)
	if e != nil || s.LastFollowup == nil || m.Count.Load() != 1 {
		t.Fatal("followup not recorded", e)
	}
	var receipts int
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.worker_tasks WHERE kind='delivery.completed'`).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatal("no context receipt", receipts, e)
	}
	// Only an explicit no-show event schedules recovery, then followup sends it.
	s.Booking = &domain.Booking{ID: "absent", Start: now.Add(-time.Hour), End: now.Add(-15 * time.Minute)}
	publishEffects(t, db, "seed-attendance", s)
	submit(t, db, domain.Event{ID: "absent-event", LeadID: s.ID, Kind: "attendance.no_show", At: now, BookingID: "absent"}, "live")
	step(t, fw, false)
	step(t, sw, true)
	step(t, fw, true)
	if m.Count.Load() != 2 {
		t.Fatal("no-show recovery not delivered", m.Count.Load())
	}
	drain(t, w)
}

func TestGlobalLeaseAndRestartAcrossQueues(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := testState("lease-lead", now)
	s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour)}
	cancel := domain.Effect{ID: "fx/cancel-crash", LeadID: s.ID, Kind: "cancel", Booking: s.Booking, BookingID: "b", Due: now.Add(-time.Second), Expires: now.Add(time.Hour)}
	reminder := scheduledMessage("lease-reminder", s, "reminder", now)
	publishEffects(t, db, "seed", s, cancel, reminder)
	var wg sync.WaitGroup
	claims := make(chan store.Job, 2)
	errs := make(chan error, 2)
	for _, q := range []string{"scheduling", "followup"} {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			j, _, e := db.ClaimQueue(ctx, q)
			if e == nil {
				claims <- j
			} else if !errors.Is(e, pgx.ErrNoRows) {
				errs <- e
			}
		}(q)
	}
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	var old store.Job
	count := 0
	for j := range claims {
		old = j
		count++
	}
	if count != 1 {
		t.Fatal("concurrent consumers leased same lead", count)
	}
	if _, e := db.Pool.Exec(ctx, `UPDATE bia_go.leads SET lease_until=now()-interval '1 second' WHERE id=$1`, s.ID); e != nil {
		t.Fatal(e)
	}
	other := "followup"
	if old.Queue == other {
		other = "scheduling"
	}
	j, state, e := db.ClaimQueue(ctx, other)
	if e != nil {
		t.Fatal(e)
	}
	if !state.Human {
		t.Fatal("crash did not require reconciliation")
	}
	assertJob(t, db, old.ID, old.Queue, "uncertain")
	if e = db.Finish(ctx, old, s, nil, "done", nil); !errors.Is(e, store.ErrLease) {
		t.Fatal("expired worker committed", e)
	}
	if e = db.Finish(ctx, j, state, nil, "skipped", nil); e != nil {
		t.Fatal(e)
	}
}

func TestRoutingMigrationBackfillsWithoutReplaying(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s := testState("migration-lead", now)
	cases := []struct{ kind, purpose, queue string }{{"book", "booking", "scheduling"}, {"cancel", "cancel_booking", "scheduling"}, {"send", "booking_confirmation", "scheduling"}, {"send", "reminder", "followup"}, {"send", "recovery", "followup"}, {"send", "followup", "followup"}, {"send", "reply", "execution"}, {"crm", "handoff", "execution"}}
	var effects []domain.Effect
	for i, c := range cases {
		effects = append(effects, domain.Effect{ID: fmt.Sprintf("fx/route-%d", i), LeadID: s.ID, Kind: c.kind, Purpose: c.purpose, Due: now.Add(time.Hour), Expires: now.Add(2 * time.Hour)})
	}
	publishEffects(t, db, "seed", s, effects...)
	// Simulate the pre-v3 schema with real existing rows and terminal statuses.
	if _, e := db.Pool.Exec(ctx, `ALTER TABLE bia_go.jobs DROP COLUMN queue; DELETE FROM bia_go.schema_versions WHERE version=3; UPDATE bia_go.jobs SET status='simulated' WHERE id='fx/route-0'`); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := db.Migrate(ctx); e != nil {
			t.Fatal(e)
		}
	}
	for i, c := range cases {
		status := "pending"
		if i == 0 {
			status = "simulated"
		}
		assertJob(t, db, fmt.Sprintf("fx/route-%d", i), c.queue, status)
	}
	// The generated route cannot be overridden by an event producer.
	if _, e := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET queue='execution' WHERE id='fx/route-1'`); e == nil {
		t.Fatal("producer overrode queue")
	}
	metrics, e := db.Metrics(ctx)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(metrics)
	if len(raw) == 0 {
		t.Fatal("metrics missing")
	}
}
