package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/quickreply"
)

func TestNoReplyCadenceReconcilesWithoutDuplicateAndStopsAfterResponse(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	anchor := now.Add(-20 * time.Minute)
	s := testState("no-reply-contact", anchor)
	publishEffects(t, db, "seed-no-reply", s)
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	w.Policy.DisableFollowups = true
	w.Policy.NoReplyEnabled = true
	w.Policy.NoReplySince = anchor.Add(-time.Minute)
	for range 2 {
		if err := db.ReconcileProactive(ctx, now, true, w.Policy); err != nil {
			t.Fatal(err)
		}
	}
	steps := domain.NoReplySequence(s, w.Policy, now)
	for _, fx := range steps {
		assertJob(t, db, fx.ID, "followup", "pending")
	}
	step(t, followupworker.New(*w), true)
	assertJob(t, db, steps[0].ID, "followup", "done")
	if err := db.ReconcileProactive(ctx, now, true, w.Policy); err != nil {
		t.Fatal(err)
	}
	step(t, followupworker.New(*w), false)
	if m.Count.Load() != 1 {
		t.Fatal("duplicate send", m.Count.Load())
	}
	s.LastInbound = now
	raw, _ := json.Marshal(s)
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, s.ID, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE lead_id=$1 AND status='pending'`, s.ID); err != nil {
		t.Fatal(err)
	}
	w.Now = func() time.Time { return anchor.Add(73 * time.Hour) }
	for range 3 {
		step(t, followupworker.New(*w), true)
	}
	if m.Count.Load() != 1 {
		t.Fatal("responded contact still received cadence", m.Count.Load())
	}
	for _, fx := range steps[1:] {
		assertJob(t, db, fx.ID, "followup", "skipped")
	}
}

func TestProactiveTemplatesDeliverButtonsOutsideSessionAndKeepChatAliveOnFailure(t *testing.T) {
	for _, purpose := range []string{"no_reply_15m", "meeting_checkin_5m", "meeting_checkin_7m_after"} {
		t.Run(purpose, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			anchor := now.Add(-time.Hour)
			s := testState("proactive-contact", anchor)
			p := policy()
			p.QuietStart = 0
			p.QuietEnd = 24
			p.DisableFollowups = true
			p.NoReplyEnabled = true
			p.NoReplySince = anchor.Add(-time.Minute)
			p.MeetingCheckins = true
			if purpose == "no_reply_15m" {
				a := now.Add(-20 * time.Minute)
				s.FirstContactAt = &a
			} else {
				start := now.Add(4 * time.Minute)
				if purpose == "meeting_checkin_7m_after" {
					start = now.Add(-8 * time.Minute)
				}
				s.Booking = &domain.Booking{ID: "booking", Start: start, End: start.Add(45 * time.Minute)}
			}
			var fx domain.Effect
			sequence := domain.NoReplySequence(s, p, now)
			if s.Booking != nil {
				sequence = domain.MeetingCheckinSequence(s, p, now)
			}
			for _, v := range sequence {
				if v.Purpose == purpose {
					fx = v
				}
			}
			if fx.ID == "" {
				t.Fatal("missing step")
			}
			publishEffects(t, db, "seed-proactive", s, fx)
			var approved atomic.Bool
			var posts atomic.Int64
			key := strings.Repeat("button-key", 5)
			sid := "HX" + strings.Repeat("d", 32)
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "ApprovalRequests") {
					status := "pending"
					if approved.Load() {
						status = "approved"
					}
					fmt.Fprintf(w, `{"whatsapp":{"status":%q}}`, status)
					return
				}
				posts.Add(1)
				_ = r.ParseForm()
				if r.Form.Get("ContentSid") != sid || r.Form.Has("Body") {
					t.Error("did not use approved buttons template")
				}
				var variables map[string]string
				if err := json.Unmarshal([]byte(r.Form.Get("ContentVariables")), &variables); err != nil {
					t.Error(err)
				}
				index := "4"
				action := "confirm"
				if domain.IsNoReply(purpose) {
					index = "2"
					action = "schedule"
				}
				if !strings.HasPrefix(variables[index], "bia1."+action+".") {
					t.Error("missing signed button", variables)
				}
				fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "SM"+strings.Repeat("b", 32))
			}))
			defer mock.Close()
			tw := &providers.TwilioMessaging{Pool: db.Pool, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, Account: "AC" + strings.Repeat("c", 32), AuthToken: "test", Sender: voiceSender, Buttons: quickreply.Signer{Key: key}, QuickReplies: providers.QuickReplyConfig{Enabled: true}, ProactiveContentSIDs: map[string]string{purpose: sid}}
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Policy = p
			w.Messenger = tw
			step(t, followupworker.New(*w), true)
			assertJob(t, db, fx.ID, "followup", "pending")
			st, err := db.Lead(ctx, s.ID)
			if err != nil || st.Human || posts.Load() != 0 {
				t.Fatal("pending template affected chat", err, st.Human, posts.Load())
			}
			approved.Store(true)
			if _, err = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, fx.ID); err != nil {
				t.Fatal(err)
			}
			step(t, followupworker.New(*w), true)
			assertJob(t, db, fx.ID, "followup", "done")
			step(t, followupworker.New(*w), false)
			if posts.Load() != 1 {
				t.Fatal("duplicate transport POST", posts.Load())
			}
			st, err = db.Lead(ctx, s.ID)
			if err != nil || st.Human || st.Booking != nil && st.Booking.Outcome != "" {
				t.Fatal("reminder fabricated outcome", err)
			}
		})
	}
}
