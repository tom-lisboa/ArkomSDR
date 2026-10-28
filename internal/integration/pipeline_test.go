package integration

import (
	"arkom/bia/internal/agent"
	"arkom/bia/internal/contextworker"
	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"sync"
	"testing"
	"time"
)

// Short deterministic windows keep the pre-existing ordering/lease scenarios
// independent of production tuning. Production timings are exercised below.
func fastWindowPolicy() store.WindowPolicy {
	p := store.DefaultWindowPolicy()
	p.Quiet = 1500 * time.Millisecond
	p.MaxWait = 8 * time.Second
	p.MediaQuiet = 0
	p.MediaSettle = 0
	p.MediaTimeout = 90 * time.Second
	return p
}

func ingest(t *testing.T, db *store.DB, p store.WindowPolicy, now time.Time) {
	t.Helper()
	for i := 0; i < 100; i++ {
		task, e := db.ClaimTask(context.Background(), "context")
		if errors.Is(e, pgx.ErrNoRows) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.ApplyContext(context.Background(), task, p, now); e != nil {
			t.Fatal(e)
		}
	}
	t.Fatal("context inbox stuck")
}
func frozen(t *testing.T, db *store.DB, p store.WindowPolicy, now time.Time) domain.Event {
	t.Helper()
	h, e := db.FlushWindow(context.Background(), "", p, now)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(h.MessageID, "run/") {
		t.Fatal("did not publish run", h)
	}
	if _, e = db.Pool.Exec(context.Background(), `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, h.MessageID); e != nil {
		t.Fatal(e)
	}
	j, e := db.Job(context.Background(), h.MessageID)
	if e != nil {
		t.Fatal(e)
	}
	var ev domain.Event
	if e = json.Unmarshal(j.Payload, &ev); e != nil {
		t.Fatal(e)
	}
	return ev
}
func inbound(id, lead, text string, at time.Time) domain.Event {
	return domain.Event{ID: id, LeadID: lead, Kind: "message.received", Text: text, At: at, ConversationID: "whatsapp-test"}
}

type fakeRunner struct {
	calls int
	event domain.Event
}

func (f *fakeRunner) Plan(context.Context, domain.State, domain.Event, []domain.Slot) (domain.Decision, error) {
	panic("execution must use Run")
}
func (f *fakeRunner) Run(_ context.Context, _ domain.State, e domain.Event, _ []domain.Slot, id string) (agent.Outcome, error) {
	f.calls++
	f.event = e
	return agent.Outcome{Decision: domain.Decision{Action: "reply", Message: "Entendi sua dúvida."}, ResponseID: "resp_test", InputTokens: 100, UsedPrevious: e.Context.Heads[id].ResponseID != ""}, nil
}

type fakeMedia struct{}

func (fakeMedia) Process(context.Context, conversation.Attachment) (string, error) {
	return "Prefiro amanhã de manhã", nil
}

func TestFragmentsRunAndContextOwnership(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	w := baseWorker(db)
	f := form("form-fragments", "+5565999999911")
	submit(t, db, f, "shadow")
	drain(t, w)
	now := time.Now().UTC()
	p := fastWindowPolicy()
	for i, text := range []string{"oi", "quero entender", "como funciona"} {
		ev := inbound("fragment-"+string(rune('a'+i)), f.LeadID, text, now.Add(time.Duration(i)*time.Millisecond))
		submit(t, db, ev, "shadow")
		if ok, e := db.Submit(ctx, ev, "shadow"); e != nil || ok {
			t.Fatal("duplicate accepted", e)
		}
	}
	ingest(t, db, p, now)
	if _, e := db.FlushWindow(ctx, "", p, now); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("did not debounce", e)
	}
	ev := frozen(t, db, p, now.Add(2*time.Second))
	if ev.Text != "oi\nquero entender\ncomo funciona" || len(ev.Context.MessageIDs) != 3 {
		t.Fatalf("bad batch %+v", ev)
	}
	if _, e := db.FlushWindow(ctx, "", p, now.Add(3*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("second concurrent run", e)
	}
	runner := &fakeRunner{}
	w.Planner = runner
	drain(t, w)
	if runner.calls != 1 {
		t.Fatal("unexpected runs", runner.calls)
	}
	var heads []byte
	if e := db.Pool.QueryRow(ctx, `SELECT heads FROM bia_go.conversations`).Scan(&heads); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(heads), "resp_test") {
		t.Fatal("execution wrote context head")
	}
	ingest(t, db, p, now.Add(3*time.Second))
	if e := db.Pool.QueryRow(ctx, `SELECT heads FROM bia_go.conversations`).Scan(&heads); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(heads), "resp_test") {
		t.Fatal("context did not persist result", string(heads))
	}
	submit(t, db, inbound("next-question", f.LeadID, "e o próximo passo?", now.Add(4*time.Second)), "shadow")
	ingest(t, db, p, now.Add(4*time.Second))
	ev = frozen(t, db, p, now.Add(6*time.Second))
	if ev.Context.Heads["sdr"].ResponseID != "resp_test" || len(ev.Context.History) != 3 {
		t.Fatalf("missing previous head/history: %+v", ev.Context)
	}
	for _, m := range ev.Context.History {
		if m.Role == "assistant" {
			t.Fatal("shadow manufactured delivered message")
		}
	}
}

func TestMediaWaitAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "timeout"}[timeout], func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			p := fastWindowPolicy()
			ev := inbound("audio-msg", "lead-media", "", now)
			ev.Attachments = []conversation.Attachment{{ID: "voice", Kind: "audio", URL: "https://media.example/audio.ogg"}}
			submit(t, db, ev, "shadow")
			ingest(t, db, p, now)
			h, e := db.FlushWindow(ctx, "", p, now.Add(2*time.Second))
			if e != nil || h.MessageID != "" || h.Due.IsZero() {
				t.Fatal("did not wait for media", h, e)
			}
			if timeout {
				ev = frozen(t, db, p, now.Add(2*time.Minute))
				if !ev.Context.MediaFailed {
					t.Fatal("timeout not explicit")
				}
			} else {
				mw := &mediaworker.Worker{DB: db, Processor: fakeMedia{}}
				if done, e := mw.Once(ctx); e != nil || !done {
					t.Fatal(done, e)
				}
				ingest(t, db, p, now.Add(3*time.Second))
				ev = frozen(t, db, p, now.Add(3*time.Second))
				if !strings.Contains(ev.Text, "Prefiro amanhã") || ev.Context.MediaFailed {
					t.Fatalf("bad media result %+v", ev)
				}
			}
		})
	}
}

func TestRedisAbsentStillFlushesAndSuppressesOldEffect(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	w := baseWorker(db)
	f := form("form-stale", "+5565999999912")
	submit(t, db, f, "shadow")
	drain(t, w)
	now := time.Now().UTC()
	p := fastWindowPolicy()
	cw := &contextworker.Worker{DB: db, Policy: p, Now: func() time.Time { return now }}
	submit(t, db, inbound("old-question", f.LeadID, "Como funciona?", now), "shadow")
	if done, e := cw.Once(ctx); e != nil || !done {
		t.Fatal(done, e)
	}
	now = now.Add(2 * time.Second)
	if done, e := cw.Once(ctx); e != nil || !done {
		t.Fatal("PG fallback failed", done, e)
	}
	if _, e := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE status='pending' AND kind='event'`); e != nil {
		t.Fatal(e)
	}
	w.Planner = &fakeRunner{}
	if done, e := w.Once(ctx); e != nil || !done {
		t.Fatal(done, e)
	}
	submit(t, db, inbound("new-question", f.LeadID, "Espere, minha dúvida é outra", now), "shadow")
	drain(t, w)
	var n int
	if e := db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE status='skipped' AND result->>'reason'='new_input_superseded_run'`).Scan(&n); e != nil || n != 1 {
		t.Fatal("stale response was not suppressed", n, e)
	}
}

func TestHeadRotationAndExpiredTaskFence(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := fastWindowPolicy()
	submit(t, db, inbound("head-msg", "lead-head", "teste", now), "shadow")
	task, e := db.ClaimTask(ctx, "context")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.worker_tasks SET lease_until=now()-interval '1 second' WHERE id=$1`, task.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ApplyContext(ctx, task, p, now); !errors.Is(e, store.ErrLease) {
		t.Fatal("expired writer accepted", e)
	}
	ingest(t, db, p, now)
	heads := map[string]conversation.Head{"sdr": {ResponseID: "expired", At: now.Add(-21 * time.Hour)}, "scheduling": {ResponseID: "deep", At: now, Depth: 8}, "recovery": {ResponseID: "large", At: now, InputTokens: 16000}}
	raw, _ := json.Marshal(heads)
	if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.conversations SET heads=$1`, raw); e != nil {
		t.Fatal(e)
	}
	ev := frozen(t, db, p, now.Add(2*time.Second))
	if len(ev.Context.Heads) != 0 {
		t.Fatal("unbounded chain", ev.Context.Heads)
	}
}

func TestContextNeverAcceptsForgedWindowOrCrossQueueID(t *testing.T) {
	now := time.Now().UTC()
	ev := inbound("same-id", "lead", "sair", now)
	ev.Context = &conversation.Window{Heads: map[string]conversation.Head{"sdr": {ResponseID: "injected"}}}
	if domain.ValidateEvent(ev) == nil {
		t.Fatal("external context accepted")
	}
	db := setup(t)
	ev.Context = nil
	submit(t, db, ev, "shadow")
	ev.Kind = "human.takeover"
	if _, e := db.Submit(context.Background(), ev, "shadow"); !errors.Is(e, store.ErrConflict) {
		t.Fatal("cross-queue id reused", e)
	}
}

func TestFragmentedStopBypassesModel(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := fastWindowPolicy()
	w := baseWorker(db)
	f := form("form-stop", "+5565999999913")
	submit(t, db, f, "shadow")
	drain(t, w)
	submit(t, db, inbound("stop-1", f.LeadID, "oi", now), "shadow")
	submit(t, db, inbound("stop-2", f.LeadID, "sair", now.Add(time.Millisecond)), "shadow")
	ingest(t, db, p, now)
	frozen(t, db, p, now.Add(2*time.Second))
	runner := &fakeRunner{}
	w.Planner = runner
	drain(t, w)
	s, e := db.Lead(ctx, f.LeadID)
	if e != nil || !s.OptedOut || runner.calls != 0 {
		t.Fatal("fragmented opt-out missed", s.OptedOut, runner.calls, e)
	}
}

func TestConcurrentIngestionPublishesOneRun(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := fastWindowPolicy()
	for i := 0; i < 20; i++ {
		submit(t, db, inbound(fmt.Sprintf("concurrent-%02d", i), "concurrent-lead", fmt.Sprintf("parte %02d", i), now.Add(time.Duration(i)*time.Millisecond)), "shadow")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task, e := db.ClaimTask(ctx, "context")
				if errors.Is(e, pgx.ErrNoRows) {
					return
				}
				if e != nil {
					errs <- e
					return
				}
				if _, e = db.ApplyContext(ctx, task, p, now); e != nil {
					errs <- e
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	ev := frozen(t, db, p, now.Add(2*time.Second))
	if len(ev.Context.MessageIDs) != 20 || ev.Context.Version != 20 {
		t.Fatal("lost fragment", ev.Context)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := db.FlushWindow(ctx, "", p, now.Add(3*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
				t.Errorf("duplicate run: %v", e)
			}
		}()
	}
	wg.Wait()
}

func TestMultipleAudioAndImageWaitForWholeBatch(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	p := store.DefaultWindowPolicy()
	now := time.Now().UTC()
	for i, kind := range []string{"audio", "audio", "image"} {
		ev := inbound(fmt.Sprintf("part-%d", i), "media-batch", "", now.Add(time.Duration(i)*4*time.Second))
		ev.Attachments = []conversation.Attachment{{ID: "asset", Kind: kind, URL: "https://cdn.example/asset"}}
		submit(t, db, ev, "shadow")
		ingest(t, db, p, ev.At)
	}
	// Text received while a media turn is pending must retain the longer window.
	submit(t, db, inbound("caption", "media-batch", "essas são as informações", now.Add(10*time.Second)), "shadow")
	ingest(t, db, p, now.Add(10*time.Second))
	if _, e := db.FlushWindow(ctx, "", p, now.Add(16*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("premature text-only flush", e)
	}
	for i := 0; i < 2; i++ {
		task, e := db.ClaimTask(ctx, "media")
		if e != nil {
			t.Fatal(e)
		}
		var req conversation.MediaRequest
		json.Unmarshal(task.Payload, &req)
		if e = db.CompleteMedia(ctx, task, conversation.MediaResult{MediaRequest: req, Text: "transcrição " + req.MessageID}); e != nil {
			t.Fatal(e)
		}
	}
	ingest(t, db, p, now.Add(23*time.Second))
	if h, e := db.FlushWindow(ctx, "", p, now.Add(31*time.Second)); e != nil || h.MessageID != "" {
		t.Fatal("did not await final image beyond batch deadline", h, e)
	}
	task, e := db.ClaimTask(ctx, "media")
	if e != nil {
		t.Fatal(e)
	}
	var req conversation.MediaRequest
	json.Unmarshal(task.Payload, &req)
	if e = db.CompleteMedia(ctx, task, conversation.MediaResult{MediaRequest: req, Text: "imagem final"}); e != nil {
		t.Fatal(e)
	}
	ingest(t, db, p, now.Add(35*time.Second))
	if _, e := db.FlushWindow(ctx, "", p, now.Add(36*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("missing post-transcription grace", e)
	}
	ev := frozen(t, db, p, now.Add(39*time.Second))
	if len(ev.Context.MessageIDs) != 4 || ev.Context.MediaFailed || !strings.Contains(ev.Text, "imagem final") || !strings.Contains(ev.Text, "transcrição part-0") || !strings.Contains(ev.Text, "transcrição part-1") || !strings.Contains(ev.Text, "essas são") {
		t.Fatal(ev)
	}
}

func TestContextWaitsForQueuedFragmentAndTypingIsThrottled(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	p := store.DefaultWindowPolicy()
	now := time.Now().UTC()
	phone := "+5565999999900"
	ev := inbound("twilio-SM"+strings.Repeat("1", 32), domain.LeadKey(phone), "primeira parte", now)
	profile := domain.Form{Name: "Teste", Phone: phone, Consent: true}
	if _, e := db.SubmitWhatsAppInbound(ctx, ev, "live", profile); e != nil {
		t.Fatal(e)
	}
	ingest(t, db, p, now)
	target, e := db.ClaimTyping(ctx, now, phone)
	if e != nil || target.MessageID != ev.ID {
		t.Fatal(target, e)
	}
	if _, e = db.ClaimTyping(ctx, now, phone); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("duplicate typing", e)
	}
	if _, e = db.ClaimTyping(ctx, now.Add(16*time.Second), "+5565999999901"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("typing recipient fence", e)
	}
	if _, e = db.ClaimTyping(ctx, now.Add(16*time.Second), phone); e != nil {
		t.Fatal(e)
	}
	ev.ID = "twilio-MM" + strings.Repeat("2", 32)
	ev.Text = "outra parte"
	ev.At = now.Add(4 * time.Second)
	if _, e = db.SubmitWhatsAppInbound(ctx, ev, "live", profile); e != nil {
		t.Fatal(e)
	}
	if _, e = db.FlushWindow(ctx, "", p, now.Add(6*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("ignored queued inbound", e)
	}
	ingest(t, db, p, now.Add(6*time.Second))
	if _, e = db.FlushWindow(ctx, "", p, now.Add(9*time.Second)); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("did not extend quiet time", e)
	}
	run := frozen(t, db, p, now.Add(12*time.Second))
	if len(run.Context.MessageIDs) != 2 {
		t.Fatal(run)
	}
	if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.leads SET state=jsonb_set(state,'{opted_out}','true')`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ClaimTyping(ctx, now.Add(32*time.Second), phone); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("typing after optout", e)
	}
}
