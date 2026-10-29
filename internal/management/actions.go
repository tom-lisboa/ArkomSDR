package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
)

type Action struct {
	Kind             string `json:"kind"`
	Reason           string `json:"reason,omitempty"`
	Text             string `json:"text,omitempty"`
	AssignedTo       string `json:"assigned_to,omitempty"`
	Status           string `json:"status,omitempty"`
	BookingID        string `json:"booking_id,omitempty"`
	Outcome          string `json:"outcome,omitempty"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var req Action
	if !decode(w, r, &req) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !domain.ValidID(key) || len(key) > 100 {
		fail(w, 422, "idempotency_key_required")
		return
	}
	if len(req.Text) > 4000 || len(req.Reason) > 1000 {
		fail(w, 422, "text_too_long")
		return
	}
	result, duplicate, err := s.ApplyAction(r.Context(), actor(r), r.PathValue("id"), key, req)
	if err != nil {
		var e *ActionError
		if errors.As(err, &e) {
			write(w, e.Status, map[string]any{"error": e.Code, "request_id": w.Header().Get("X-Request-ID"), "retryable": e.Code == "contact_busy"})
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 404, "not_found")
			return
		}
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"result": result, "duplicate": duplicate})
}

type ActionError struct {
	Status int
	Code   string
}

func (e *ActionError) Error() string { return e.Code }
func conflict(code string) error     { return &ActionError{409, code} }
func invalid(code string) error      { return &ActionError{422, code} }

// ApplyAction uses the same lead lock and lease fence as the SDR. Draft runs can
// be interrupted; an already-started external effect must finish before a write.
func (s *Server) ApplyAction(ctx context.Context, u User, lead, key string, a Action) (map[string]any, bool, error) {
	if !domain.ValidID(lead) {
		return nil, false, invalid("invalid_lead")
	}
	id := "op_" + digest(u.ID + "\x00" + key)[:40]
	raw, _ := json.Marshal(a)
	fp := digest(lead + "\x00" + string(raw))
	now := s.now()
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
		return nil, false, err
	}
	var old string
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT fingerprint,result FROM bia_go.management_actions WHERE id=$1`, id).Scan(&old, &prior)
	if err == nil {
		if old != fp {
			return nil, false, conflict("idempotency_conflict")
		}
		var out map[string]any
		err = json.Unmarshal(prior, &out)
		return out, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	var stateRaw []byte
	var revision int64
	var lease *time.Time
	if err = tx.QueryRow(ctx, `SELECT state,revision,lease_until FROM bia_go.leads WHERE id=$1 FOR UPDATE`, lead).Scan(&stateRaw, &revision, &lease); err != nil {
		return nil, false, err
	}
	var st domain.State
	if err = json.Unmarshal(stateRaw, &st); err != nil {
		return nil, false, err
	}
	if a.ExpectedRevision != nil && *a.ExpectedRevision != revision {
		return nil, false, conflict("revision_conflict")
	}
	var runningEffect bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE lead_id=$1 AND status='running' AND kind='effect')`, lead).Scan(&runningEffect); err != nil {
		return nil, false, err
	}
	metadata := a.Kind == "assign" || a.Kind == "note"
	interrupt := a.Kind == "takeover" || a.Kind == "pause" || a.Kind == "status"
	if !metadata && (runningEffect || (lease != nil && lease.After(now) && !interrupt)) {
		return nil, false, conflict("contact_busy")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO bia_go.lead_management(lead_id) VALUES($1) ON CONFLICT DO NOTHING`, lead); err != nil {
		return nil, false, err
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM bia_go.lead_management WHERE lead_id=$1`, lead).Scan(&status); err != nil {
		return nil, false, err
	}
	effects := []domain.Effect{}
	changeState := !metadata
	result := map[string]any{"action_id": id, "lead_id": lead, "kind": a.Kind, "at": now}
	switch a.Kind {
	case "takeover":
		if st.OptedOut {
			return nil, false, conflict("contact_opted_out")
		}
		st.Human = true
		st.Paused = false
		st.AutomationSince = now
	case "pause":
		st.Paused = true
		st.Human = true
		st.AutomationSince = now
	case "release", "resume":
		if st.OptedOut || !st.Profile.Consent || status != "open" {
			return nil, false, conflict("automation_not_permitted")
		}
		st.Human = false
		st.Paused = false
		st.AutomationSince = now
	case "assign":
		var target any
		if a.AssignedTo != "" {
			var enabled bool
			if err = tx.QueryRow(ctx, `SELECT enabled FROM bia_go.management_users WHERE id=$1`, a.AssignedTo).Scan(&enabled); err != nil || !enabled {
				return nil, false, invalid("invalid_assignee")
			}
			target = a.AssignedTo
		}
		if _, err = tx.Exec(ctx, `UPDATE bia_go.lead_management SET assigned_to=$2,updated_at=$3 WHERE lead_id=$1`, lead, target, now); err != nil {
			return nil, false, err
		}
	case "note":
		if strings.TrimSpace(a.Text) == "" {
			return nil, false, invalid("note_required")
		}
	case "status":
		if a.Status != "open" && a.Status != "closed" && a.Status != "won" && a.Status != "lost" {
			return nil, false, invalid("invalid_status")
		}
		if (a.Status == "lost" || a.Status == "closed") && strings.TrimSpace(a.Reason) == "" {
			return nil, false, invalid("reason_required")
		}
		if _, err = tx.Exec(ctx, `UPDATE bia_go.lead_management SET status=$2,closed_at=CASE WHEN $2='open' THEN NULL ELSE $3::timestamptz END,first_won_at=CASE WHEN $2='won' THEN COALESCE(first_won_at,$3::timestamptz) ELSE first_won_at END,updated_at=$3 WHERE lead_id=$1`, lead, a.Status, now); err != nil {
			return nil, false, err
		}
		if a.Status != "open" {
			st.Paused = true
			st.Human = true
			st.AutomationSince = now
		}
	case "message":
		if !st.Human || st.Paused || st.OptedOut || !st.Profile.Consent || st.Owner != "go" {
			return nil, false, conflict("human_reply_not_permitted")
		}
		if st.LastInbound.IsZero() || now.Sub(st.LastInbound) >= 23*time.Hour || st.LastInbound.After(now.Add(time.Minute)) {
			return nil, false, conflict("whatsapp_window_closed")
		}
		if strings.TrimSpace(a.Text) == "" || len([]rune(a.Text)) > 1000 {
			return nil, false, invalid("message_length_1_to_1000_required")
		}
		fx := domain.Effect{ID: "fx/" + id, LeadID: lead, Kind: "send", Purpose: "human_reply", OperatorID: u.ID, Text: a.Text, Due: now, Expires: earlier(now.Add(15*time.Minute), st.LastInbound.Add(23*time.Hour))}
		effects = append(effects, fx)
		result["job_id"] = fx.ID
		result["delivery_status"] = "queued"
	case "attendance":
		if a.Outcome != "attended" && a.Outcome != "no_show" {
			return nil, false, invalid("invalid_attendance")
		}
		var bookingRaw []byte
		var cancelled *time.Time
		if err = tx.QueryRow(ctx, `SELECT payload,cancelled_at FROM bia_go.bookings WHERE id=$1 AND lead_id=$2 FOR UPDATE`, a.BookingID, lead).Scan(&bookingRaw, &cancelled); err != nil {
			return nil, false, err
		}
		var booking domain.Booking
		if err = json.Unmarshal(bookingRaw, &booking); err != nil {
			return nil, false, err
		}
		if cancelled != nil {
			return nil, false, conflict("booking_cancelled")
		}
		if now.Before(booking.Start) || (a.Outcome == "no_show" && now.Before(booking.End)) {
			return nil, false, conflict("meeting_not_finished")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO bia_go.meeting_attendance(booking_id,lead_id,starts_at,ends_at,outcome,actor_id,recorded_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(booking_id) DO UPDATE SET outcome=excluded.outcome,actor_id=excluded.actor_id,recorded_at=excluded.recorded_at`, booking.ID, lead, booking.Start, booking.End, a.Outcome, u.ID, now); err != nil {
			return nil, false, err
		}
		if st.Booking != nil && st.Booking.ID == booking.ID {
			r := domain.Reduce(st, domain.Event{ID: id, Kind: "attendance." + a.Outcome, LeadID: lead, BookingID: booking.ID, At: now}, domain.Decision{}, nil, s.Policy, now)
			st = r.State
			effects = append(effects, r.Effects...)
		}
		st.RecordMeetingResult(domain.MeetingResult{BookingID: booking.ID, Start: booking.Start, Outcome: a.Outcome, RecordedAt: now})
	default:
		return nil, false, invalid("unknown_action")
	}
	if changeState {
		if interrupt {
			if _, err = tx.Exec(ctx, `UPDATE bia_go.jobs SET status='skipped',error_code='operator_interrupted',lease_token=NULL,completed_at=$2 WHERE lead_id=$1 AND status IN ('pending','running') AND (kind='event' AND payload->>'kind'='message.received' OR kind='effect' AND payload->>'purpose' IN ('reply','human_reply','welcome','form_contact','form_context'))`, lead, now); err != nil {
				return nil, false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE bia_go.conversation_runs r SET status='skipped',completed_at=$2 FROM bia_go.conversations c WHERE c.lead_id=$1 AND r.id=c.active_run`, lead, now); err != nil {
				return nil, false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE bia_go.conversations SET active_run=NULL,version=version+1,updated_at=$2 WHERE lead_id=$1`, lead, now); err != nil {
				return nil, false, err
			}
		}
		stateRaw, _ = json.Marshal(st)
		if _, err = tx.Exec(ctx, `UPDATE bia_go.leads SET state=$2,revision=revision+1,updated_at=$3,lease_token=NULL,lease_until=NULL WHERE id=$1`, lead, stateRaw, now); err != nil {
			return nil, false, err
		}
		if st.Human || st.Paused {
			if _, err = tx.Exec(ctx, `UPDATE bia_go.voice_requests SET revoked_at=COALESCE(revoked_at,$2),stop_state=CASE WHEN call_sid IS NOT NULL AND stop_state NOT IN ('done','uncertain') THEN 'pending' ELSE stop_state END,state=CASE WHEN state IN ('new','ready','awaiting_permission') THEN 'skipped' ELSE state END,error_code='operator_interrupted',updated_at=$2 WHERE lead_id=$1 AND state IN ('new','ready','requesting','awaiting_permission','dialing','queued','initiated','ringing','in-progress')`, lead, now); err != nil {
				return nil, false, err
			}
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE bia_go.leads SET revision=revision+1,updated_at=$2 WHERE id=$1`, lead, now); err != nil {
			return nil, false, err
		}
	}
	for _, fx := range effects {
		payload, _ := json.Marshal(fx)
		if _, err = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'effect',$3,$4,$5,$6)`, fx.ID, lead, s.Mode, payload, digest(string(payload)), fx.Due); err != nil {
			return nil, false, err
		}
	}
	result["revision"] = revision + 1
	result["human"], result["paused"] = st.Human, st.Paused
	output, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `INSERT INTO bia_go.management_actions(id,actor_id,lead_id,kind,fingerprint,request,result,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, u.ID, lead, a.Kind, fp, raw, output, now); err != nil {
		return nil, false, err
	}
	return result, false, tx.Commit(ctx)
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
