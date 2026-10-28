package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arkom/bia/internal/agent"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/worker"
	"github.com/jackc/pgx/v5"
)

func setup(t *testing.T) *store.DB {
	t.Helper()
	dsn := os.Getenv("BIA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BIA_TEST_DATABASE_URL to run isolated PostgreSQL integration tests")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Path != "/arkom_bia_test" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("tests only accept localhost/arkom_bia_test")
	}
	db, e := store.Open(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(db.Close)
	if _, e = db.Pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS bia_go CASCADE`); e != nil {
		t.Fatal(e)
	}
	if e = db.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return db
}
func policy() domain.Policy {
	return domain.Policy{Brand: "Exemplo", SchedulingURL: "https://example.com/agendar", TimeZone: "America/Cuiaba", QuietStart: 8, QuietEnd: 20, FollowupAfter: 2 * time.Hour, Reminders: []time.Duration{24 * time.Hour, 2 * time.Hour, 15 * time.Minute}}
}
func form(id, phone string) domain.Event {
	zero := int64(0)
	f := domain.Form{Name: "Lead Exemplo", Phone: phone, Consent: true, ConsentTextVersion: "test-v1", Revenue: &zero, Channel: "gateway", TimeZone: "America/Cuiaba"}
	return domain.Event{ID: id, LeadID: domain.LeadKey(phone), Kind: "form.submitted", At: time.Now().UTC(), Form: &f}
}
func submit(t *testing.T, db *store.DB, e domain.Event, mode string) {
	t.Helper()
	if _, err := db.Submit(context.Background(), e, mode); err != nil {
		t.Fatal(err)
	}
}
func baseWorker(db *store.DB) *worker.Worker {
	return &worker.Worker{DB: db, Planner: agent.Offline{}, Messenger: providers.Disabled{}, Calendar: &providers.LocalCalendar{Pool: db.Pool}, CRM: providers.LocalCRM{Pool: db.Pool}, Policy: policy(), PerLeadDaily: 15, GlobalHourly: 60}
}
func drain(t *testing.T, w *worker.Worker) {
	t.Helper()
	for i := 0; i < 50; i++ {
		worked, e := w.Once(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if !worked {
			return
		}
	}
	t.Fatal("queue did not drain")
}

func TestFormAPIAndShadowQueue(t *testing.T) {
	db := setup(t)
	token := strings.Repeat("x", 32)
	srv := &httpapi.Server{DB: db, Calendar: &providers.LocalCalendar{Pool: db.Pool}, Token: token, Mode: "shadow", Policy: policy()}
	h := srv.Handler()
	e := form("form-zero", "+5565999999999")
	body, _ := json.Marshal(e.Form)
	call := func(b []byte, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/forms", bytes.NewReader(b))
		r.Header.Set("Authorization", auth)
		r.Header.Set("Idempotency-Key", e.ID)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if r := call(body, ""); r.Code != 401 {
		t.Fatal(r.Code)
	}
	r := call(body, "Bearer "+token)
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"revenue_gate":false`) {
		t.Fatal(r.Body.String())
	}
	r = call(body, "Bearer "+token)
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"duplicate":true`) {
		t.Fatal("duplicate form not idempotent", r.Body.String())
	}
	e.Form.Name = "Different"
	other, _ := json.Marshal(e.Form)
	if call(other, "Bearer "+token).Code != 409 {
		t.Fatal("changed payload accepted with old key")
	}
	w := baseWorker(db)
	drain(t, w)
	s, err := db.Lead(context.Background(), e.LeadID)
	if err != nil {
		t.Fatal(err)
	}
	if s.FirstContactAt != nil {
		t.Fatal("simulation marked contact as real")
	}
	var n int
	err = db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE status='simulated' AND payload->>'purpose'='welcome'`).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("welcome count %d: %v", n, err)
	}
	req := httptest.NewRequest("GET", "/v1/leads/"+e.LeadID+"/availability", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"status":"empty"`) {
		t.Fatal(rr.Body.String())
	}
	// A storage failure is not described as an empty/full calendar.
	_, _ = db.Pool.Exec(context.Background(), `ALTER TABLE bia_go.slots RENAME TO unavailable_slots`)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 503 || !strings.Contains(rr.Body.String(), `"status":"unavailable"`) {
		t.Fatal(rr.Body.String())
	}
}

func TestConcurrentIdempotencyAndLeaseFencing(t *testing.T) {
	db := setup(t)
	e := form("same-event", "+5565999999999")
	var inserted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := db.Submit(context.Background(), e, "shadow")
			if err != nil {
				t.Error(err)
			}
			if ok {
				inserted.Add(1)
			}
		}()
	}
	wg.Wait()
	if inserted.Load() != 1 {
		t.Fatal(inserted.Load())
	}
	j, s, err := db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.Claim(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("same lead claimed twice", err)
	}
	_, err = db.Pool.Exec(context.Background(), `UPDATE bia_go.leads SET lease_until=now()-interval '1 second' WHERE id=$1`, e.LeadID)
	if err != nil {
		t.Fatal(err)
	}
	newJob, _, err := db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Finish(context.Background(), j, s, nil, "done", nil); !errors.Is(err, store.ErrLease) {
		t.Fatal("stale worker committed", err)
	}
	if err = db.Finish(context.Background(), newJob, s, nil, "done", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCrashDuringEffectParksForReconciliation(t *testing.T) {
	db := setup(t)
	e := form("form-crash", "+5565999999999")
	submit(t, db, e, "live")
	j, s, err := db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := domain.Reduce(s, e, domain.Decision{}, nil, policy(), time.Now())
	if err = db.Finish(context.Background(), j, r.State, r.Effects, "done", nil); err != nil {
		t.Fatal(err)
	}
	fx, _, err := db.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fx.Kind != "effect" {
		t.Fatal("expected effect")
	}
	_, _ = db.Pool.Exec(context.Background(), `UPDATE bia_go.leads SET lease_until=now()-interval '1 second' WHERE id=$1`, e.LeadID)
	_, _, err = db.Claim(context.Background())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	parked, err := db.Job(context.Background(), fx.ID)
	if err != nil || parked.Status != "uncertain" {
		t.Fatalf("%+v %v", parked, err)
	}
	s, err = db.Lead(context.Background(), e.LeadID)
	if err != nil || !s.Human {
		t.Fatal("uncertain effect did not pause automation")
	}
}

type countingMessenger struct {
	Count atomic.Int32
	Err   error
}

func (m *countingMessenger) Send(context.Context, string, domain.State, string, string, time.Time) (providers.Receipt, error) {
	m.Count.Add(1)
	return providers.Receipt{ID: "receipt", Status: "sent"}, m.Err
}
func TestShadowEffectsCannotBecomeLive(t *testing.T) {
	db := setup(t)
	e := form("old-shadow", "+5565999999999")
	submit(t, db, e, "shadow")
	w := baseWorker(db)
	_, err := w.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	submit(t, db, domain.Event{ID: "owner", LeadID: e.LeadID, Kind: "ownership.changed", Owner: "go", At: time.Now()}, "live")
	m := &countingMessenger{}
	w.Live = true
	w.Messenger = m
	drain(t, w)
	if m.Count.Load() != 0 {
		t.Fatal("shadow effects replayed live")
	}
}

func TestLocalCalendarConcurrencyAndAtomicReschedule(t *testing.T) {
	db := setup(t)
	c := providers.LocalCalendar{Pool: db.Pool}
	ctx := context.Background()
	e1 := form("lead1", "+5565999999991")
	e2 := form("lead2", "+5565999999992")
	submit(t, db, e1, "shadow")
	submit(t, db, e2, "shadow")
	start := time.Now().Add(48 * time.Hour).Truncate(time.Minute)
	slot := providers.LocalSlot{Slot: domain.Slot{ID: "slot-1", Provider: "local", Start: start, End: start.Add(45 * time.Minute), CloserID: 7}, MeetingURL: "https://meet.example/test"}
	if err := c.Add(ctx, slot); err != nil {
		t.Fatal(err)
	}
	overlap := slot
	overlap.ID = "overlap"
	overlap.Start = slot.Start.Add(time.Minute)
	if err := c.Add(ctx, overlap); err == nil {
		t.Fatal("overlapping slots accepted")
	}
	s1 := domain.State{ID: e1.LeadID, Profile: *e1.Form}
	s2 := domain.State{ID: e2.LeadID, Profile: *e2.Form}
	var wins atomic.Int32
	var wg sync.WaitGroup
	var won domain.State
	var booking domain.Booking
	var mu sync.Mutex
	for i, s := range []domain.State{s1, s2} {
		wg.Add(1)
		go func(i int, s domain.State) {
			defer wg.Done()
			b, err := c.Book(ctx, fmt.Sprintf("book-%d", i), s, slot.Slot, nil)
			if err == nil {
				wins.Add(1)
				mu.Lock()
				won = s
				booking = b
				mu.Unlock()
			}
		}(i, s)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("double booking: %d winners", wins.Load())
	}
	second := slot
	second.ID = "slot-2"
	second.Start = start.Add(24 * time.Hour)
	second.End = second.Start.Add(45 * time.Minute)
	if err := c.Add(ctx, second); err != nil {
		t.Fatal(err)
	}
	newBooking, err := c.Book(ctx, "reschedule", won, second.Slot, &booking)
	if err != nil {
		t.Fatal(err)
	}
	if newBooking.CloserID != booking.CloserID {
		t.Fatal("closer changed")
	}
	again, err := c.Book(ctx, "reschedule", won, second.Slot, &booking)
	if err != nil || again.ID != newBooking.ID {
		t.Fatal("booking idempotency failed")
	}
	available, err := c.Slots(ctx, won, time.Now())
	if err != nil || len(available) != 1 || available[0].ID != slot.ID {
		t.Fatalf("old reservation not released: %+v %v", available, err)
	}
	if err = c.Cancel(ctx, "cancel", newBooking); err != nil {
		t.Fatal(err)
	}
	if err = c.Cancel(ctx, "cancel", newBooking); err != nil {
		t.Fatal("cancel not idempotent")
	}
}

func TestLiveDeliveryAndUncertainReceipt(t *testing.T) {
	db := setup(t)
	e := form("live-form", "+5565999999999")
	submit(t, db, domain.Event{ID: "own-first", LeadID: e.LeadID, At: time.Now(), Kind: "ownership.changed", Owner: "go"}, "live")
	submit(t, db, e, "live")
	w := baseWorker(db)
	m := &countingMessenger{Err: &providers.Failure{Code: "transport_after_send", Ambiguous: true}}
	w.Live = true
	w.Messenger = m
	drain(t, w)
	if m.Count.Load() != 1 {
		t.Fatal("ambiguous send retried")
	}
	s, err := db.Lead(context.Background(), e.LeadID)
	if err != nil || s.FirstContactAt != nil || !s.Human {
		t.Fatal("unknown delivery recorded as success")
	}
	var n int
	_ = db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.crm_notes WHERE purpose='handoff'`).Scan(&n)
	if n != 1 {
		t.Fatal("missing durable review task")
	}
}
