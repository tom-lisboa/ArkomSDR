package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/remindermedia"
	"arkom/bia/internal/voiceworker"
)

func reminderAsset(t *testing.T) remindermedia.Asset {
	t.Helper()
	data := []byte("OggS-integration-fixture-OpusHead-not-real-recording")
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hash+".ogg"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return remindermedia.Asset{Directory: dir, SHA256: hash, PublicURL: "https://media.example"}
}

type reminderAgenda struct{ lookups int }

func (a *reminderAgenda) Lookup(context.Context, string) (domain.MeetingContext, error) {
	a.lookups++
	return domain.MeetingContext{Status: "available"}, nil
}
func (*reminderAgenda) Note(context.Context, string, string, string) (string, error) {
	return "recorded", nil
}

func TestMeetingSequenceAcrossWorkersAndRestart(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := now
	s := testState(domain.LeadKey("+5565999999900"), now)
	s.LastInbound = now
	s.Booking = &domain.Booking{ID: "sequence-booking", Start: now.Add(29 * time.Minute), End: now.Add(74 * time.Minute), MeetingURL: "https://meet.example/sequence"}
	publishEffects(t, db, "seed-sequence", s)
	for range 2 {
		if err := db.ReconcileMeetingReminders(ctx, now, true); err != nil {
			t.Fatal(err)
		}
	}
	var total int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE payload->>'purpose' IN ('meeting_audio','meeting_call')`).Scan(&total); err != nil || total != 2 {
		t.Fatal(total, err)
	}
	fx := domain.MeetingSequence(s, now)
	a := reminderAsset(t)
	posts := 0
	audioPosts := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			posts++
			if r.Form.Get("MediaUrl") != "" {
				audioPosts++
				if r.Form.Get("MediaUrl") != a.URL() || r.Form.Has("Body") {
					t.Error("audio payload", r.Form)
				}
			} else if !strings.Contains(r.Form.Get("Body"), "Autorize") {
				t.Error("missing invitation")
			}
			fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "MM"+fmt.Sprintf("%032d", posts))
		} else {
			fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, strings.TrimSuffix(filepath.Base(r.URL.Path), ".json"))
		}
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("1", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL}
	w := runtimeWorker(db, now, &countingMessenger{})
	w.Messenger = tw
	w.AudioSender = tw
	w.Policy.MeetingReminders = true
	w.Policy.DisableFollowups = true
	w.VoiceSender = voiceSender
	w.Now = func() time.Time { return clock }
	newFollowup := func() *followupworker.Worker {
		fw := followupworker.New(*w)
		fw.Audio = a
		fw.VoiceEnabled = true
		return fw
	}
	fw := newFollowup()
	mw := mediaworker.Outbound(*w, a)
	step(t, w, false)
	step(t, mw, false)
	step(t, fw, true)
	assertJob(t, db, fx[0].ID, "followup", "done")
	assertJob(t, db, fx[0].ID+":media", "media", "pending")
	step(t, mw, true)
	step(t, mw, false)
	if audioPosts != 1 || posts != 1 {
		t.Fatal(posts, audioPosts)
	}
	// Fresh processes and reconciliation must not repeat the T-30 send.
	if err := db.ReconcileMeetingReminders(ctx, now, true); err != nil {
		t.Fatal(err)
	}
	fw = newFollowup()
	step(t, fw, false)
	clock = s.Booking.Start.Add(-15 * time.Minute)
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, fx[1].ID); err != nil {
		t.Fatal(err)
	}
	step(t, fw, true)
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.voice_requests`).Scan(&n)
	if n != 0 || posts != 2 {
		t.Fatal("permission created before message receipt", n, posts)
	}
	// Next step is durable and starts only after the prior transaction committed.
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, fx[1].ID+":permission"); err != nil {
		t.Fatal(err)
	}
	step(t, newFollowup(), true)
	var id string
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM bia_go.voice_requests`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	repo := voiceworker.Repository{DB: db}
	req, err := repo.Get(ctx, id)
	if err != nil || !req.ScheduledReminder || !req.Expires.Equal(s.Booking.Start.Truncate(time.Microsecond)) {
		t.Fatal(req, err)
	}
	provider := &fakeVoice{}
	agenda := &reminderAgenda{}
	vw := &voiceworker.Worker{Repo: repo, Provider: provider, Agenda: agenda, Policy: w.Policy, Live: true, Now: func() time.Time { return clock }}
	step(t, vw, true)
	step(t, vw, false)
	if provider.permissions != 1 || provider.dials != 0 || agenda.lookups != 1 {
		t.Fatal("call before consent", provider)
	}
	if err := repo.Permission(ctx, "accept-sequence", s.Profile.Phone, voiceSender, true, false, req.Created.Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}
	step(t, vw, true)
	step(t, vw, false)
	if provider.dials != 1 {
		t.Fatal(provider.dials)
	}
	if err := repo.Permission(ctx, "accept-sequence-again", s.Profile.Phone, voiceSender, true, false, req.Created); err != nil {
		t.Fatal(err)
	}
	step(t, vw, false)
	if provider.dials != 1 || posts != 2 {
		t.Fatal("replay caused external operation")
	}
}

func TestMeetingCadenceStopsBeforeExternalSend(t *testing.T) {
	for _, condition := range []string{"disabled", "missing_audio", "closed_session", "cancelled", "rescheduled", "optout", "paused", "human", "no_consent", "late", "shadow", "buffered_input"} {
		t.Run(condition, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("guard-lead", now)
			s.LastInbound = now
			s.Booking = &domain.Booking{ID: "b", Start: now.Add(29 * time.Minute)}
			fx := domain.MeetingSequence(s, now)[0]
			w := runtimeWorker(db, now, &countingMessenger{})
			w.Policy.MeetingReminders = true
			a := reminderAsset(t)
			switch condition {
			case "disabled":
				w.Policy.MeetingReminders = false
			case "missing_audio":
				a.SHA256 = ""
			case "closed_session":
				s.LastInbound = now.Add(-24 * time.Hour)
			case "cancelled":
				s.Booking = nil
			case "rescheduled":
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
			case "optout":
				s.OptedOut = true
			case "paused":
				s.Paused = true
			case "human":
				s.Human = true
			case "no_consent":
				s.Profile.Consent = false
			case "late":
				fx.Expires = now.Add(-time.Second)
			}
			publishEffects(t, db, "seed", s, fx)
			if condition == "shadow" {
				if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET mode='shadow' WHERE id=$1`, fx.ID); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "buffered_input" {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.conversations(id,lead_id,mode,flush_at) VALUES('buffer',$1,'live',now()+interval '12 seconds')`, s.ID); err != nil {
					t.Fatal(err)
				}
			}
			fw := followupworker.New(*w)
			fw.Audio = a
			fw.VoiceEnabled = true
			step(t, fw, true)
			want := "skipped"
			if condition == "shadow" {
				want = "simulated"
			}
			if condition == "buffered_input" {
				want = "pending"
			}
			assertJob(t, db, fx.ID, "followup", want)
			var n int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE queue='media'`).Scan(&n); err != nil || n != 0 {
				t.Fatal("queued forbidden audio", n, err)
			}
		})
	}
}

