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
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
)

func demoFixture(t *testing.T) (*store.DB, *httpapi.Server, domain.State) {
	t.Helper()
	db := setup(t)
	now := time.Now().UTC()
	s := testState(domain.LeadKey("+5565999999900"), now)
	s.LastInbound = now
	s.Booking = &domain.Booking{ID: "demo-booking", Start: now.Add(48 * time.Hour), End: now.Add(48*time.Hour + 45*time.Minute)}
	publishEffects(t, db, "demo-seed", s)
	if _, _, err := db.RegisterTestForm(context.Background(), "demo-form", s.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.test_forms SET claimed_by=$1 WHERE id='demo-form'`, s.ID); err != nil {
		t.Fatal(err)
	}
	srv := &httpapi.Server{DB: db, TestFormToken: strings.Repeat("d", 32), Mode: "live", VoiceEnabled: true, VoiceSender: voiceSender, ReminderAudio: reminderAsset(t), Policy: policy()}
	return db, srv, s
}

func demoCall(h http.Handler, method, path, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", id)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDemoReminderAPIIdempotencyAndBinding(t *testing.T) {
	db, srv, s := demoFixture(t)
	h := srv.Handler()
	base := "/webhooks/test-form/" + srv.TestFormToken + "/reminders/"
	body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
	for _, tc := range []struct {
		method, path, id, body string
		code                   int
	}{
		{"POST", "/webhooks/test-form/wrong/reminders/audio", "one", body, 404},
		{"OPTIONS", base + "audio", "", "", 204},
		{"POST", base + "bad", "one", body, 404},
		{"POST", base + "audio", "", body, 422},
		{"POST", base + "audio", "one", `{"phone":"+5565999999902","submission_id":"demo-form"}`, 409},
		{"POST", base + "audio", "one", `{"phone":"+5565999999900","submission_id":"missing"}`, 404},
		{"POST", base + "audio", "one", `{"phone":"+5565999999900","submission_id":"demo-form","force":true}`, 400},
	} {
		if w := demoCall(h, tc.method, tc.path, tc.id, tc.body); w.Code != tc.code {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	// Concurrent network retries have exactly one committed job.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := demoCall(h, "POST", base+"audio", "one", body); w.Code != 202 {
				t.Errorf("%d %s", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	var n int
	db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE id='fx/demo:one'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	if w := demoCall(h, "POST", base+"audio", "one", body); !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatal(w.Body)
	}
	// Brazilian phone aliases must resolve to the same request fingerprint.
	if w := demoCall(h, "POST", base+"audio", "one", `{"phone":"+556599999900","submission_id":"demo-form"}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	if w := demoCall(h, "POST", base+"call", "one", body); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := demoCall(h, "POST", base+"audio", "new-click", body); w.Code != 429 {
		t.Fatal(w.Code, w.Body)
	}
	if w := demoCall(h, "GET", base+"audio/one", "", ""); w.Code != 200 || strings.Contains(w.Body.String(), s.Profile.Phone) {
		t.Fatal(w.Code, w.Body)
	}
	if w := demoCall(h, "GET", base+"call/one", "", ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestDemoReminderPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         func(*domain.State, *httpapi.Server)
		action, reason string
		code           int
	}{
		{"no_booking", func(s *domain.State, _ *httpapi.Server) { s.Booking = nil }, "call", "future_booking_required", 409},
		{"past_booking", func(s *domain.State, _ *httpapi.Server) { s.Booking.Start = time.Now().Add(-time.Minute) }, "call", "future_booking_required", 409},
		{"closed_session", func(s *domain.State, _ *httpapi.Server) { s.LastInbound = time.Now().Add(-24 * time.Hour) }, "call", "whatsapp_reply_required", 409},
		{"optout", func(s *domain.State, _ *httpapi.Server) { s.OptedOut = true }, "call", "contact_blocked", 409},
		{"human", func(s *domain.State, _ *httpapi.Server) { s.Human = true }, "call", "contact_blocked", 409},
		{"paused", func(s *domain.State, _ *httpapi.Server) { s.Paused = true }, "call", "contact_blocked", 409},
		{"n8n", func(s *domain.State, _ *httpapi.Server) { s.Owner = "n8n" }, "call", "contact_blocked", 409},
		{"no_audio", func(_ *domain.State, s *httpapi.Server) { s.ReminderAudio.SHA256 = "" }, "audio", "reminder_audio_not_ready", 503},
		{"voice_off", func(_ *domain.State, s *httpapi.Server) { s.VoiceEnabled = false }, "call", "voice_not_enabled", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, srv, s := demoFixture(t)
			tc.change(&s, srv)
			raw, _ := json.Marshal(s)
			if _, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, s.ID, raw); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
			w := demoCall(srv.Handler(), "POST", "/webhooks/test-form/"+srv.TestFormToken+"/reminders/"+tc.action, "bad", body)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.reason) {
				t.Fatal(w.Code, w.Body)
			}
			var n int
			db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE id LIKE 'fx/demo:%'`).Scan(&n)
			if n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestDemoRemindersUseExistingWorkersImmediately(t *testing.T) {
	db, srv, s := demoFixture(t)
	ctx := context.Background()
	h := srv.Handler()
	base := "/webhooks/test-form/" + srv.TestFormToken + "/reminders/"
	body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
	posts, audios := 0, 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = r.ParseForm()
			posts++
			if r.Form.Get("MediaUrl") != "" {
				audios++
			} else if !strings.Contains(r.Form.Get("Body"), "permitir chamadas") {
				t.Error("missing call invitation")
			}
		}
		fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "MM"+fmt.Sprintf("%032d", posts))
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("1", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL}
	w := runtimeWorker(db, time.Now(), &countingMessenger{})
	w.Now = nil
	w.Messenger = tw
	w.AudioSender = tw
	w.VoiceSender = voiceSender
	w.Policy.MeetingReminders = false
	w.Policy.DisableFollowups = true
	// No automatic contact hour is open; an explicit demo click still runs now.
	w.Policy.QuietStart = 0
	w.Policy.QuietEnd = 0
	fw := followupworker.New(*w)
	fw.Audio = srv.ReminderAudio
	fw.VoiceEnabled = true
	mw := mediaworker.Outbound(*w, srv.ReminderAudio)
	for _, action := range []string{"audio", "call"} {
		if resp := demoCall(h, "POST", base+action, action, body); resp.Code != 202 {
			t.Fatal(resp.Code, resp.Body)
		}
		step(t, fw, true)
		if action == "audio" {
			step(t, mw, true)
		} else {
			step(t, fw, true)
		}
	}
	if posts != 2 || audios != 1 {
		t.Fatal(posts, audios)
	}
	requestID := "meeting:" + domain.LeadKey("fx/demo:call:permission")[5:]
	repo := voiceworker.Repository{DB: db}
	req, err := repo.Get(ctx, requestID)
	if err != nil || !req.ManualReminder || req.ScheduledReminder || req.Expires.After(time.Now().Add(11*time.Minute)) {
		t.Fatal(req, err)
	}
	provider := &fakeVoice{}
	vw := &voiceworker.Worker{Repo: repo, Provider: provider, Agenda: &reminderAgenda{}, Policy: w.Policy, Live: true}
	step(t, vw, true)
	if provider.permissions != 1 || provider.dials != 0 {
		t.Fatal("dialed before native permission", provider)
	}
	if resp := demoCall(h, "GET", base+"call/call", "", ""); resp.Code != 200 || !strings.Contains(resp.Body.String(), "awaiting_permission") {
		t.Fatal(resp.Code, resp.Body)
	}
	if err = repo.Permission(ctx, "demo-accept", s.Profile.Phone, voiceSender, true, false, req.Created.Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}
	step(t, vw, true)
	step(t, vw, false)
	if provider.dials != 1 {
		t.Fatal(provider)
	}
	// Retries after dispatch do not create a second audio, invitation or call.
	for _, action := range []string{"audio", "call"} {
		if resp := demoCall(h, "POST", base+action, action, body); resp.Code != 202 {
			t.Fatal(resp.Code, resp.Body)
		}
	}
	step(t, fw, false)
	step(t, mw, false)
	updated, err := db.Lead(ctx, s.ID)
	if err != nil || updated.Booking.ID != s.Booking.ID || !updated.Booking.Start.Equal(s.Booking.Start.Truncate(time.Microsecond)) {
		t.Fatal("booking changed", err)
	}
	if posts != 2 || provider.dials != 1 {
		t.Fatal(posts, provider)
	}
}

func TestDemoReminderRechecksBookingAndShadow(t *testing.T) {
	for _, mode := range []string{"changed_booking", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			db, srv, s := demoFixture(t)
			if mode == "shadow" {
				srv.Mode = "shadow"
			}
			body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
			if resp := demoCall(srv.Handler(), "POST", "/webhooks/test-form/"+srv.TestFormToken+"/reminders/call", "check", body); resp.Code != 202 {
				t.Fatal(resp.Code, resp.Body)
			}
			if mode == "changed_booking" {
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
				raw, _ := json.Marshal(s)
				_, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, s.ID, raw)
				if err != nil {
					t.Fatal(err)
				}
			}
			w := runtimeWorker(db, time.Now(), &countingMessenger{})
			fw := followupworker.New(*w)
			fw.VoiceEnabled = true
			step(t, fw, true)
			want := "skipped"
			if mode == "shadow" {
				want = "simulated"
			}
			assertJob(t, db, "fx/demo:check", "followup", want)
			var n int
			db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.voice_requests`).Scan(&n)
			if n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestDemoReminderPermissionLimitAndUncertainAudio(t *testing.T) {
	t.Run("permission_limit", func(t *testing.T) {
		db, srv, s := demoFixture(t)
		repo := voiceworker.Repository{DB: db}
		if _, err := repo.Create(context.Background(), "prior-call", s.ID, "live", voiceSender, "confirm"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.voice_requests SET state='completed',permission_sid='SM11111111111111111111111111111111' WHERE id='prior-call'`); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"phone":%q,"submission_id":"demo-form"}`, s.Profile.Phone)
		resp := demoCall(srv.Handler(), "POST", "/webhooks/test-form/"+srv.TestFormToken+"/reminders/call", "limit", body)
		if resp.Code != 429 || !strings.Contains(resp.Body.String(), "permission_request_limit") {
			t.Fatal(resp.Code, resp.Body)
		}
	})
	t.Run("uncertain_media", func(t *testing.T) {
		db, srv, s := demoFixture(t)
		ctx := context.Background()
		if _, _, err := db.QueueTestReminder(ctx, "audio-one", "demo-form", s.Profile.Phone, "audio", "live", true, true, false); err != nil {
			t.Fatal(err)
		}
		w := runtimeWorker(db, time.Now(), &countingMessenger{})
		fw := followupworker.New(*w)
		fw.Audio = srv.ReminderAudio
		step(t, fw, true)
		if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET created_at=now()-interval '2 minutes',status=CASE WHEN id LIKE '%:media' THEN 'uncertain' ELSE status END WHERE id LIKE 'fx/demo:%'`); err != nil {
			t.Fatal(err)
		}
		_, _, err := db.QueueTestReminder(ctx, "audio-two", "demo-form", s.Profile.Phone, "audio", "live", true, true, false)
		if err == nil || err.Error() != "test_trigger_rate_limit" {
			t.Fatal(err)
		}
	})
}
