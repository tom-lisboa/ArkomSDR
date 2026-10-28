package voiceworker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrBlocked = errors.New("voice_request_blocked")

type Request struct {
	ScheduledReminder bool       `json:"scheduled_reminder"`
	BookingID         string     `json:"booking_id,omitempty"`
	BookingStart      *time.Time `json:"booking_start,omitempty"`
	ID                string     `json:"id"`
	LeadID            string     `json:"lead_id"`
	Mode              string     `json:"mode"`
	Sender            string     `json:"sender"`
	Purpose           string     `json:"purpose"`
	State             string     `json:"state"`
	Created           time.Time  `json:"created_at"`
	Expires           time.Time  `json:"expires_at"`
	PermissionAt      *time.Time `json:"permission_at,omitempty"`
	PermissionExpires *time.Time `json:"permission_expires_at,omitempty"`
	Permanent         bool       `json:"permanent"`
	Revoked           *time.Time `json:"revoked_at,omitempty"`
	PermissionSID     string     `json:"permission_sid,omitempty"`
	CallSID           string     `json:"call_sid,omitempty"`
	Token             string     `json:"-"`
	Error             string     `json:"error_code,omitempty"`
}

const requestColumns = `id,lead_id,mode,sender,purpose,state,created_at,expires_at,permission_at,permission_expires_at,permanent,revoked_at,COALESCE(permission_sid,''),COALESCE(call_sid,''),COALESCE(lease_token,''),COALESCE(error_code,''),COALESCE(booking_id,''),booking_start,scheduled_reminder`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	e := row.Scan(&r.ID, &r.LeadID, &r.Mode, &r.Sender, &r.Purpose, &r.State, &r.Created, &r.Expires, &r.PermissionAt, &r.PermissionExpires, &r.Permanent, &r.Revoked, &r.PermissionSID, &r.CallSID, &r.Token, &r.Error, &r.BookingID, &r.BookingStart, &r.ScheduledReminder)
	return r, e
}

type Repository struct{ DB *store.DB }