type failedReminderMessage struct{}

func (failedReminderMessage) Send(context.Context, string, domain.State, string, string, time.Time) (providers.Receipt, error) {
	return providers.Receipt{}, &providers.Failure{Code: "test_uncertain", Ambiguous: true}
}
func TestFailedReminderTextCannotCreatePermission(t *testing.T) {
	db := setup(t)
	now := time.Now()
	s := testState("failed-lead", now)
	s.LastInbound = now
	s.Booking = &domain.Booking{ID: "b", Start: now.Add(14 * time.Minute)}
	fx := domain.MeetingSequence(s, now)[0]
	publishEffects(t, db, "seed", s, fx)
	w := runtimeWorker(db, now, &countingMessenger{})
	w.Policy.MeetingReminders = true
	w.Messenger = failedReminderMessage{}
	fw := followupworker.New(*w)
	fw.VoiceEnabled = true
	step(t, fw, true)
	assertJob(t, db, fx.ID, "followup", "uncertain")
	var n int
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE payload->>'kind'='request_call'`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestScheduledVoiceCancellationExpiryAndDisable(t *testing.T) {
	for _, condition := range []string{"cancelled", "rescheduled", "disabled", "late_permission", "late_request"} {
		t.Run(condition, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now()
			s := testState(domain.LeadKey("+5565999999900"), now)
			s.LastInbound = now
			s.Booking = &domain.Booking{ID: "b", Start: now.Add(14 * time.Minute)}
			publishEffects(t, db, "seed", s)
			fx := domain.MeetingSequence(s, now)[0]
			fx.Kind = "request_call"
			repo := voiceworker.Repository{DB: db}
			if ok, err := repo.CreateScheduled(ctx, "scheduled", s.ID, "live", voiceSender, fx); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if ok, err := repo.CreateScheduled(ctx, "scheduled", s.ID, "live", voiceSender, fx); err != nil || ok {
				t.Fatal("duplicate", ok, err)
			}
			p := policy()
			p.MeetingReminders = true
			p.QuietStart = 0
			p.QuietEnd = 24
			v := &fakeVoice{}
			vw := &voiceworker.Worker{Repo: repo, Provider: v, Agenda: &reminderAgenda{}, Policy: p, Live: true}
			if condition == "late_request" {
				vw.Now = func() time.Time { return s.Booking.Start.Add(-9 * time.Minute) }
				step(t, vw, true)
				if v.permissions != 0 {
					t.Fatal("late button")
				}
				return
			}
			step(t, vw, true)
			r, _ := repo.Get(ctx, "scheduled")
			if condition == "cancelled" || condition == "rescheduled" {
				if condition == "cancelled" {
					s.Booking = nil
				} else {
					s.Booking.Start = s.Booking.Start.Add(time.Hour)
				}
				publishEffects(t, db, "changed", s)
				if err := repo.Permission(ctx, "late-approval", s.Profile.Phone, voiceSender, true, false, r.Created); err != nil {
					t.Fatal(err)
				}
				step(t, vw, false)
			} else if condition == "late_permission" {
				if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET expires_at=now()-interval '1 second' WHERE id='scheduled'`); err != nil {
					t.Fatal(err)
				}
				if err := repo.Permission(ctx, "late", s.Profile.Phone, voiceSender, true, false, r.Created); err != nil {
					t.Fatal(err)
				}
				step(t, vw, false)
			} else {
				if err := repo.Permission(ctx, "approve", s.Profile.Phone, voiceSender, true, false, r.Created); err != nil {
					t.Fatal(err)
				}
				vw.Policy.MeetingReminders = false
				step(t, vw, true)
			}
			if v.dials != 0 {
				t.Fatal("forbidden call")
			}
		})
	}
}

