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

func TestMeetingStartUsesTemplateOrSessionAndNeverRepeatsAfterRestart(t *testing.T) {
	for _, session := range []bool{false, true} {
		t.Run(fmt.Sprint(session), func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("start-contact", now)
			s.Booking = &domain.Booking{ID: "start-booking", Start: now.Add(-time.Minute), End: now.Add(44 * time.Minute)}
			if session {
				s.LastInbound = now.Add(-time.Minute)
			}
			publishEffects(t, db, "seed-start", s)
			for range 2 {
				if err := db.ReconcileMeetingStart(ctx, now, true); err != nil {
					t.Fatal(err)
				}
			}
			fx := domain.MeetingStartSequence(s, now)[0]
			assertJob(t, db, fx.ID, "followup", "pending")
			posts := 0
			approved := session
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "ApprovalRequests") {
					status := "pending"
					if approved {
						status = "approved"
					}
					fmt.Fprintf(w, `{"whatsapp":{"status":%q}}`, status)
					return
				}
				if r.Method != "POST" {
					t.Error("unexpected read")
					return
				}
				posts++
				_ = r.ParseForm()
				if session {
					if r.Form.Get("Body") != domain.MeetingStartOpening(s, policy()).Text || r.Form.Has("ContentSid") {
						t.Error("wrong session text", r.Form)
					}
				} else {
					if r.Form.Get("ContentSid") != "HX"+strings.Repeat("a", 32) || r.Form.Has("Body") || r.Form.Has("MediaUrl") {
						t.Error("wrong outside-session template", r.Form)
					}
					var variables map[string]string
					_ = json.Unmarshal([]byte(r.Form.Get("ContentVariables")), &variables)
					if variables["3"] != "45" || variables["1"] != "Teste" {
						t.Error("wrong booking facts", variables)
					}
				}
				fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "SM"+strings.Repeat("b", 32))
			}))
			defer mock.Close()
			tw := &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, Account: "AC" + strings.Repeat("c", 32), AuthToken: "test", Sender: voiceSender, MeetingStartContentSID: "HX" + strings.Repeat("a", 32)}
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Policy.MeetingStart = true
			w.Policy.DisableFollowups = true
			w.Messenger = tw
			step(t, w, false)
			step(t, followupworker.New(*w), true)
			if !session {
				assertJob(t, db, fx.ID, "followup", "pending")
				if posts != 0 {
					t.Fatal("sent pending template")
				}
				approved = true
				if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, fx.ID); err != nil {
					t.Fatal(err)
				}
				step(t, followupworker.New(*w), true)
			}
			assertJob(t, db, fx.ID, "followup", "done")
			if err := db.ReconcileMeetingStart(ctx, now.Add(time.Minute), true); err != nil {
				t.Fatal(err)
			}
			step(t, followupworker.New(*w), false)
			if posts != 1 {
				t.Fatal("T0 repeated", posts)
			}
			var calls int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.voice_requests`).Scan(&calls); err != nil || calls != 0 {
				t.Fatal("T0 created an unauthorized call", calls, err)
			}
		})
	}
}

func TestMeetingStartRechecksContactBookingAndExpiry(t *testing.T) {
	for _, condition := range []string{"disabled", "cancelled", "rescheduled", "paused", "optout", "human", "no_consent", "expired", "attended", "provider_error"} {
		t.Run(condition, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("guard-start", now)
			s.LastInbound = now
			s.Booking = &domain.Booking{ID: "guard-booking", Start: now.Add(-time.Minute), End: now.Add(44 * time.Minute)}
			fx := domain.MeetingStartSequence(s, now)[0]
			switch condition {
			case "cancelled":
				s.Booking = nil
			case "rescheduled":
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
			case "paused":
				s.Paused = true
			case "optout":
				s.OptedOut = true
			case "human":
				s.Human = true
			case "no_consent":
				s.Profile.Consent = false
			case "attended":
				s.Booking.Outcome = "attended"
			}
			publishEffects(t, db, "seed-guard-start", s, fx)
			posts := 0
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":20500}`)
			}))
			defer mock.Close()
			tw := &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL, Account: "AC" + strings.Repeat("d", 32), AuthToken: "test", Sender: voiceSender}
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Policy.MeetingStart = condition != "disabled"
			w.Messenger = tw
			if condition == "expired" {
				w.Now = func() time.Time { return fx.Expires }
			}
			step(t, followupworker.New(*w), true)
			want := "skipped"
			if condition == "provider_error" {
				want = "uncertain"
			}
			assertJob(t, db, fx.ID, "followup", want)
			if condition == "provider_error" {
				current, err := db.Lead(ctx, s.ID)
				if err != nil || current.Human || posts != 1 {
					t.Fatal("optional reminder stopped conversation", current.Human, posts, err)
				}
			} else if posts != 0 {
				t.Fatal("blocked stage sent", condition)
			}
		})
	}
}
