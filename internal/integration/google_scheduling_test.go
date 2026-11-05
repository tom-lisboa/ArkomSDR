package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/dualcalendar"
	"arkom/bia/internal/googlecalendar"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
	"arkom/bia/internal/store"
	"golang.org/x/oauth2"
)

type calendarDouble struct {
	mu                     sync.Mutex
	events                 map[string]googlecalendar.Event
	busy                   map[string][]googlecalendar.Interval
	loseInsert, loseDelete bool
	inserts                int
}

func (g *calendarDouble) RoundTrip(r *http.Request) (*http.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Host != "www.googleapis.com" {
		return nil, errors.New("unexpected Google host")
	}
	status, body := 200, any(map[string]any{})
	parts := strings.Split(r.URL.Path, "/")
	switch {
	case strings.Contains(r.URL.Path, "/calendarList/"):
		body = googlecalendar.Calendar{ID: parts[len(parts)-1], Summary: "Agenda de teste", AccessRole: "owner"}
	case r.URL.Path == "/calendar/v3/freeBusy":
		var q struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			return nil, err
		}
		result := map[string]any{}
		for _, item := range q.Items {
			result[item.ID] = map[string]any{"busy": g.busy[item.ID]}
		}
		body = map[string]any{"calendars": result}
	case r.Method == http.MethodPost && len(parts) == 6 && parts[5] == "events":
		var event googlecalendar.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			return nil, err
		}
		if r.URL.Query().Get("sendUpdates") != "none" {
			return nil, errors.New("unexpected notification mode")
		}
		key := parts[4] + "/" + event.ID
		if _, found := g.events[key]; found {
			status = 409
			break
		}
		g.events[key] = event
		g.inserts++
		body = event
		if g.loseInsert {
			g.loseInsert = false
			return nil, io.ErrUnexpectedEOF
		}
	case len(parts) == 7 && parts[5] == "events":
		key := parts[4] + "/" + parts[6]
		event, found := g.events[key]
		if !found {
			status = 404
			break
		}
		if r.Method == http.MethodDelete {
			delete(g.events, key)
			status = 204
			if g.loseDelete {
				g.loseDelete = false
				return nil, io.ErrUnexpectedEOF
			}
		} else {
			body = event
		}
	default:
		return nil, errors.New("unexpected Google operation")
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
}

func googleSchedulingFixture(t *testing.T, db *store.DB) (*dualcalendar.Service, *calendarDouble) {
	t.Helper()
	client, err := googlecalendar.New(googlecalendar.Config{Enabled: true, ClientID: "test", ClientSecret: "test", RedirectURL: googlecalendar.DefaultRedirectURL, TokenKey: strings.Repeat("k", 64)})
	if err != nil {
		t.Fatal(err)
	}
	fake := &calendarDouble{events: map[string]googlecalendar.Event{}, busy: map[string][]googlecalendar.Interval{}}
	client.HTTP.Transport = fake
	a := providers.DailyAvailability{Enabled: true, TimeZone: "America/Cuiaba", Windows: "08:00-12:00,14:00-20:00", DurationMinutes: 45, Days: 3, CloserID: 1}
	return &dualcalendar.Service{Pool: db.Pool, Google: client, Availability: a}, fake
}

