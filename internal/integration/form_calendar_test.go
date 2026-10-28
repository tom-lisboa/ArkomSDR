package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
)

func calendarCall(h http.Handler, method, path, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Idempotency-Key", id)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFormCalendarBookingAtomicAndVisibleToWorkers(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	cal := &providers.LocalCalendar{Pool: db.Pool}
	slot := domain.Slot{ID: "web-slot", Provider: "local", CloserID: 1, Start: now.Add(48 * time.Hour).Truncate(time.Minute), End: now.Add(48 * time.Hour).Truncate(time.Minute).Add(45 * time.Minute)}
	if err := cal.Add(ctx, providers.LocalSlot{Slot: slot}); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 48)
	base := "/webhooks/test-form/" + key
	p := policy()
	p.MeetingReminders = true
	p.SMSReminders = true
	h := (&httpapi.Server{DB: db, Calendar: cal, TestFormToken: key, VoiceSender: voiceSender, Mode: "live", FormAutoContact: true, Policy: p}).Handler()
	for _, suffix := range []string{"/availability", "/bookings"} {
		if r := calendarCall(h, "OPTIONS", base+suffix, "", ""); r.Code != 204 {
			t.Fatal(r.Code)
		}
	}
	if r := calendarCall(h, "GET", base+"/availability?timezone=America/Cuiaba", "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), slot.ID) || strings.Contains(r.Body.String(), "closer") {
		t.Fatal(r.Code, r.Body)
	}
	if r := calendarCall(h, "GET", base+"/availability?timezone=Wrong", "", ""); r.Code != 422 {
		t.Fatal(r.Code)
	}
	body := `{"name":"Ana","company":"Exemplo","phone":"+5577999749058","revenue":5000,"consent":true,"sms_consent":true,"slot_id":"web-slot"}`
	for range 2 {
		r := calendarCall(h, "POST", base, "form-web", body)
		if r.Code != 202 || !strings.Contains(r.Body.String(), `"booking":{"id":"local_`) {
			t.Fatal(r.Code, r.Body)
		}
	}
	if r := calendarCall(h, "POST", base, "form-web", strings.Replace(body, slot.ID, "other-slot", 1)); r.Code != 409 {
		t.Fatal(r.Code)
	}
	if r := calendarCall(h, "GET", base+"/availability", "", ""); r.Code != 200 || strings.Contains(r.Body.String(), slot.ID) {
		t.Fatal(r.Code, r.Body)
	}
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	w.Policy = p
	// Form event first; the booking event then blocks every effect across queues.
	step(t, w, true)
	if done, err := w.Once(ctx); err != nil || done {
		t.Fatal("contact sent before booking event", done, err)
	}
	sw := schedulingworker.New(*w, cal)
	if done, err := sw.Once(ctx); err != nil || !done {
		t.Fatal(done, err)
	}
	state, err := db.Lead(ctx, domain.LeadKey("+5577999749058"))
	if err != nil || state.Booking == nil || state.Stage != "booked" || state.Profile.Name != "Ana" || !state.Booking.Start.Equal(slot.Start) {
		t.Fatal(state, err)
	}
	data := domain.CustomerContext(state, state.Booking, p)
	raw, _ := json.Marshal(data)
	if !strings.Contains(string(raw), "data_reuniao") {
		t.Fatal(string(raw))
	}
	var jobs int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE payload->>'purpose' IN ('meeting_sms','meeting_audio','meeting_call')`).Scan(&jobs); err != nil || jobs != 3 {
		t.Fatal(jobs, err)
	}
	var bookings int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.bookings`).Scan(&bookings)
	if bookings != 1 {
		t.Fatal(bookings)
	}
	// Another submitter cannot take the occupied slot; failed activation leaves
	// no lead, event, delivery, or second reservation for that submission.
	loser := strings.Replace(body, "+5577999749058", "+5565999999900", 1)
	r := calendarCall(h, "POST", base, "form-loser", loser)
	if r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE id='form-contact-form-loser'`).Scan(&jobs)
	if jobs != 0 {
		t.Fatal("partial activation", jobs)
	}
}

func TestSeparateFormBookingConcurrencyBindingAndRetry(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	cal := &providers.LocalCalendar{Pool: db.Pool}
	slot := domain.Slot{ID: "race-slot", Provider: "local", CloserID: 1, Start: now.Add(72 * time.Hour), End: now.Add(72*time.Hour + 45*time.Minute)}
	if err := cal.Add(ctx, providers.LocalSlot{Slot: slot}); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 48)
	base := "/webhooks/test-form/" + key
	h := (&httpapi.Server{DB: db, Calendar: cal, TestFormToken: key, VoiceSender: voiceSender, Mode: "live", FormAutoContact: true, Policy: policy()}).Handler()
	phones := []string{"+5577999749058", "+5565999999900"}
	for i, phone := range phones {
		body := fmt.Sprintf(`{"name":"Teste","company":"Exemplo","phone":%q,"consent":true}`, phone)
		if r := calendarCall(h, "POST", base, fmt.Sprintf("form-%d", i), body); r.Code != 202 {
			t.Fatal(r.Code, r.Body)
		}
	}
	payload := func(i int) string {
		return fmt.Sprintf(`{"phone":%q,"submission_id":"form-%d","slot_id":"race-slot"}`, phones[i], i)
	}
	r := calendarCall(h, "POST", base+"/bookings", "mismatch", strings.Replace(payload(0), phones[0], phones[1], 1))
	if r.Code != 404 {
		t.Fatal(r.Code, r.Body)
	}
	// An active run cannot be overwritten by a site reservation.
	db.Pool.Exec(ctx, `UPDATE bia_go.leads SET lease_until=now()+interval '1 minute' WHERE id=$1`, domain.LeadKey(phones[0]))
	if r = calendarCall(h, "POST", base+"/bookings", "busy", payload(0)); r.Code != 409 || !strings.Contains(r.Body.String(), "contact_busy") {
		t.Fatal(r.Code, r.Body)
	}
	db.Pool.Exec(ctx, `UPDATE bia_go.leads SET lease_until=NULL WHERE id=$1`, domain.LeadKey(phones[0]))
	results := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = calendarCall(h, "POST", base+"/bookings", fmt.Sprintf("reserve-%d", i), payload(i))
		}(i)
	}
	wg.Wait()
	winner := -1
	for i, r := range results {
		if r.Code == 201 {
			if winner >= 0 {
				t.Fatal("two winners")
			}
			winner = i
		} else if r.Code != 409 {
			t.Fatal(r.Code, r.Body)
		}
	}
	if winner < 0 {
		t.Fatal("no winner")
	}
	id := fmt.Sprintf("reserve-%d", winner)
	// Alternate Brazilian spelling is the same contact/idempotency fingerprint.
	again := payload(winner)
	if winner == 0 {
		again = strings.Replace(again, phones[0], "+557799749058", 1)
	}
	for range 2 {
		r = calendarCall(h, "POST", base+"/bookings", id, again)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"duplicate":true`) {
			t.Fatal(r.Code, r.Body)
		}
	}
	if r = calendarCall(h, "POST", base+"/bookings", id, strings.Replace(again, "race-slot", "other-slot", 1)); r.Code != 409 || !strings.Contains(r.Body.String(), "idempotency_conflict") {
		t.Fatal(r.Code, r.Body)
	}
	if r = calendarCall(h, "POST", base+"/bookings", id, payload(1-winner)); r.Code != 409 || !strings.Contains(r.Body.String(), "idempotency_conflict") {
		t.Fatal(r.Code, r.Body)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.bookings`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	// New request IDs cannot silently reschedule the contact.
	second := slot
	second.ID = "second-slot"
	second.Start = slot.End.Add(time.Hour)
	second.End = second.Start.Add(45 * time.Minute)
	if err := cal.Add(ctx, providers.LocalSlot{Slot: second}); err != nil {
		t.Fatal(err)
	}
	if r = calendarCall(h, "POST", base+"/bookings", "different", strings.Replace(again, slot.ID, second.ID, 1)); r.Code != 409 || !strings.Contains(r.Body.String(), "lead_already_booked") {
		t.Fatal(r.Code, r.Body)
	}
}

func TestWebConfirmationWaitsForWhatsAppWithoutProviderCall(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	s := testState("waiting-web", now)
	s.LastInbound = time.Time{}
	s.Booking = &domain.Booking{ID: "reserved", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
	fx := domain.Effect{ID: "web-confirm", LeadID: s.ID, Kind: "send", Purpose: "booking_confirmation", Text: "Reunião confirmada.", BookingID: s.Booking.ID, BookingStart: s.Booking.Start, Due: now, Expires: s.Booking.Start}
	publishEffects(t, db, "seed-web", s, fx)
	transport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("closed window caused provider request")
		w.WriteHeader(500)
	}))
	defer transport.Close()
	w := runtimeWorker(db, now, &countingMessenger{})
	w.Queue = "scheduling"
	w.Messenger = &providers.TwilioMessaging{Pool: db.Pool, BaseURL: transport.URL, Client: transport.Client()}
	step(t, w, true)
	current, err := db.Lead(ctx, s.ID)
	if err != nil || current.Human {
		t.Fatal(current, err)
	}
	j, err := db.Job(ctx, fx.ID)
	if err != nil || j.Status != "pending" {
		t.Fatal(j, err)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.twilio_messages`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}