func (s Repository) Get(ctx context.Context, id string) (Request, error) {
	return scanRequest(s.DB.Pool.QueryRow(ctx, `SELECT `+requestColumns+` FROM bia_go.voice_requests WHERE id=$1`, id))
}
func (s Repository) Create(ctx context.Context, id, lead, mode, sender, purpose string) (bool, error) {
	return s.create(ctx, id, lead, mode, sender, purpose, nil)
}
func (s Repository) CreateScheduled(ctx context.Context, id, lead, mode, sender string, fx domain.Effect) (bool, error) {
	if !domain.IsMeetingReminder(fx) || fx.Kind != "request_call" {
		return false, ErrBlocked
	}
	return s.create(ctx, id, lead, mode, sender, "confirm", &fx)
}
func (s Repository) create(ctx context.Context, id, lead, mode, sender, purpose string, expected *domain.Effect) (bool, error) {
	if !domain.ValidID(id) || !domain.ValidID(lead) || (mode != "shadow" && mode != "live") || purpose != "confirm" {
		return false, ErrBlocked
	}
	tx, e := s.DB.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE id=$1 FOR UPDATE`, lead).Scan(&raw)
	if e != nil {
		return false, e
	}
	var st domain.State
	if e = json.Unmarshal(raw, &st); e != nil {
		return false, e
	}
	if st.Booking == nil || !st.Booking.Start.After(time.Now()) || st.Profile.Phone == "" || !st.Profile.Consent || st.OptedOut || st.Human || st.Paused {
		return false, ErrBlocked
	}
	expires := time.Now().Add(24 * time.Hour)
	if expected != nil {
		if ok, _ := domain.Deliverable(st, *expected, time.Now()); !ok || expected.BookingID == "" || expected.BookingStart.IsZero() {
			return false, ErrBlocked
		}
		expires = st.Booking.Start
	}
	ct, e := tx.Exec(ctx, `INSERT INTO bia_go.voice_requests(id,lead_id,mode,sender,purpose,booking_id,booking_start,expires_at,scheduled_reminder) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING`, id, lead, mode, sender, purpose, st.Booking.ID, st.Booking.Start, expires, expected != nil)
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			return false, ErrBlocked
		}
		return false, e
	}
	if ct.RowsAffected() == 0 {
		old, e := scanRequest(tx.QueryRow(ctx, `SELECT `+requestColumns+` FROM bia_go.voice_requests WHERE id=$1`, id))
		if e != nil {
			return false, e
		}
		if old.LeadID != lead || old.Mode != mode || old.Sender != sender || old.Purpose != purpose || old.BookingID != st.Booking.ID || old.BookingStart == nil || !old.BookingStart.Equal(st.Booking.Start) || old.ScheduledReminder != (expected != nil) {
			return false, store.ErrConflict
		}
	}
	return ct.RowsAffected() == 1, tx.Commit(ctx)
}
func (s Repository) Claim(ctx context.Context, token string) (Request, error) {
	// A crash during a provider mutation is never retried automatically.
	_, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET state='uncertain',error_code='voice_worker_lost',lease_token=NULL,lease_until=NULL WHERE state IN ('requesting','dialing') AND lease_until<now()`)
	if e != nil {
		return Request{}, e
	}
	_, e = s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET state='expired' WHERE state IN ('new','ready','awaiting_permission') AND expires_at<=now()`)
	if e != nil {
		return Request{}, e
	}
	return scanRequest(s.DB.Pool.QueryRow(ctx, `UPDATE bia_go.voice_requests SET state=CASE WHEN state='new' THEN 'requesting' ELSE 'dialing' END,lease_token=$1,lease_until=now()+interval '2 minutes',updated_at=now()
 WHERE id=(SELECT id FROM bia_go.voice_requests WHERE state IN ('new','ready') AND due_at<=now() ORDER BY due_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING `+requestColumns, token))
}
func (s Repository) Finish(ctx context.Context, r Request, state, providerID, code string) error {
	ct, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET state=$3,permission_sid=CASE WHEN $4<>'' AND state='requesting' THEN $4 ELSE permission_sid END,call_sid=CASE WHEN $4<>'' AND state='dialing' THEN $4 ELSE call_sid END,error_code=NULLIF($5,''),lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, r.ID, r.Token, state, providerID, code)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return store.ErrLease
	}
	return nil
}
func (s Repository) Defer(ctx context.Context, r Request, due time.Time) error {
	ct, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET state=CASE WHEN state='requesting' THEN 'new' ELSE 'ready' END,due_at=$3,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, r.ID, r.Token, due)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return store.ErrLease
	}
	return nil
}

// Permission timestamps are fetched from Twilio's Message resource, not supplied
// by the sender or inferred from arrival order. Each provider event is consumed once.
func (s Repository) Permission(ctx context.Context, eventID, phone, sender string, accepted, permanent bool, at time.Time) error {
	lead := domain.LeadKey(phone)
	contact, lookupErr := s.DB.WhatsAppContact(ctx, phone)
	if lookupErr == nil {
		lead = contact.ID
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}
	tx, e := s.DB.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	ct, e := tx.Exec(ctx, `INSERT INTO bia_go.voice_webhooks(id,occurred_at) VALUES($1,$2) ON CONFLICT DO NOTHING`, eventID, at)
	if e != nil {
		return e
	}
	if ct.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if !accepted {
		_, e = tx.Exec(ctx, `UPDATE bia_go.voice_requests SET revoked_at=$3,updated_at=now(),state=CASE WHEN state IN ('awaiting_permission','ready','new') THEN 'rejected' ELSE state END WHERE lead_id=$1 AND sender=$2 AND mode='live' AND (permission_at IS NULL OR permission_at<=$3)`, lead, sender, at)
	} else {
		// Each invitation authorizes at most one automatic call. Permanent platform
		// permission does not start another campaign or bypass a new invitation.
		_, e = tx.Exec(ctx, `UPDATE bia_go.voice_requests SET state='ready',permission_at=$3,permission_expires_at=$3+interval '7 days',permanent=$4,due_at=now(),updated_at=now() WHERE lead_id=$1 AND sender=$2 AND mode='live' AND state='awaiting_permission' AND date_trunc('second',created_at)<=$3 AND expires_at>now() AND (revoked_at IS NULL OR revoked_at<$3)`, lead, sender, at, permanent)
	}
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Repository) Status(ctx context.Context, id, sid, status string, seq int64) error {
	switch status {
	case "initiated", "ringing", "in-progress", "completed", "busy", "failed", "no-answer", "canceled":
	default:
		return ErrBlocked
	}
	ct, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET state=$3,callback_seq=$4,call_sid=$2,updated_at=now() WHERE id=$1 AND mode='live' AND (call_sid=$2 OR (call_sid IS NULL AND state='dialing')) AND callback_seq<$4 AND state IN ('dialing','queued','initiated','ringing','in-progress')`, id, sid, status, seq)
	if e != nil {
		return e
	}
	_ = ct
	return nil
}
func (s Repository) Active(ctx context.Context, r Request) (domain.State, error) {
	fresh, e := s.Get(ctx, r.ID)
	if e != nil {
		return domain.State{}, e
	}
	if fresh.LeadID != r.LeadID || fresh.Purpose != "confirm" || fresh.Mode != "live" || fresh.Revoked != nil || fresh.PermissionAt == nil || fresh.PermissionExpires == nil || !time.Now().Before(*fresh.PermissionExpires) {
		return domain.State{}, ErrBlocked
	}
	switch fresh.State {
	case "dialing", "queued", "initiated", "ringing", "in-progress":
	default:
		return domain.State{}, ErrBlocked
	}
	st, e := s.DB.Lead(ctx, r.LeadID)
	if e != nil {
		return st, e
	}
	if st.Booking == nil || fresh.BookingStart == nil || st.Booking.ID != fresh.BookingID || !st.Booking.Start.Equal(*fresh.BookingStart) || !st.Booking.Start.After(time.Now()) || !st.Profile.Consent || st.Owner != "go" || st.OptedOut || st.Human || st.Paused {
		return st, ErrBlocked
	}
	return st, nil
}
func (s Repository) BeginStream(ctx context.Context, r Request, sid string) error {
	ct, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET stream_started=true,call_sid=$2 WHERE id=$1 AND mode='live' AND NOT stream_started AND revoked_at IS NULL AND permission_expires_at>now() AND state IN ('dialing','queued','initiated','ringing','in-progress') AND (call_sid=$2 OR call_sid IS NULL)`, r.ID, sid)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrBlocked
	}
	return nil
}
func (s Repository) Transcript(ctx context.Context, r Request, item, role, text string) error {
	if (role != "user" && role != "assistant") || len(text) == 0 || len(text) > 16000 || len(item) > 200 {
		return ErrBlocked
	}
	tx, e := s.DB.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	ct, e := tx.Exec(ctx, `INSERT INTO bia_go.voice_transcripts(request_id,item_id,role,body) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, r.ID, item, role, text)
	if e != nil {
		return e
	}
	if ct.RowsAffected() > 0 {
		// Context remains the only writer of canonical history. Audio receipts are
		// published separately from ordinary messages to avoid triggering text runs.
		payload, _ := json.Marshal(map[string]any{"id": "voice/" + r.ID + "/" + item + "/" + role, "text": text, "role": role, "at": time.Now(), "request_id": r.ID})
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.worker_tasks(id,queue,kind,lead_id,mode,payload,fingerprint) VALUES($1,'context','voice.transcript',$2,$3,$4,$1) ON CONFLICT DO NOTHING`, "voice/"+r.ID+"/"+item+"/"+role, r.LeadID, r.Mode, payload)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s Repository) SetSession(ctx context.Context, id, session string) error {
	_, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET realtime_session_id=$2 WHERE id=$1`, id, session)
	return e
}
func (s Repository) MarkError(ctx context.Context, id, code string) error {
	_, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET error_code=$2,updated_at=now() WHERE id=$1`, id, code)
	return e
}
func (s Repository) Counts(ctx context.Context, r Request) (int, int, error) {
	var daily, weekly int
	e := s.DB.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE created_at>now()-interval '24 hours'),count(*) FROM bia_go.voice_requests WHERE lead_id=$1 AND sender=$2 AND mode=$3 AND id<>$4 AND created_at>now()-interval '7 days' AND (permission_sid IS NOT NULL OR state='uncertain')`, r.LeadID, r.Sender, r.Mode, r.ID).Scan(&daily, &weekly)
	return daily, weekly, e
}
