package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/agendaclient"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/schedulingworker"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
)

const voiceSender = "+5565999999901"
const voiceCall = "CA11111111111111111111111111111111"

type fakeVoice struct {
	permissions, dials, hangups int
	dialErr                     error
	at                          time.Time
}

func (f *fakeVoice) Permission(context.Context, string, string) (string, error) {
	f.permissions++
	return "SM11111111111111111111111111111111", nil
}
func (f *fakeVoice) Dial(context.Context, voiceworker.Request, string) (string, error) {
	f.dials++
	return voiceCall, f.dialErr
}
func (f *fakeVoice) Hangup(context.Context, string) error { f.hangups++; return nil }
func (f *fakeVoice) MessageTime(context.Context, string, string, string) (time.Time, error) {
	return f.at, nil
}
func voiceFixture(t *testing.T, mode string) (*store.DB, voiceworker.Repository, *voiceworker.Worker, *fakeVoice, domain.State) {
	t.Helper()
	db := setup(t)
	now := time.Now().UTC()
	s := testState(domain.LeadKey("+5565999999900"), now)
	s.LastInbound = now
	s.Profile.Company = "Empresa Teste"
	revenue := int64(0)
	s.Profile.Revenue = &revenue
	publishEffects(t, db, "seed-voice", s)
	cal := &providers.LocalCalendar{Pool: db.Pool}
	start := now.Add(48 * time.Hour).Truncate(time.Hour)
	slot := providers.LocalSlot{Slot: domain.Slot{ID: "reminder-slot", Provider: "local", CloserID: 10, Start: start, End: start.Add(45 * time.Minute)}, MeetingURL: "https://meet.example/reminder"}
	if e := cal.Add(context.Background(), slot); e != nil {
		t.Fatal(e)
	}
	booking, e := cal.Book(context.Background(), "reminder-reservation", s, slot.Slot, nil)
	if e != nil {
		t.Fatal(e)
	}
	s.Booking = &booking
	publishEffects(t, db, "seed-voice-booking", s)
	p := policy()
	p.QuietStart = 0
	p.QuietEnd = 24
	repo := voiceworker.Repository{DB: db}
	f := &fakeVoice{at: now}
	agenda := agendaclient.Client{DB: db}
	scheduler := schedulingworker.New(*baseWorker(db), cal)
	schedulerContext, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); scheduler.Run(schedulerContext) }()
	t.Cleanup(func() { stop(); <-done })
	w := &voiceworker.Worker{Agenda: agenda, Repo: repo, Provider: f, Policy: p, Live: true}
	if _, e := repo.Create(context.Background(), "voice-one", s.ID, mode, voiceSender, "confirm"); e != nil {
		t.Fatal(e)
	}
	return db, repo, w, f, s
}
func voiceState(t *testing.T, repo voiceworker.Repository, want string) voiceworker.Request {
	t.Helper()
	r, e := repo.Get(context.Background(), "voice-one")
	if e != nil || r.State != want {
		t.Fatalf("state=%s want=%s err=%v", r.State, want, e)
	}
	return r
}
func approveVoice(t *testing.T, repo voiceworker.Repository, s domain.State) {
	t.Helper()
	r := voiceState(t, repo, "awaiting_permission")
	if e := repo.Permission(context.Background(), "permission-event", s.Profile.Phone, voiceSender, true, false, r.Created.Truncate(time.Second)); e != nil {
		t.Fatal(e)
	}
	voiceState(t, repo, "ready")
}
func TestVoiceConsentIdempotenceAndCallbackOrder(t *testing.T) {
	_, repo, w, f, s := voiceFixture(t, "live")
	ctx := context.Background()
	if created, e := repo.Create(ctx, "voice-one", s.ID, "live", voiceSender, "confirm"); e != nil || created {
		t.Fatal(created, e)
	}
	if _, e := repo.Create(ctx, "voice-two", s.ID, "live", voiceSender, "confirm"); !errors.Is(e, voiceworker.ErrBlocked) {
		t.Fatal(e)
	}
	step(t, w, true)
	voiceState(t, repo, "awaiting_permission")
	step(t, w, false)
	if f.permissions != 1 || f.dials != 0 {
		t.Fatal("dialed without approval")
	}
	r := voiceState(t, repo, "awaiting_permission")
	for _, v := range []struct {
		id, sender string
		at         time.Time
	}{{"wrong-sender", "+5565999999902", time.Now()}, {"stale", voiceSender, r.Created.Add(-time.Hour)}} {
		if e := repo.Permission(ctx, v.id, s.Profile.Phone, v.sender, true, false, v.at); e != nil {
			t.Fatal(e)
		}
	}
	voiceState(t, repo, "awaiting_permission")
	approveVoice(t, repo, s)
	step(t, w, true)
	voiceState(t, repo, "queued")
	step(t, w, false)
	if e := repo.Permission(ctx, "permission-event", s.Profile.Phone, voiceSender, true, false, r.Created); e != nil {
		t.Fatal(e)
	}
	step(t, w, false)
	if f.dials != 1 {
		t.Fatal("duplicate dial", f.dials)
	}
	if e := repo.Status(ctx, r.ID, voiceCall, "in-progress", 2); e != nil {
		t.Fatal(e)
	}
	if e := repo.Status(ctx, r.ID, voiceCall, "ringing", 1); e != nil {
		t.Fatal(e)
	}
	r = voiceState(t, repo, "in-progress")
	if e := repo.BeginStream(ctx, r, voiceCall); e != nil {
		t.Fatal(e)
	}
	if e := repo.BeginStream(ctx, r, voiceCall); !errors.Is(e, voiceworker.ErrBlocked) {
		t.Fatal("stream replay", e)
	}
	if e := repo.Permission(ctx, "revoked", s.Profile.Phone, voiceSender, false, false, time.Now()); e != nil {
		t.Fatal(e)
	}
	if _, e := repo.Active(ctx, r); !errors.Is(e, voiceworker.ErrBlocked) {
		t.Fatal("revocation ignored", e)
	}
	if e := repo.Status(ctx, r.ID, voiceCall, "completed", 3); e != nil {
		t.Fatal(e)
	}
	if e := repo.Status(ctx, r.ID, voiceCall, "in-progress", 4); e != nil {
		t.Fatal(e)
	}
	voiceState(t, repo, "completed")
}
func TestVoiceShadowAndAmbiguousMutation(t *testing.T) {
	t.Run("shadow", func(t *testing.T) {
		_, repo, w, f, _ := voiceFixture(t, "shadow")
		step(t, w, true)
		voiceState(t, repo, "simulated")
		if f.permissions+f.dials != 0 {
			t.Fatal("shadow reached provider")
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		db, repo, w, f, s := voiceFixture(t, "live")
		step(t, w, true)
		approveVoice(t, repo, s)
		f.dialErr = &providers.Failure{Code: "timeout", Ambiguous: true}
		step(t, w, true)
		voiceState(t, repo, "uncertain")
		step(t, w, false)
		if f.dials != 1 {
			t.Fatal(f.dials)
		}
		active, e := db.VoiceActive(context.Background(), s.ID)
		if e != nil || !active {
			t.Fatal("uncertain call must require reconciliation", e)
		}
	})
	t.Run("crash", func(t *testing.T) {
		db, repo, w, f, _ := voiceFixture(t, "live")
		ctx := context.Background()
		if _, e := repo.Claim(ctx, "lost"); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET lease_until=now()-interval '1 second'`); e != nil {
			t.Fatal(e)
		}
		step(t, w, false)
		voiceState(t, repo, "uncertain")
		if f.permissions != 0 {
			t.Fatal("retried crashed mutation")
		}
	})
	t.Run("expired", func(t *testing.T) {
		db, repo, w, f, _ := voiceFixture(t, "live")
		if _, e := db.Pool.Exec(context.Background(), `UPDATE bia_go.voice_requests SET expires_at=now()-interval '1 second'`); e != nil {
			t.Fatal(e)
		}
		step(t, w, false)
		voiceState(t, repo, "expired")
		if f.permissions != 0 {
			t.Fatal("expired invite sent")
		}
	})
	t.Run("outside_session", func(t *testing.T) {
		db, repo, w, f, s := voiceFixture(t, "live")
		s.LastInbound = time.Now().Add(-25 * time.Hour)
		publishEffects(t, db, "old-inbound", s)
		step(t, w, true)
		voiceState(t, repo, "skipped")
		if f.permissions != 0 {
			t.Fatal("unapproved template sent")
		}
	})
}
func TestVoiceSignedPermissionWebhook(t *testing.T) {
	_, repo, w, f, s := voiceFixture(t, "live")
	step(t, w, true)
	r := voiceState(t, repo, "awaiting_permission")
	f.at = r.Created.Truncate(time.Second)
	srv := &voiceworker.Server{Repo: repo, Provider: f, Account: "AC11111111111111111111111111111111", Sender: voiceSender, AuthToken: "test-only", PublicURL: "https://voice.example", Enabled: true}
	form := url.Values{"AccountSid": {srv.Account}, "To": {"whatsapp:" + voiceSender}, "From": {"whatsapp:" + s.Profile.Phone}, "Body": {"VOICE_CALL_REQUEST"}, "ButtonPayload": {"ACCEPTED"}, "MessageSid": {"SM22222222222222222222222222222222"}}
	invoke := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/voice/permission", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", signature)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := invoke("wrong"); code != 403 {
		t.Fatal(code)
	}
	voiceState(t, repo, "awaiting_permission")
	sig := voiceworker.Signature(srv.AuthToken, srv.PublicURL+"/voice/permission", form)
	if code := invoke(sig); code != 200 {
		t.Fatal(code)
	}
	voiceState(t, repo, "ready")
	form.Set("ButtonPayload", "REJECTED")
	if code := invoke(sig); code != 403 {
		t.Fatal("tampered callback accepted", code)
	}
}
func TestVoiceReminderCannotBookAndRecordsNotes(t *testing.T) {
	db, repo, vw, _, s := voiceFixture(t, "live")
	ctx := context.Background()
	step(t, vw, true)
	approveVoice(t, repo, s)
	step(t, vw, true)
	r := voiceState(t, repo, "queued")
	tools := voiceworker.Tools{Repo: repo, Agenda: vw.Agenda, Policy: vw.Policy}
	for _, name := range []string{"book_slot", "list_slots", "cancel", "reschedule"} {
		out := tools.Execute(ctx, r, name, "forged", "{}").(map[string]string)
		if out["error"] != "tool_not_allowed_in_reminder_call" {
			t.Fatal(out)
		}
	}
	if _, e := db.VoiceBooking(ctx, r.ID, "old", "slot", "confirmo", vw.Policy); e == nil {
		t.Fatal("legacy mutation accepted")
	}
	meeting, e := vw.Agenda.Lookup(ctx, r.ID)
	if e != nil || meeting.Status != "available" || meeting.Customer.Company != "Empresa Teste" || meeting.Customer.Revenue == nil || *meeting.Customer.Revenue != 0 || !meeting.Booking.Start.Equal(s.Booking.Start) {
		t.Fatal(meeting, e)
	}
	args := `{"kind":"reschedule_request","text":"Prefere sexta de manhã"}`
	for range 2 {
		out := tools.Execute(ctx, r, "record_reminder_note", "note-1", args).(map[string]string)
		if out["status"] != "recorded" {
			t.Fatal(out)
		}
	}
	var count int
	if e := db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.crm_notes WHERE purpose='voice_reminder'`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	current, e := db.Lead(ctx, s.ID)
	if e != nil || current.Booking.ID != s.Booking.ID || !current.Booking.Start.Equal(s.Booking.Start) {
		t.Fatal("note changed booking", e)
	}
	if _, e := vw.Agenda.Note(ctx, r.ID, "note-1", "different note"); e == nil {
		t.Fatal("conflicting retry accepted")
	}
}

func TestVoiceFormAutoInviteAndPermissionLimit(t *testing.T) {
	t.Run("form", func(t *testing.T) {
		db := setup(t)
		w := baseWorker(db)
		w.VoiceAutoRequest = true
		w.VoiceSender = voiceSender
		ev := form("voice-zero-revenue", "+5565999999900")
		submit(t, db, ev, "shadow")
		drain(t, w)
		var count int
		if e := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.voice_requests WHERE lead_id=$1 AND state='new'`, ev.LeadID).Scan(&count); e != nil || count != 0 {
			t.Fatal("form unexpectedly started reminder", count, e)
		}
	})
	t.Run("limit", func(t *testing.T) {
		db, repo, w, f, s := voiceFixture(t, "live")
		step(t, w, true)
		if _, e := db.Pool.Exec(context.Background(), `UPDATE bia_go.voice_requests SET state='rejected' WHERE id='voice-one'`); e != nil {
			t.Fatal(e)
		}
		if _, e := repo.Create(context.Background(), "voice-two", s.ID, "live", voiceSender, "confirm"); e != nil {
			t.Fatal(e)
		}
		step(t, w, true)
		r, e := repo.Get(context.Background(), "voice-two")
		if e != nil || r.State != "skipped" || r.Error != "permission_request_limit" || f.permissions != 1 {
			t.Fatal("daily limit bypassed", r.State, e)
		}
	})
}
