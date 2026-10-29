package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/management"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
)

type panelFixture struct {
	s       *management.Server
	handler http.Handler
	user    management.User
	token   string
}

func newPanel(t *testing.T, db *store.DB, now time.Time, role string) panelFixture {
	t.Helper()
	u, err := management.CreateUser(context.Background(), db, "test-"+role, "Operador de teste", "a-test-password-12345", role)
	if err != nil {
		t.Fatal(err)
	}
	s := &management.Server{DB: db, Mode: "live", Policy: policy(), Now: func() time.Time { return now }, Origins: []string{"https://staff.example"}}
	f := panelFixture{s: s, handler: s.Handler(), user: u}
	out := panelCall(t, f, "POST", "/auth/login", map[string]any{"username": u.Username, "password": "a-test-password-12345"}, "")
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var login map[string]any
	_ = json.Unmarshal(out.Body.Bytes(), &login)
	f.token = login["access_token"].(string)
	return f
}
func panelCall(t *testing.T, f panelFixture, method, path string, body any, key string) *httptest.ResponseRecorder {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, "/v1/management"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, req)
	return out
}
func decodeMap(t *testing.T, out *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestManagementAuthenticationRolesAndRevocation(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	admin := newPanel(t, db, now, "admin")
	viewer := newPanel(t, db, now, "viewer")
	unknown := viewer
	unknown.token = ""
	if out := panelCall(t, unknown, "GET", "/leads", nil, ""); out.Code != 401 {
		t.Fatal(out.Code)
	}
	if out := panelCall(t, viewer, "POST", "/users", map[string]any{"username": "another", "name": "Outra pessoa", "password": "long-password-12345", "role": "operator"}, ""); out.Code != 403 {
		t.Fatal(out.Code)
	}
	if out := panelCall(t, admin, "PATCH", "/users/"+admin.user.ID, map[string]any{"enabled": false}, ""); out.Code != 409 {
		t.Fatal("disabled last admin", out.Code, out.Body.String())
	}
	out := panelCall(t, admin, "GET", "/users", nil, "")
	if out.Code != 200 || strings.Contains(out.Body.String(), "password") {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := panelCall(t, admin, "PATCH", "/users/"+viewer.user.ID, map[string]any{"enabled": false}, ""); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := panelCall(t, viewer, "GET", "/auth/me", nil, ""); out.Code != 401 {
		t.Fatal("disabled session remained valid")
	}
	req := httptest.NewRequest("OPTIONS", "/v1/management/leads", nil)
	req.Header.Set("Origin", "https://attacker.example")
	rec := httptest.NewRecorder()
	admin.handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("permissive CORS")
	}
	if out := panelCall(t, admin, "POST", "/auth/logout", map[string]any{}, ""); out.Code != 200 {
		t.Fatal(out.Code)
	}
	if out := panelCall(t, admin, "GET", "/auth/me", nil, ""); out.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
}
func TestManagementLeadListScorePaginationAndEmptyMetrics(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	for i := 0; i < 3; i++ {
		st := testState(fmt.Sprint("panel-lead-", i), now.Add(-time.Hour))
		st.Profile.Company = "Empresa"
		if i == 0 {
			st.Profile.Goal = "vender"
			st.Booking = &domain.Booking{ID: "b-hot", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute), ConfirmedAt: &now}
			st.LastInbound = now
			st.Stage = "booked"
		}
		publishEffects(t, db, fmt.Sprint("seed-panel-", i), st)
		if i == 0 {
			_, err := db.Pool.Exec(context.Background(), `INSERT INTO bia_go.conversations(id,lead_id,mode) VALUES('conv-hot',$1,'live');`, st.ID)
			if err != nil {
				t.Fatal(err)
			}
			for j := 0; j < 3; j++ {
				_, err = db.Pool.Exec(context.Background(), `INSERT INTO bia_go.conversation_messages(id,conversation_id,role,body,occurred_at,run_id) VALUES($1,'conv-hot','user','Quero agendar',$2,'done')`, fmt.Sprint("m-", j), now)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	first := decodeMap(t, panelCall(t, f, "GET", "/leads?limit=2", nil, ""))
	if len(first["items"].([]any)) != 2 || first["next_cursor"] == "" {
		t.Fatal(first)
	}
	second := decodeMap(t, panelCall(t, f, "GET", "/leads?limit=2&cursor="+url.QueryEscape(first["next_cursor"].(string)), nil, ""))
	if len(second["items"].([]any)) != 1 {
		t.Fatal(second)
	}
	hot := decodeMap(t, panelCall(t, f, "GET", "/leads?temperature=hot", nil, ""))
	items := hot["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "panel-lead-0" {
		t.Fatal(hot)
	}
	score := items[0].(map[string]any)["score"].(map[string]any)
	if score["value"] != float64(95) || score["is_probability"] != false {
		t.Fatal(score)
	}
	detail := decodeMap(t, panelCall(t, f, "GET", "/leads/panel-lead-0", nil, ""))
	if detail["pipeline_stage"] != "presence_confirmed" {
		t.Fatal(detail)
	}
	opted := testState("opted-panel", now)
	opted.OptedOut = true
	opted.Human = true
	opted.Paused = true
	publishEffects(t, db, "seed-opted-panel", opted)
	metrics := decodeMap(t, panelCall(t, f, "GET", "/metrics", nil, ""))
	snapshot := metrics["snapshot"].(map[string]any)
	sum := float64(0)
	for _, n := range snapshot["open_by_stage"].(map[string]any) {
		sum += n.(float64)
	}
	if sum != snapshot["open_leads"].(float64) || snapshot["human_open_leads"] != float64(0) || snapshot["paused_open_leads"] != float64(0) {
		t.Fatal("inconsistent open lead denominators", snapshot)
	}
	meetings := metrics["meeting_cohort"].(map[string]any)
	if meetings["show_up_rate"] != nil {
		t.Fatal("invented rate without attendance", meetings)
	}
	if out := panelCall(t, f, "GET", "/leads?temperature=unknown", nil, ""); out.Code != 422 {
		t.Fatal(out.Code)
	}
}
func TestManagementTakeoverFencesDraftAndBusyExternalEffects(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	st := testState("draft-interrupt", now.Add(-time.Hour))
	publishEffects(t, db, "seed-interrupt", st)
	ev := domain.Event{ID: "pending-model", LeadID: st.ID, Kind: "message.received", Text: "Quero saber mais", At: now}
	raw, _ := json.Marshal(ev)
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event','live',$3,$1,now())`, ev.ID, st.ID, raw); err != nil {
		t.Fatal(err)
	}
	job, snapshot, err := db.ClaimQueue(context.Background(), "execution")
	if err != nil {
		t.Fatal(err)
	}
	a := management.Action{Kind: "pause", Reason: "Operador assumiu o atendimento"}
	result, duplicate, err := f.s.ApplyAction(context.Background(), f.user, st.ID, "pause-key", a)
	if err != nil || duplicate || result["human"] != true {
		t.Fatal(result, err)
	}
	if err = db.Finish(context.Background(), job, snapshot, nil, "done", nil); !errors.Is(err, store.ErrLease) {
		t.Fatal("stale model regained ownership", err)
	}
	again, duplicate, err := f.s.ApplyAction(context.Background(), f.user, st.ID, "pause-key", a)
	if err != nil || !duplicate || again["action_id"] != result["action_id"] {
		t.Fatal(again, err)
	}
	if _, _, err = f.s.ApplyAction(context.Background(), f.user, st.ID, "pause-key", management.Action{Kind: "resume"}); err == nil {
		t.Fatal("idempotency payload changed")
	}
	current, _ := db.Lead(context.Background(), st.ID)
	if !current.Human || !current.Paused {
		t.Fatal(current)
	}
	// A running provider effect is not falsely reported as cancelled.
	if _, _, err = f.s.ApplyAction(context.Background(), f.user, st.ID, "resume-key", management.Action{Kind: "resume"}); err != nil {
		t.Fatal(err)
	}
	current, _ = db.Lead(context.Background(), st.ID)
	fx := scheduledMessage("external-busy", current, "reply", now)
	publishEffects(t, db, "seed-external", current, fx)
	job, snapshot, err = db.ClaimQueue(context.Background(), "execution")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.ApplyAction(context.Background(), f.user, st.ID, "busy-key", management.Action{Kind: "takeover"}); err == nil {
		t.Fatal("in-flight external send was ignored")
	}
	if err = db.Finish(context.Background(), job, snapshot, nil, "done", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.ApplyAction(context.Background(), f.user, st.ID, "busy-key", management.Action{Kind: "takeover"}); err != nil {
		t.Fatal(err)
	}
}
func TestManagementHumanReplyUsesNormalTransportAndAttributesOperator(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	st := testState("human-chat", now.Add(-time.Hour))
	st.LastInbound = now.Add(-time.Minute)
	publishEffects(t, db, "seed-human", st)
	if _, _, err := f.s.ApplyAction(ctx, f.user, st.ID, "takeover", management.Action{Kind: "takeover"}); err != nil {
		t.Fatal(err)
	}
	result, _, err := f.s.ApplyAction(ctx, f.user, st.ID, "human-message", management.Action{Kind: "message", Text: "Olá, sou a pessoa responsável pelo seu atendimento."})
	if err != nil {
		t.Fatal(err)
	}
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	drain(t, w)
	if m.Count.Load() != 1 {
		t.Fatal("manual reply was blocked or duplicated", m.Count.Load())
	}
	ingest(t, db, fastWindowPolicy(), now)
	out := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/messages", nil, ""))
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["sender_type"] != "operator" || items[0].(map[string]any)["operator_id"] != f.user.ID {
		t.Fatal(result, out)
	}
	if _, _, err = f.s.ApplyAction(ctx, f.user, st.ID, "release", management.Action{Kind: "release"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.ApplyAction(ctx, f.user, st.ID, "invalid-human-message", management.Action{Kind: "message", Text: "não enviar"}); err == nil {
		t.Fatal("manual transport bypassed human ownership")
	}
}
func seedPastBooking(t *testing.T, db *store.DB, id string, now time.Time) domain.State {
	t.Helper()
	ctx := context.Background()
	st := testState(id, now.Add(-7*24*time.Hour))
	first := st.CreatedAt.Add(time.Minute)
	st.FirstContactAt = &first
	st.Booking = &domain.Booking{ID: "booking-" + id, Provider: "local", CloserID: 1, Start: now.Add(-3 * time.Hour), End: now.Add(-135 * time.Minute)}
	st.Stage = "booked"
	publishEffects(t, db, "seed-"+id, st)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.slots(id,starts_at,ends_at,closer_id,meeting_url,reserved_by) VALUES($1,$2,$3,$4,'',$5)`, "slot-"+id, st.Booking.Start, st.Booking.End, int64(len(id)+1), id); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(st.Booking)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.bookings(id,operation_id,lead_id,slot_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, st.Booking.ID, "book-op-"+id, id, "slot-"+id, raw, st.CreatedAt.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return st
}
func TestManagementAttendanceMetricsAndAIContext(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	f := newPanel(t, db, now, "operator")
	attended := seedPastBooking(t, db, "attended-a", now)
	missed := seedPastBooking(t, db, "missed-long-b", now)
	unknown := seedPastBooking(t, db, "unknown-longer-c", now)
	for _, pair := range []struct {
		st      domain.State
		outcome string
	}{{attended, "attended"}, {missed, "no_show"}} {
		if _, _, err := f.s.ApplyAction(ctx, f.user, pair.st.ID, pair.outcome, management.Action{Kind: "attendance", BookingID: pair.st.Booking.ID, Outcome: pair.outcome}); err != nil {
			t.Fatal(err)
		}
	}
	current, err := db.Lead(ctx, missed.ID)
	if err != nil || current.Booking.Outcome != "no_show" || len(current.MeetingResults) != 1 || domain.CustomerContext(current, current.Booking, policy()).MeetingResults[0].Outcome != "no_show" {
		t.Fatal("AI context missing attendance", current, err)
	}
	if _, _, err = f.s.ApplyAction(ctx, f.user, attended.ID, "mark-won", management.Action{Kind: "status", Status: "won"}); err != nil {
		t.Fatal(err)
	}
	out := decodeMap(t, panelCall(t, f, "GET", "/metrics", nil, ""))
	meetings := out["meeting_cohort"].(map[string]any)
	if meetings["attended"] != float64(1) || meetings["no_show"] != float64(1) || meetings["completed_without_outcome"] != float64(1) || meetings["show_up_rate"] != 0.5 {
		t.Fatal(out)
	}
	cohort := out["lead_cohort"].(map[string]any)
	if cohort["ever_booked_leads"] != float64(3) || cohort["ever_won_leads"] != float64(1) || cohort["time_to_first_booking_seconds"].(map[string]any)["average"] != float64(7200) {
		t.Fatal(cohort)
	}
	// Merely being past the meeting end never synthesizes a no-show.
	pending, _ := db.Lead(ctx, unknown.ID)
	if pending.Booking.Outcome != "" {
		t.Fatal("unknown automatically marked absent")
	}
	correction := management.Action{Kind: "attendance", BookingID: missed.Booking.ID, Outcome: "attended", Reason: "Presença conferida pela equipe"}
	if _, _, err = f.s.ApplyAction(ctx, f.user, missed.ID, "correct-presence", correction); err != nil {
		t.Fatal(err)
	}
	out = decodeMap(t, panelCall(t, f, "GET", "/metrics", nil, ""))
	if out["meeting_cohort"].(map[string]any)["show_up_rate"] != float64(1) {
		t.Fatal("correction was double-counted", out)
	}
	var recovery int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE lead_id=$1 AND payload->>'purpose'='recovery'`, missed.ID).Scan(&recovery); err != nil || recovery != 1 {
		t.Fatal(recovery, err)
	}
}
func TestManagementCallTranscriptFollowupsAndAudit(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	st := testState("history-lead", now)
	publishEffects(t, db, "seed-history", st)
	_, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.voice_requests(id,lead_id,mode,sender,purpose,state) VALUES('call-test',$1,'live','+5565999999901','confirm','completed')`, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, role := range []string{"user", "assistant"} {
		if _, err = db.Pool.Exec(ctx, `INSERT INTO bia_go.voice_transcripts(request_id,item_id,role,body) VALUES('call-test',$1,$2,'Texto da conversa')`, fmt.Sprint(i), role); err != nil {
			t.Fatal(err)
		}
	}
	for i, status := range []string{"done", "simulated", "skipped", "dead"} {
		fx := scheduledMessage(fmt.Sprint("followup-", i), st, "followup", now)
		raw, _ := json.Marshal(fx)
		if _, err = db.Pool.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,status,due_at,completed_at,result) VALUES($1,$2,'effect','live',$3,$1,$4,$5,$5,'{"status":"sent"}')`, fx.ID, st.ID, raw, status, now); err != nil {
			t.Fatal(err)
		}
	}
	lead := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID, nil, ""))
	if lead["followups_sent"] != float64(1) {
		t.Fatal("counted simulated/skipped sends", lead)
	}
	calls := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/calls", nil, ""))
	if len(calls["items"].([]any)) != 1 {
		t.Fatal(calls)
	}
	tr := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/calls/call-test/transcript?limit=1", nil, ""))
	if len(tr["items"].([]any)) != 1 || tr["next_after_seq"] == nil || tr["recording_available"] != false {
		t.Fatal(tr)
	}
	fu := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/followups", nil, ""))
	if len(fu["items"].([]any)) != 4 {
		t.Fatal(fu)
	}
	if _, _, err = f.s.ApplyAction(ctx, f.user, st.ID, "note", management.Action{Kind: "note", Text: "Nota interna da equipe"}); err != nil {
		t.Fatal(err)
	}
	audit := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/actions", nil, ""))
	if len(audit["items"].([]any)) != 1 {
		t.Fatal(audit)
	}
	if out := panelCall(t, f, "GET", "/leads/"+st.ID+"/calls/wrong-call/transcript", nil, ""); out.Code != 404 {
		t.Fatal("cross-contact call access not scoped", out.Code)
	}
}

