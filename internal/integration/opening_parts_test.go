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
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/remindermedia"
)

func TestBookedOpeningHasTwoApprovedShortBubblesAndNoReplay(t *testing.T) {
	for _, goal := range []string{"Quero vender mais", ""} {
		t.Run(fmt.Sprint(goal != ""), func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now()
			s := testState("split-opening", now)
			s.Profile.Goal = goal
			s.LastInbound = time.Time{}
			s.Booking = &domain.Booking{ID: "booking", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
			fx := domain.Effect{ID: "fx/split-opening", LeadID: s.ID, Kind: "send", Purpose: "form_contact", Due: now, Expires: now.Add(time.Hour)}
			publishEffects(t, db, "seed", s, fx)
			mainSID := "HX" + strings.Repeat("1", 32)
			contextSID := "HX" + strings.Repeat("2", 32)
			questionSID := "HX" + strings.Repeat("3", 32)
			posts := []string{}
			approvedContext := false
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "ApprovalRequests") {
					status := "approved"
					if !strings.Contains(r.URL.Path, mainSID) && !approvedContext {
						status = "pending"
					}
					fmt.Fprintf(w, `{"whatsapp":{"status":%q}}`, status)
					return
				}
				if r.Method != "POST" {
					t.Error("unexpected provider read")
					return
				}
				_ = r.ParseForm()
				sid := r.Form.Get("ContentSid")
				posts = append(posts, sid)
				if r.Form.Has("Body") || r.Form.Has("MediaUrl") {
					t.Error("outside-window bubble was not a template", r.Form)
				}
				var vars map[string]string
				_ = json.Unmarshal([]byte(r.Form.Get("ContentVariables")), &vars)
				if len(posts) == 1 {
					if sid != mainSID || len(vars) != 4 || vars["4"] != "45" {
						t.Error(r.Form)
					}
				} else if goal != "" {
					if sid != contextSID || vars["1"] != goal {
						t.Error(r.Form)
					}
				} else if sid != questionSID || len(vars) != 0 {
					t.Error(r.Form)
				}
				fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "SM"+fmt.Sprintf("%032d", len(posts)))
			}))
			defer mock.Close()
			tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("4", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, BookedContentSID: mainSID, BookedQuestionSID: mainSID, FormContextContentSID: contextSID, FormContextQuestionSID: questionSID}
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Messenger = tw
			w.IntroVideoURL = "https://example.com/case.mp4"
			step(t, w, true)
			if len(posts) != 1 {
				t.Fatal("multiple bubbles sent together", posts)
			}
			id := fx.ID + ":context"
			j, err := db.Job(ctx, id)
			if err != nil || j.Queue != "execution" || j.Status != "pending" {
				t.Fatal(j, err)
			}
			var due time.Time
			if err := db.Pool.QueryRow(ctx, `SELECT due_at FROM bia_go.jobs WHERE id=$1`, id).Scan(&due); err != nil || due.Sub(now.Add(2*time.Second)).Abs() > time.Millisecond {
				t.Fatal(due, err)
			}
			// The case video must wait for the second opening bubble, including if
			// its template is still pending. No media is actually sent in this test.
			_, err = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE payload->>'purpose'='intro_video'`)
			if err != nil {
				t.Fatal(err)
			}
			step(t, mediaworker.Outbound(*w, remindermedia.Asset{}), true)
			assertJob(t, db, "fx/intro-video:"+s.ID, "media", "pending")
			_, _ = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, id)
			step(t, w, true)
			assertJob(t, db, id, "execution", "pending")
			if len(posts) != 1 {
				t.Fatal("sent pending second template")
			}
			approvedContext = true
			_, _ = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, id)
			step(t, w, true)
			assertJob(t, db, id, "execution", "done")
			step(t, w, false)
			if len(posts) != 2 {
				t.Fatal("missing or duplicate short bubble", posts)
			}
			current, err := db.Lead(ctx, s.ID)
			if err != nil || len(current.History) != 2 || current.Profile.Goal != goal || current.Human {
				t.Fatal(current, err)
			}
		})
	}
}

func TestOpeningSecondBubbleStopsAfterNewInputOrBookingChange(t *testing.T) {
	for _, change := range []string{"inbound", "cancel", "reschedule", "pause", "optout", "human", "consent"} {
		t.Run(change, func(t *testing.T) {
			db := setup(t)
			now := time.Now()
			s := testState("opening-guard", now)
			s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
			fx := domain.ContactContextSequence(s, "opening", policy(), now)[0]
			fx.Due = now
			switch change {
			case "inbound":
				s.LastInbound = now.Add(time.Second)
			case "cancel":
				s.Booking = nil
			case "reschedule":
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
			case "pause":
				s.Paused = true
			case "optout":
				s.OptedOut = true
			case "human":
				s.Human = true
			case "consent":
				s.Profile.Consent = false
			}
			publishEffects(t, db, "guard", s, fx)
			m := &countingMessenger{}
			w := runtimeWorker(db, now, m)
			step(t, w, true)
			assertJob(t, db, fx.ID, "execution", "skipped")
			if m.Count.Load() != 0 {
				t.Fatal("stale context delivered")
			}
		})
	}
}
