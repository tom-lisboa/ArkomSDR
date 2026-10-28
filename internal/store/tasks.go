package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
)

//go:embed pipeline.sql
var pipelineSchema string

type Task struct {
	ID, Queue, Kind, LeadID, Mode, Token string
	Payload                              json.RawMessage
	Attempts                             int
}

func enqueueTask(ctx context.Context, tx pgx.Tx, id, queue, kind, lead, mode string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.worker_tasks(id,queue,kind,lead_id,mode,payload,fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, id, queue, kind, lead, mode, b, hash(b))
	return e
}

func (d *DB) SubmitInbound(ctx context.Context, e domain.Event, mode string) (bool, error) {
	return d.submitInbound(ctx, e, mode, nil)
}

// The inbound WhatsApp message authorizes a reply in its service window.
// Existing consent, owner, opt-out and human-control state are never overwritten.
func (d *DB) SubmitWhatsAppInbound(ctx context.Context, e domain.Event, mode string, profile domain.Form) (bool, error) {
	s := domain.State{ID: e.LeadID, Owner: "go", Stage: "new", CreatedAt: e.At, Profile: profile}
	return d.submitInbound(ctx, e, mode, &s)
}

func (d *DB) submitInbound(ctx context.Context, e domain.Event, mode string, seed *domain.State) (bool, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, e.ID); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE id=$1)`, e.ID).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, ErrConflict
	}

	b, _ := json.Marshal(e)
	initial, _ := json.Marshal(domain.State{ID: e.LeadID, Owner: "shadow", Stage: "new", CreatedAt: time.Now().UTC()})
	if seed != nil {
		initial, _ = json.Marshal(seed)
	}
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.leads(id,state) VALUES($1,$2) ON CONFLICT DO NOTHING`, e.LeadID, initial)
	if err != nil {
		return false, err
	}
	if err = claimTestForm(ctx, tx, e, mode, seed); err != nil {
		return false, err
	}
	ct, err := tx.Exec(ctx, `INSERT INTO bia_go.worker_tasks(id,queue,kind,lead_id,mode,payload,fingerprint) VALUES($1,'context','inbound',$2,$3,$4,$5) ON CONFLICT DO NOTHING`, e.ID, e.LeadID, mode, b, hash(b))
	if err != nil {
		return false, err
	}
	if ct.RowsAffected() == 0 {
		var old string
		err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.worker_tasks WHERE id=$1`, e.ID).Scan(&old)
		if err != nil {
			return false, err
		}
		if old != hash(b) {
			return false, ErrConflict
		}
	}
	return ct.RowsAffected() == 1, tx.Commit(ctx)
}

func (d *DB) ClaimTask(ctx context.Context, queue string) (Task, error) {
	var t Task
	t.Token = token()
	e := d.Pool.QueryRow(ctx, `UPDATE bia_go.worker_tasks SET status='running',lease_token=$2,lease_until=now()+interval '2 minutes',attempts=attempts+1
 WHERE id=(SELECT id FROM bia_go.worker_tasks WHERE queue=$1 AND due_at<=now() AND (status='pending' OR(status='running' AND lease_until<now())) ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 RETURNING id,queue,kind,lead_id,mode,payload,attempts`, queue, t.Token).Scan(&t.ID, &t.Queue, &t.Kind, &t.LeadID, &t.Mode, &t.Payload, &t.Attempts)
	return t, e
}

func ackTask(ctx context.Context, tx pgx.Tx, t Task) error {
	ct, e := tx.Exec(ctx, `UPDATE bia_go.worker_tasks SET status='done',lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, t.ID, t.Token)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	return nil
}

func (d *DB) FailTask(ctx context.Context, t Task, code string) error {
	status := "pending"
	if t.Attempts >= 5 {
		status = "dead"
	}
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	ct, e := tx.Exec(ctx, `UPDATE bia_go.worker_tasks SET status=$3,error_code=$4,lease_token=NULL,lease_until=NULL,due_at=now()+interval '2 seconds' WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, t.ID, t.Token, status, code)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	if status == "dead" && t.Queue == "media" {
		var req conversation.MediaRequest
		if json.Unmarshal(t.Payload, &req) == nil {
			e = enqueueTask(ctx, tx, "media-result/"+t.ID, "context", "media.completed", t.LeadID, t.Mode, conversation.MediaResult{MediaRequest: req, ErrorCode: code})
			if e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}

// Media emits a result; it never writes the conversation history or response head.
func (d *DB) CompleteMedia(ctx context.Context, t Task, r conversation.MediaResult) error {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = ackTask(ctx, tx, t); e != nil {
		return e
	}
	if e = enqueueTask(ctx, tx, "media-result/"+t.ID, "context", "media.completed", t.LeadID, t.Mode, r); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (d *DB) HasNewInput(ctx context.Context, lead, conv string, version int64) (bool, error) {
	var b bool
	e := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.worker_tasks WHERE lead_id=$1 AND kind='inbound' AND status IN('pending','running')) OR EXISTS(SELECT 1 FROM bia_go.conversations WHERE id=$2 AND version>$3) OR EXISTS(SELECT 1 FROM bia_go.conversation_messages WHERE conversation_id=$2 AND role='user' AND run_id IS NULL) OR ($2='' AND EXISTS(SELECT 1 FROM bia_go.conversations WHERE lead_id=$1 AND (flush_at IS NOT NULL OR active_run IS NOT NULL)))`, lead, conv, version).Scan(&b)
	return b, e
}

func (d *DB) TaskAsJob(ctx context.Context, id string) (Job, error) {
	var j Job
	e := d.Pool.QueryRow(ctx, `SELECT id,lead_id,kind,mode,payload,attempts,status,error_code,queue FROM bia_go.worker_tasks WHERE id=$1`, id).Scan(&j.ID, &j.LeadID, &j.Kind, &j.Mode, &j.Payload, &j.Attempts, &j.Status, &j.ErrorCode, &j.Queue)
	return j, e
}

func taskNoRows(e error) bool { return errors.Is(e, pgx.ErrNoRows) }