func TestManagementReleaseAcceptsNewInboundWithSecondPrecisionTimestamp(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	st := testState("release-context", now.Add(-time.Hour))
	st.Human = true
	publishEffects(t, db, "seed-release", st)
	if _, _, err := f.s.ApplyAction(ctx, f.user, st.ID, "release", management.Action{Kind: "release"}); err != nil {
		t.Fatal(err)
	}
	// Twilio timestamps have second precision; receipt time identifies new input.
	submit(t, db, inbound("new-after-release", st.ID, "Quero entender melhor", now.Truncate(time.Second)), "shadow")
	p := fastWindowPolicy()
	ingest(t, db, p, now)
	ev := frozen(t, db, p, now.Add(2*time.Second))
	if ev.Context.ReceivedAt.IsZero() {
		t.Fatal("missing server receipt time")
	}
	runner := &fakeRunner{}
	w := baseWorker(db)
	w.Planner = runner
	w.Now = func() time.Time { return now.Add(2 * time.Second) }
	drain(t, w)
	if runner.calls != 1 {
		t.Fatal("fresh inbound was incorrectly dropped", runner.calls)
	}
}

func TestManagementTakeoverStopsRingingCallThroughVoiceWorker(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	st := testState("ringing-lead", now)
	publishEffects(t, db, "seed-ringing", st)
	_, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.voice_requests(id,lead_id,mode,sender,purpose,state,call_sid) VALUES('ringing-call',$1,'live','+5565999999901','confirm','ringing',$2)`, st.ID, "CA"+strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.ApplyAction(ctx, f.user, st.ID, "interrupt-ringing", management.Action{Kind: "takeover"}); err != nil {
		t.Fatal(err)
	}
	transport := &fakeVoice{}
	w := &voiceworker.Worker{Repo: voiceworker.Repository{DB: db}, Provider: transport, Live: true}
	for range 2 {
		if _, err = w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if transport.hangups != 1 {
		t.Fatal("operator stop duplicated or missing", transport.hangups)
	}
	calls := decodeMap(t, panelCall(t, f, "GET", "/leads/"+st.ID+"/calls", nil, ""))
	if calls["items"].([]any)[0].(map[string]any)["stop_state"] != "done" {
		t.Fatal(calls)
	}
}
