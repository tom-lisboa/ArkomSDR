package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/providers"
)

func TestSMSAtFiveMinutesOutsideWhatsAppWindow(t *testing.T) {
	db, srv, s := demoFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.Profile.SMSConsent = true
	s.Profile.SMSConsentVersion = "test-sms-v1"
	s.Profile.SMSPhone = s.Profile.Phone
	s.LastInbound = time.Time{}
	s.Booking.Start = now.Add(6 * time.Minute)
	s.Booking.End = s.Booking.Start.Add(45 * time.Minute)
	raw, _ := json.Marshal(s)
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, s.ID, raw); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.ReconcileSMSReminders(ctx, now, true); err != nil {
			t.Fatal(err)
		}
	}
	fx := domain.SMSSequence(s, now)[0]
	posts := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Method == "POST" {
			posts++
			if r.Form.Get("To") != s.Profile.SMSPhone || r.Form.Get("From") != "+18572902049" || r.Form.Get("ValidityPeriod") != "300" || r.Form.Has("ContentSid") || !strings.Contains(r.Form.Get("Body"), "sua reuniao") {
				t.Error(r.Form)
			}
		}
		fmt.Fprint(w, `{"sid":"SM11111111111111111111111111111111","status":"sent"}`)
	}))
	defer mock.Close()
	tw := &providers.TwilioSMS{From: "+18572902049", Transport: &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL, PollInterval: time.Millisecond}}
	w := runtimeWorker(db, now, &countingMessenger{})
	w.SMSSender = tw
	w.Policy.SMSReminders = true
	w.Policy.MeetingReminders = false
	w.Policy.DisableFollowups = true
	w.Policy.QuietStart = 0
	w.Policy.QuietEnd = 0
	w.Policy.WhatsAppPhone = voiceSender
	fw := followupworker.New(*w)
	step(t, fw, false)
	clock := fx.Due
	w.Now = func() time.Time { return clock }
	fw = followupworker.New(*w)
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, fx.ID); err != nil {
		t.Fatal(err)
	}
	step(t, fw, true)
	assertJob(t, db, fx.ID, "followup", "done")
	if err := db.ReconcileSMSReminders(ctx, clock, true); err != nil {
		t.Fatal(err)
	}
	step(t, followupworker.New(*w), false)
	if posts != 1 {
		t.Fatal(posts)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.sms_messages WHERE status='sent'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	lead, global, err := db.Counts(ctx, s.ID)
	if err != nil || lead != 1 || global != 1 {
		t.Fatal(lead, global, err)
	}
	// The demo endpoint uses the same channel and requires explicit SMS consent,
	// but does not require a WhatsApp session or the automatic cadence flag.
	srv.SMSEnabled = true
	body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
	resp := demoCall(srv.Handler(), "POST", "/webhooks/test-form/"+srv.TestFormToken+"/reminders/sms", "sms-button", body)
	if resp.Code != 202 {
		t.Fatal(resp.Code, resp.Body)
	}
	w.Policy.SMSReminders = false
	clock = time.Now()
	step(t, followupworker.New(*w), true)
	if posts != 2 {
		t.Fatal(posts)
	}
}

func TestSMSUncertainSendDoesNotRepeatOrStopWhatsApp(t *testing.T) {
	db, _, s := demoFixture(t)
	ctx := context.Background()
	now := time.Now()
	s.Profile.SMSConsent = true
	s.Profile.SMSPhone = s.Profile.Phone
	s.Profile.SMSConsentVersion = "test"
	s.Booking.Start = now.Add(4 * time.Minute)
	fx := domain.SMSSequence(s, now)[0]
	publishEffects(t, db, "seed-sms-uncertain", s, fx)
	posts := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(500)
		fmt.Fprint(w, `{"code":20500}`)
	}))
	defer mock.Close()
	tw := &providers.TwilioSMS{From: "+18572902049", Transport: &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL}}
	w := runtimeWorker(db, now, &countingMessenger{})
	w.SMSSender = tw
	w.Policy.SMSReminders = true
	step(t, followupworker.New(*w), true)
	assertJob(t, db, fx.ID, "followup", "uncertain")
	got, err := db.Lead(ctx, s.ID)
	if err != nil || got.Human {
		t.Fatal("SMS failure stopped WhatsApp", err)
	}
	_, err = tw.SendSMS(ctx, fx.ID, s, domain.Render(s, fx, w.Policy, now), now, fx.Expires)
	if err == nil || posts != 1 {
		t.Fatal(err, posts)
	}
}

func TestSMSGuardAtDelivery(t *testing.T) {
	for _, change := range []string{"consent", "cancel", "reschedule", "shadow"} {
		t.Run(change, func(t *testing.T) {
			db, _, s := demoFixture(t)
			ctx := context.Background()
			now := time.Now()
			s.Profile.SMSConsent = true
			s.Profile.SMSPhone = s.Profile.Phone
			s.Profile.SMSConsentVersion = "test"
			s.Booking.Start = now.Add(4 * time.Minute)
			fx := domain.SMSSequence(s, now)[0]
			publishEffects(t, db, "seed-sms-guard", s, fx)
			switch change {
			case "consent":
				s.Profile.SMSConsent = false
			case "cancel":
				s.Booking = nil
			case "reschedule":
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
			case "shadow":
				if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET mode='shadow' WHERE id=$1`, fx.ID); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(s)
			if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, s.ID, raw); err != nil {
				t.Fatal(err)
			}
			posts := 0
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++ }))
			defer mock.Close()
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Policy.SMSReminders = true
			w.SMSSender = &providers.TwilioSMS{From: "+18572902049", Transport: &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL}}
			step(t, followupworker.New(*w), true)
			want := "skipped"
			if change == "shadow" {
				want = "simulated"
			}
			assertJob(t, db, fx.ID, "followup", want)
			if posts != 0 {
				t.Fatal(posts)
			}
		})
	}
}

func TestSMSFormConsentRetainsOriginalNumber(t *testing.T) {
	db, srv, _ := demoFixture(t)
	ctx := context.Background()
	srv.FormAutoContact = true
	// Existing Twilio identity has no ninth digit.
	canonical := "+556599999900"
	s := testState(domain.LeadKey(canonical), time.Now())
	s.Profile.Phone = canonical
	s.Profile.ConversationID = "twilio:" + voiceSender + ":" + canonical
	if _, err := db.Pool.Exec(ctx, `DELETE FROM bia_go.test_forms; DELETE FROM bia_go.jobs; DELETE FROM bia_go.leads`); err != nil {
		t.Fatal(err)
	}
	publishEffects(t, db, "seed-sms-alias", s)
	for _, consent := range []bool{true, false} {
		id := fmt.Sprintf("sms-form-%t", consent)
		body := fmt.Sprintf(`{"name":"Teste","phone":"+5565999999900","consent":true,"sms_consent":%t}`, consent)
		resp := demoCall(srv.Handler(), "POST", "/webhooks/test-form/"+srv.TestFormToken, id, body)
		if resp.Code != 202 {
			t.Fatal(resp.Code, resp.Body)
		}
		j, err := db.Job(ctx, "form-contact-"+id)
		if err != nil {
			t.Fatal(err)
		}
		var e domain.Event
		if err = json.Unmarshal(j.Payload, &e); err != nil {
			t.Fatal(err)
		}
		if e.Form.Phone != canonical || e.Form.SMSConsent != consent {
			t.Fatal(e.Form)
		}
		if consent && domain.SMSDestination(*e.Form) != "+5565999999900" {
			t.Fatal("original number lost", e.Form)
		}
	}
}
