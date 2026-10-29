package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/voiceworker"
)

type greetingConfirmationPlanner struct{}

func (greetingConfirmationPlanner) Plan(context.Context, domain.State, domain.Event, []domain.Slot) (domain.Decision, error) {
	return domain.Decision{Action: "confirm", Message: "Oi! Pode falar comigo por aqui.", Evidence: `current_message="pá\nopa" and meeting_status="upcoming"`}, nil
}

func TestGreetingKeepsAllDemoRemindersAvailable(t *testing.T) {
	db, srv, s := demoFixture(t)
	ctx := context.Background()
	s.Profile.SMSConsent = true
	s.Profile.SMSPhone = s.Profile.Phone
	s.Profile.SMSConsentVersion = "test-form-sms-v1"
	s.Stage = "booked"
	publishEffects(t, db, "sms-enabled", s)
	w := runtimeWorker(db, time.Now(), &countingMessenger{})
	w.Now = nil
	w.Planner = greetingConfirmationPlanner{}
	now := time.Now()
	submit(t, db, inbound("greeting-a", s.ID, "pá", now), "live")
	submit(t, db, inbound("greeting-b", s.ID, "opa", now.Add(time.Millisecond)), "live")
	window := fastWindowPolicy()
	ingest(t, db, window, now.Add(2*time.Millisecond))
	frozen(t, db, window, now.Add(2*time.Second))
	drain(t, w)
	ingest(t, db, window, now.Add(3*time.Second))
	current, err := db.Lead(ctx, s.ID)
	if err != nil || current.Human || current.Paused || !current.Profile.SMSConsent || current.Booking.ConfirmedAt != nil {
		t.Fatal("greeting changed contact control or presence", current, err)
	}
	srv.SMSEnabled = true
	base := "/webhooks/test-form/" + srv.TestFormToken
	body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
	if r := demoCall(srv.Handler(), "POST", base+"/test-status", "", body); r.Code != 200 || !strings.Contains(r.Body.String(), `"sms_consent":true`) || !strings.Contains(r.Body.String(), `"contact_blocked":false`) {
		t.Fatal(r.Code, r.Body)
	}
	for _, action := range []string{"audio", "sms", "call"} {
		r := demoCall(srv.Handler(), "POST", base+"/reminders/"+action, "greeting-"+action, body)
		if r.Code != 202 {
			t.Fatal("greeting blocked reminder API", action, r.Code, r.Body)
		}
	}
	posts, audio, sms := 0, 0, 0
	mock := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("unexpected provider read")
			return
		}
		_ = r.ParseForm()
		posts++
		if r.Form.Has("MediaUrl") {
			audio++
		}
		if !strings.HasPrefix(r.Form.Get("To"), "whatsapp:") {
			sms++
		}
		fmt.Fprintf(out, `{"sid":%q,"status":"sent"}`, "SM"+fmt.Sprintf("%032d", posts))
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "test", AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL}
	w.Messenger, w.AudioSender = tw, tw
	w.SMSSender = &providers.TwilioSMS{Transport: tw, From: "+18572902049"}
	w.VoiceSender = voiceSender
	fw := followupworker.New(*w)
	fw.Audio, fw.VoiceEnabled = srv.ReminderAudio, true
	drainRole(t, fw)
	drainRole(t, mediaworker.Outbound(*w, srv.ReminderAudio))
	if posts != 3 || audio != 1 || sms != 1 {
		t.Fatal("missing or duplicate reminder delivery", posts, audio, sms)
	}
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM bia_go.voice_requests`).Scan(&id); err != nil {
		t.Fatal("call request missing", err)
	}
	repo := voiceworker.Repository{DB: db}
	provider := &fakeVoice{}
	vw := &voiceworker.Worker{Repo: repo, Provider: provider, Agenda: &reminderAgenda{}, Policy: w.Policy, Live: true}
	step(t, vw, true)
	if provider.permissions != 1 || provider.dials != 0 {
		t.Fatal("call did not wait for permission", provider)
	}
	request, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Permission(ctx, "greeting-call-approval", s.Profile.Phone, voiceSender, true, false, request.Created); err != nil {
		t.Fatal(err)
	}
	step(t, vw, true)
	step(t, vw, false)
	if provider.dials != 1 {
		t.Fatal("missing or duplicate call", provider.dials)
	}
}
