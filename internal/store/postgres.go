package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

//go:embed routing.sql
var routingSchema string

//go:embed voice.sql
var voiceSchema string

//go:embed twilio.sql
var twilioSchema string

//go:embed typing.sql
var typingSchema string

//go:embed agenda.sql
var agendaSchema string

//go:embed test_form.sql
var testFormSchema string

//go:embed reminders.sql
var remindersSchema string

//go:embed form_contact.sql
var formContactSchema string

//go:embed manual_reminders.sql
var manualRemindersSchema string

//go:embed sms.sql
var smsSchema string

//go:embed form_booking.sql
var formBookingSchema string

//go:embed preparation.sql
var preparationSchema string

//go:embed intro_video.sql
var introVideoSchema string

//go:embed meeting_start.sql
var meetingStartSchema string

//go:embed management.sql
var managementSchema string

//go:embed sales_alerts.sql
var salesAlertsSchema string

var ErrConflict = errors.New("idempotency key reused with different payload")
var ErrLease = errors.New("lease lost; stale worker cannot commit")

type DB struct{ Pool *pgxpool.Pool }
type VoiceInvite struct{ Sender string }
type Job struct {
	GuardContext bool                    `json:"-"`
	VoiceInvite  *VoiceInvite            `json:"-"`
	Run          *conversation.RunResult `json:"-"`
	Delivery     *conversation.Delivery  `json:"-"`
	Queue        string                  `json:"queue"`
	ID           string                  `json:"id"`
	LeadID       string                  `json:"lead_id"`
	Kind         string                  `json:"kind"`
	Mode         string                  `json:"mode"`
	Payload      json.RawMessage         `json:"payload"`
	Token        string                  `json:"-"`
	Attempts     int                     `json:"attempts"`
	Status       string                  `json:"status"`
	Result       json.RawMessage         `json:"result,omitempty"`
	ErrorCode    *string                 `json:"error_code,omitempty"`
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	c, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	c.MaxConns = 12
	c.MinConns = 1
	c.MaxConnLifetime = 30 * time.Minute
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return &DB{Pool: p}, nil
}
func (d *DB) Migrate(ctx context.Context) error {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, schema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, pipelineSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, routingSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, voiceSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, twilioSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, typingSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, agendaSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, testFormSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, remindersSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, formContactSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, manualRemindersSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, smsSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, formBookingSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, preparationSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, introVideoSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, meetingStartSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, managementSchema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, salesAlertsSchema); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d *DB) Close() { d.Pool.Close() }

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func token() string {
	var b [16]byte
	_, e := rand.Read(b[:])
	if e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

func (d *DB) Submit(ctx context.Context, e domain.Event, mode string) (bool, error) {
	if e.Kind == "message.received" {
		return d.SubmitInbound(ctx, e, mode)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	fingerprintEvent := e
	if e.Kind == "form.submitted" || e.Kind == "form.contact_requested" || e.Kind == "contact.sms_consent" {
		fingerprintEvent.At = time.Time{}
	}
	fp, _ := json.Marshal(fingerprintEvent)
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, e.ID); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.worker_tasks WHERE id=$1)`, e.ID).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, ErrConflict
	}

	initial, _ := json.Marshal(domain.State{ID: e.LeadID, Owner: "shadow", Stage: "new", CreatedAt: time.Now().UTC()})
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.leads(id,state) VALUES($1,$2) ON CONFLICT DO NOTHING`, e.LeadID, initial)
	if err != nil {
		return false, err
	}
	ct, err := tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,now()) ON CONFLICT DO NOTHING`, e.ID, e.LeadID, mode, b, hash(fp))
	if err != nil {
		return false, err
	}
	inserted := ct.RowsAffected() == 1
	if !inserted {
		var old string
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.jobs WHERE id=$1`, e.ID).Scan(&old)
		if err != nil {
			return false, err
		}
		if old != hash(fp) {
			return false, ErrConflict
		}
	}
	return inserted, tx.Commit(ctx)
}

// A short transaction leases a lead, then its next job. Network calls happen
// outside transactions. All completion writes are fenced by the lease token.
func (d *DB) Claim(ctx context.Context) (Job, domain.State, error) {
	return d.ClaimQueue(ctx, "execution")
}