func TestTwilioAudioInterruptedSendDoesNotRepeat(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	s := testState("audio-ledger", now)
	s.LastInbound = now
	posts := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++; w.WriteHeader(503) }))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("1", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL}
	for range 2 {
		if _, err := tw.SendAudio(ctx, "audio-ambiguous", s, "https://media.example/audio.ogg", now); err == nil || !providers.Classify(err).Ambiguous {
			t.Fatal(err)
		}
	}
	if posts != 1 {
		t.Fatal("duplicate audio POST", posts)
	}
	s.LastInbound = now.Add(-24 * time.Hour)
	if _, err := tw.SendAudio(ctx, "audio-closed", s, "https://media.example/audio.ogg", now); err == nil {
		t.Fatal("session guard")
	}
	if posts != 1 {
		t.Fatal("sent outside session")
	}
}

func TestMeetingRoutingMigrationRetainsTerminalJobs(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	s := testState("migration", now)
	s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour)}
	fx := domain.MeetingSequence(s, now)
	publishEffects(t, db, "seed", s, fx...)
	if _, err := db.Pool.Exec(ctx, `DELETE FROM bia_go.schema_versions WHERE version=9; ALTER TABLE bia_go.voice_requests DROP COLUMN scheduled_reminder; UPDATE bia_go.jobs SET status='done' WHERE payload->>'purpose'='meeting_audio'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertJob(t, db, fx[0].ID, "followup", "done")
	assertJob(t, db, fx[1].ID, "followup", "pending")
	// Verify JSON timestamp roundtrips retain IDs across polling/restarts.
	raw, _ := json.Marshal(s)
	var restored domain.State
	json.Unmarshal(raw, &restored)
	if domain.MeetingSequence(restored, now)[0].ID != fx[0].ID {
		t.Fatal("unstable persisted identity")
	}
}