func addSchedulingAccount(t *testing.T, db *store.DB, service *dualcalendar.Service, user, id string) {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	raw, _ := json.Marshal(oauth2.Token{AccessToken: "test-" + id, RefreshToken: "refresh-" + id, TokenType: "Bearer", Expiry: expiry})
	cipher, err := service.Google.Seal("account:"+user+":"+id, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(context.Background(), `INSERT INTO bia_go.google_accounts(id,user_id,client_id,google_subject,credential,scopes,expires_at,connected_at,updated_at) VALUES($1,$2,$3,$1,$4,$5,$6,now(),now())`, id, user, service.Google.Config.ClientID, cipher, []string{googlecalendar.CalendarListScope, googlecalendar.EventsScope, googlecalendar.FreeBusyScope}, expiry)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInternalGoogleCalendarRoutesByLoadAndRecoversProviderTimeouts(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	service, fake := googleSchedulingFixture(t, db)
	first := newPanel(t, db, now, "operator")
	second := newPanel(t, db, now, "admin")
	first.s.Calendar = service
	addSchedulingAccount(t, db, service, first.user.ID, "first-work")
	addSchedulingAccount(t, db, service, first.user.ID, "first-personal")
	addSchedulingAccount(t, db, service, second.user.ID, "second-work")
	destination := dualcalendar.Selection{AccountID: "first-work", CalendarID: "first-calendar"}
	profile := dualcalendar.Profile{Enabled: true, Destination: destination, Blocks: []dualcalendar.Selection{{AccountID: "first-personal", CalendarID: "first-personal-calendar"}}}
	if out := panelCall(t, first, "PUT", "/integrations/google/scheduling", profile, ""); out.Code != 200 || !strings.Contains(out.Body.String(), `"ready":true`) {
		t.Fatal(out.Code, out.Body)
	}
	foreign := dualcalendar.Profile{Enabled: true, Destination: dualcalendar.Selection{AccountID: "second-work", CalendarID: "second-calendar"}}
	if out := panelCall(t, first, "PUT", "/integrations/google/scheduling", foreign, ""); out.Code != 409 {
		t.Fatal("foreign calendar accepted", out.Code)
	}
	slots, err := service.Slots(ctx, domain.State{}, now)
	if err != nil || len(slots) < 3 {
		t.Fatal(slots, err)
	}
	fake.busy["first-personal-calendar"] = []googlecalendar.Interval{{Start: slots[0].Start, End: slots[0].End}}
	visible, err := service.Slots(ctx, domain.State{}, now)
	if err != nil || len(visible) == 0 || visible[0].ID == slots[0].ID {
		t.Fatal("personal calendar did not block the work agenda", err)
	}
	if err = service.Save(ctx, second.user.ID, foreign); err != nil {
		t.Fatal(err)
	}
	visible, err = service.Slots(ctx, domain.State{}, now)
	if err != nil || len(visible) < 3 || !visible[0].Start.Equal(slots[0].Start) || visible[0].CloserID == slots[0].CloserID {
		t.Fatal("other seller not considered", err)
	}
	secondCloser := visible[0].CloserID
	lead := testState("dual-first", now)
	publishEffects(t, db, "dual-seed-1", lead)
	fake.loseInsert = true
	if _, err = service.Book(ctx, "dual-create", lead, visible[0], nil); err == nil {
		t.Fatal("confirmed a lost response")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.bookings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("premature local confirmation", count, err)
	}
	booked, err := service.Book(ctx, "dual-create", lead, visible[0], nil)
	if err != nil || !booked.GoogleSynced || booked.CloserID != secondCloser || fake.inserts != 1 {
		t.Fatal("lost event not recovered", booked, err, fake.inserts)
	}
	lead2 := testState("dual-second", now)
	publishEffects(t, db, "dual-seed-2", lead2)
	// Use the same timestamp originally shown for another seller; assignment is re-evaluated at booking.
	b2, err := service.Book(ctx, "dual-create-2", lead2, visible[1], nil)
	if err != nil || b2.CloserID == secondCloser {
		t.Fatal("least-loaded seller was not selected", b2, err)
	}
	lead.Booking = &booked
	choices, err := service.Slots(ctx, lead, now)
	if err != nil || len(choices) == 0 || choices[0].CloserID != secondCloser {
		t.Fatal("reschedule lost assigned seller", err)
	}
	fake.loseDelete = true
	if _, err = service.Book(ctx, "dual-move", lead, choices[0], &booked); err == nil {
		t.Fatal("reschedule confirmed before old calendar reconciled")
	}
	moved, err := service.Book(ctx, "dual-move", lead, choices[0], &booked)
	if err != nil || moved.ID == booked.ID || len(fake.events) != 2 || fake.inserts != 3 {
		t.Fatal("reschedule retry inconsistent", moved, err, fake.inserts, len(fake.events))
	}
	fake.loseDelete = true
	if err = service.Cancel(ctx, "dual-cancel", moved); err == nil {
		t.Fatal("cancel confirmed after lost response")
	}
	if err = service.Cancel(ctx, "dual-cancel", moved); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.bookings WHERE cancelled_at IS NULL`).Scan(&count); err != nil || count != 1 || len(fake.events) != 1 {
		t.Fatal("cancellation not applied in both calendars", count, err)
	}
}

func TestInternalGoogleFormBookingWaitsForSchedulingWorker(t *testing.T) {
	db := setup(t)
	db.DualCalendar = true
	ctx := context.Background()
	now := time.Now()
	service, fake := googleSchedulingFixture(t, db)
	seller := newPanel(t, db, now, "operator")
	addSchedulingAccount(t, db, service, seller.user.ID, "work")
	if err := service.Save(ctx, seller.user.ID, dualcalendar.Profile{Enabled: true, Destination: dualcalendar.Selection{AccountID: "work", CalendarID: "work-calendar"}}); err != nil {
		t.Fatal(err)
	}
	cal := &providers.LocalCalendar{Pool: db.Pool, External: service}
	available, err := cal.Slots(ctx, domain.State{}, now)
	if err != nil || len(available) == 0 {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 48)
	base := "/webhooks/test-form/" + key
	h := (&httpapi.Server{DB: db, Calendar: cal, TestFormToken: key, VoiceSender: voiceSender, Mode: "live", FormAutoContact: true, Policy: policy()}).Handler()
	phone := "+5577999749058"
	request, _ := json.Marshal(map[string]any{"name": "Ana", "company": "Exemplo", "phone": phone, "revenue": 5000, "consent": true, "slot_id": available[0].ID})
	worker := runtimeWorker(db, now, &countingMessenger{})
	scheduler := schedulingworker.New(*worker, cal)
	response := make(chan struct {
		Code int
		Body string
	}, 1)
	go func() {
		out := calendarCall(h, "POST", base, "dual-web", string(request))
		response <- struct {
			Code int
			Body string
		}{out.Code, out.Body.String()}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var exists bool
		if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE id='booking-event/form-booking:dual-web')`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-response:
		t.Fatal("form returned before the worker ran")
	default:
	}
	step(t, worker, true)
	step(t, scheduler, true)
	step(t, scheduler, true)
	out := <-response
	if out.Code != 202 || !strings.Contains(out.Body, `"google_synced":true`) || fake.inserts != 1 {
		t.Fatal(out, fake.inserts)
	}
	state, err := db.Lead(ctx, domain.LeadKey(phone))
	if err != nil || state.Booking == nil || !state.Booking.GoogleSynced {
		t.Fatal("opening cannot see confirmed meeting", err)
	}
	status := calendarCall(h, "POST", base+"/test-status", "", `{"submission_id":"dual-web","phone":"`+phone+`"}`)
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"booking_status":"booked"`) {
		t.Fatal(status.Code, status.Body)
	}
}