// A global per-lead lease spans all queues. Earlier due events in any queue
// must finish before a later event or an effect can use that lead's state.
func (d *DB) ClaimQueue(ctx context.Context, queue string) (Job, domain.State, error) {
	if queue != "execution" && queue != "scheduling" && queue != "followup" && queue != "media" {
		return Job{}, domain.State{}, errors.New("invalid job queue")
	}
	var j Job
	var s domain.State
	var raw []byte
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return j, s, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT l.id,l.state FROM bia_go.leads l
 WHERE (l.lease_until IS NULL OR l.lease_until < now()) AND EXISTS
 (SELECT 1 FROM bia_go.jobs j WHERE j.lead_id=l.id AND j.queue=$1 AND j.due_at<=now() AND j.status IN ('pending','running')
 AND NOT EXISTS(SELECT 1 FROM bia_go.jobs ev WHERE ev.lead_id=l.id AND ev.kind='event' AND ev.status IN ('pending','running') AND ev.due_at<=now() AND (j.kind='effect' OR ev.seq<j.seq)))
 ORDER BY (SELECT min(j.due_at) FROM bia_go.jobs j WHERE j.lead_id=l.id AND j.queue=$1 AND j.status IN ('pending','running'))
 LIMIT 1 FOR UPDATE OF l SKIP LOCKED`, queue).Scan(&j.LeadID, &raw)
	if e != nil {
		return j, s, e
	}
	if e = json.Unmarshal(raw, &s); e != nil {
		return j, s, e
	}
	// An abandoned external effect is ambiguous. It must be reconciled, never
	// automatically resent after a process crash or an expired lease.
	ct, e := tx.Exec(ctx, `UPDATE bia_go.jobs SET status='uncertain',error_code='worker_lost_during_effect',completed_at=now()
 WHERE lead_id=$1 AND kind='effect' AND status='running'`, j.LeadID)
	if e != nil {
		return j, s, e
	}
	if ct.RowsAffected() > 0 {
		s.Human = true
		s.Reasons = append(s.Reasons, "worker_lost_during_effect")
		raw, _ = json.Marshal(s)
		if _, e = tx.Exec(ctx, `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, j.LeadID, raw); e != nil {
			return j, s, e
		}
	}
	e = tx.QueryRow(ctx, `SELECT j.id,j.kind,j.mode,j.payload,j.attempts,j.queue FROM bia_go.jobs j
 WHERE j.lead_id=$1 AND j.queue=$2 AND j.status IN ('pending','running') AND j.due_at<=now()
 AND NOT EXISTS(SELECT 1 FROM bia_go.jobs ev WHERE ev.lead_id=j.lead_id AND ev.kind='event' AND ev.status IN ('pending','running') AND ev.due_at<=now() AND (j.kind='effect' OR ev.seq<j.seq))
 ORDER BY CASE WHEN j.kind='event' THEN 0 ELSE 1 END,j.seq LIMIT 1 FOR UPDATE`, j.LeadID, queue).Scan(&j.ID, &j.Kind, &j.Mode, &j.Payload, &j.Attempts, &j.Queue)
	if errors.Is(e, pgx.ErrNoRows) {
		_, e = tx.Exec(ctx, `UPDATE bia_go.leads SET lease_token=NULL,lease_until=NULL WHERE id=$1`, j.LeadID)
		if e != nil {
			return j, s, e
		}
		if e = tx.Commit(ctx); e != nil {
			return j, s, e
		}
		return Job{}, s, pgx.ErrNoRows
	}
	if e != nil {
		return j, s, e
	}
	j.Token = token()
	j.Attempts++
	_, e = tx.Exec(ctx, `UPDATE bia_go.leads SET lease_token=$2,lease_until=now()+interval '2 minutes' WHERE id=$1`, j.LeadID, j.Token)
	if e != nil {
		return j, s, e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.jobs SET status='running',lease_token=$2,attempts=attempts+1 WHERE id=$1`, j.ID, j.Token)
	if e != nil {
		return j, s, e
	}
	return j, s, tx.Commit(ctx)
}

func (d *DB) Finish(ctx context.Context, j Job, s domain.State, effects []domain.Effect, status string, result any) error {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if j.GuardContext && j.Run != nil && j.Kind == "event" {
		// Serialize the final assessment against the context writer. A newer
		// inbound invalidates the draft, including its commercial memory.
		var version int64
		if e = tx.QueryRow(ctx, `SELECT version FROM bia_go.conversations WHERE id=$1 FOR UPDATE`, j.Run.ConversationID).Scan(&version); e != nil {
			return e
		}
		var pending bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.worker_tasks WHERE lead_id=$1 AND kind='inbound' AND status IN('pending','running')) OR EXISTS(SELECT 1 FROM bia_go.conversation_messages WHERE conversation_id=$2 AND role='user' AND run_id IS NULL)`, j.LeadID, j.Run.ConversationID).Scan(&pending); e != nil {
			return e
		}
		if pending || version > j.Run.Version {
			var original []byte
			if e = tx.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE id=$1 AND lease_token=$2`, j.LeadID, j.Token).Scan(&original); e != nil {
				return e
			}
			s = domain.State{}
			if e = json.Unmarshal(original, &s); e != nil {
				return e
			}
			effects = nil
			status = "skipped"
			result = map[string]string{"reason": "new_input_superseded_assessment"}
			j.Run.Agent = ""
			j.Run.ResponseID = ""
			j.Run.PromptVersion = ""
		}
	}
	raw, e := json.Marshal(s)
	if e != nil {
		return e
	}
	ct, e := tx.Exec(ctx, `UPDATE bia_go.leads SET state=$3,revision=revision+1,lease_token=NULL,lease_until=NULL,updated_at=now()
 WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, j.LeadID, j.Token, raw)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	// Keep outcome history even when a later meeting replaces the current booking.
	if j.Kind == "event" && s.Booking != nil && (s.Booking.Outcome == "attended" || s.Booking.Outcome == "no_show") {
		var event domain.Event
		if json.Unmarshal(j.Payload, &event) == nil && (event.Kind == "attendance.attended" || event.Kind == "attendance.no_show") && event.BookingID == s.Booking.ID {
			_, e = tx.Exec(ctx, `INSERT INTO bia_go.meeting_attendance(booking_id,lead_id,starts_at,ends_at,outcome,recorded_at,source) VALUES($1,$2,$3,$4,$5,$6,'event') ON CONFLICT(booking_id) DO UPDATE SET outcome=excluded.outcome,recorded_at=excluded.recorded_at,source='event',actor_id=NULL WHERE meeting_attendance.recorded_at<excluded.recorded_at`, s.Booking.ID, s.ID, s.Booking.Start, s.Booking.End, s.Booking.Outcome, event.At)
			if e != nil {
				return e
			}
		}
	}
	// Pending invitations cannot survive a cancellation, reschedule or contact
	// pause. Completed/in-flight calls keep their provider audit history.
	bookingID := ""
	var bookingStart *time.Time
	if s.Booking != nil {
		bookingID = s.Booking.ID
		bookingStart = &s.Booking.Start
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.voice_requests SET state='skipped',error_code='meeting_or_contact_changed',updated_at=now()
 WHERE lead_id=$1 AND scheduled_reminder AND state IN ('new','awaiting_permission','ready')
 AND (booking_id IS DISTINCT FROM $2 OR booking_start IS DISTINCT FROM $3::timestamptz OR $4)`, s.ID, bookingID, bookingStart, s.OptedOut || s.Paused || s.Human || !s.Profile.Consent || s.Owner != "go" || (s.Booking != nil && s.Booking.Outcome != ""))
	if e != nil {
		return e
	}
	// Form submission never initiates reminder calls. They require an existing booking.
	for _, fx := range effects {
		b, e := json.Marshal(fx)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'effect',$3,$4,$5,$6) ON CONFLICT DO NOTHING`, fx.ID, fx.LeadID, j.Mode, b, hash(b), fx.Due)
		if e != nil {
			return e
		}
	}
	r, e := json.Marshal(result)
	if e != nil {
		return e
	}
	if j.Run != nil {
		j.Run.Status = status
		if e = enqueueTask(ctx, tx, "run-result/"+j.ID, "context", "run.completed", j.LeadID, j.Mode, j.Run); e != nil {
			return e
		}
	}
	if j.Delivery != nil && status == "done" {
		if e = enqueueTask(ctx, tx, "delivery/"+j.ID, "context", "delivery.completed", j.LeadID, j.Mode, j.Delivery); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.jobs SET status=$3,result=$4,completed_at=now(),lease_token=NULL,error_code=CASE WHEN $3='done' THEN NULL ELSE error_code END WHERE id=$1 AND lease_token=$2`, j.ID, j.Token, status, r)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (d *DB) Fail(ctx context.Context, j Job, code string, retry bool) error {
	status := "dead"
	if retry && j.Attempts < 5 {
		status = "pending"
	}
	delay := time.Duration(1<<min(j.Attempts, 8)) * time.Second
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	ct, e := tx.Exec(ctx, `UPDATE bia_go.leads SET lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, j.LeadID, j.Token)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.jobs SET status=$3,error_code=$4,due_at=now()+$5::interval,lease_token=NULL WHERE id=$1 AND lease_token=$2`, j.ID, j.Token, status, code, delay.String())
	if e != nil {
		return e
	}
	if status == "dead" {
		var ev domain.Event
		if json.Unmarshal(j.Payload, &ev) == nil && ev.Context != nil {
			r := conversation.RunResult{ConversationID: ev.Context.ConversationID, RunID: j.ID, Version: ev.Context.Version, Status: "failed"}
			if e = enqueueTask(ctx, tx, "run-result/"+j.ID, "context", "run.completed", j.LeadID, j.Mode, r); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}

func (d *DB) Lead(ctx context.Context, id string) (domain.State, error) {
	var s domain.State
	var b []byte
	e := d.Pool.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE id=$1`, id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &s)
	}
	return s, e
}
func (d *DB) Job(ctx context.Context, id string) (Job, error) {
	var j Job
	e := d.Pool.QueryRow(ctx, `SELECT id,lead_id,kind,mode,payload,attempts,status,result,error_code,queue FROM bia_go.jobs WHERE id=$1`, id).Scan(&j.ID, &j.LeadID, &j.Kind, &j.Mode, &j.Payload, &j.Attempts, &j.Status, &j.Result, &j.ErrorCode, &j.Queue)
	if errors.Is(e, pgx.ErrNoRows) {
		return d.TaskAsJob(ctx, id)
	}
	return j, e
}
func (d *DB) PendingEvent(ctx context.Context, id string) (bool, error) {
	var b bool
	e := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE lead_id=$1 AND kind='event' AND status='pending')`, id).Scan(&b)
	return b, e
}
func (d *DB) Counts(ctx context.Context, id string) (int, int, error) {
	var lead, global int
	e := d.Pool.QueryRow(ctx, `SELECT
 count(*) FILTER(WHERE lead_id=$1),
 count(*) FILTER(WHERE COALESCE(completed_at,due_at)>now()-interval '1 hour')
 FROM bia_go.jobs WHERE mode='live' AND status IN ('done','running','uncertain') AND kind='effect' AND payload->>'kind' IN ('send','send_audio','send_sms','send_video') AND COALESCE(result->>'chunked','false')<>'true' AND COALESCE(completed_at,due_at)>now()-interval '24 hours'`, id).Scan(&lead, &global)
	return lead, global, e
}

func (d *DB) Metrics(ctx context.Context) (map[string]any, error) {
	var pending, uncertain, dead, simulated, sent int64
	e := d.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='uncertain'),count(*) FILTER(WHERE status='dead'),count(*) FILTER(WHERE status='simulated'),count(*) FILTER(WHERE status='done' AND kind='effect' AND payload->>'kind' IN ('send','send_audio','send_sms','send_video') AND COALESCE(result->>'chunked','false')<>'true') FROM bia_go.jobs`).Scan(&pending, &uncertain, &dead, &simulated, &sent)
	if e != nil {
		return nil, e
	}
	var attended, noShow int64
	var p95 *float64
	e = d.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state->>'stage'='attended'),count(*) FILTER(WHERE state->>'stage'='no_show'),
 percentile_cont(0.95) WITHIN GROUP(ORDER BY extract(epoch FROM ((state->>'first_contact_at')::timestamptz-(state->>'created_at')::timestamptz))) FILTER(WHERE state->>'first_contact_at' IS NOT NULL)
 FROM bia_go.leads`).Scan(&attended, &noShow, &p95)
	if e != nil {
		return nil, e
	}
	var contextPending, mediaPending, taskDead, activeRuns int64
	e = d.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE queue='context' AND status IN('pending','running')),count(*) FILTER(WHERE queue='media' AND status IN('pending','running')),count(*) FILTER(WHERE status='dead') FROM bia_go.worker_tasks`).Scan(&contextPending, &mediaPending, &taskDead)
	if e != nil {
		return nil, e
	}
	e = d.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.conversations WHERE active_run IS NOT NULL`).Scan(&activeRuns)
	if e != nil {
		return nil, e
	}
	queues := map[string]int64{"execution": 0, "scheduling": 0, "followup": 0, "media": 0}
	rows, e := d.Pool.Query(ctx, `SELECT queue,count(*) FROM bia_go.jobs WHERE status IN ('pending','running') GROUP BY queue`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var q string
		var n int64
		if e = rows.Scan(&q, &n); e != nil {
			return nil, e
		}
		queues[q] = n
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return map[string]any{"jobs_pending_by_queue": queues, "context_pending": contextPending, "media_pending": mediaPending, "worker_tasks_dead": taskDead, "active_runs": activeRuns, "pending": pending, "uncertain": uncertain, "dead": dead, "simulated": simulated, "sent": sent, "leads_latest_attended": attended, "leads_latest_no_show": noShow, "first_contact_p95_seconds": p95}, e
}
