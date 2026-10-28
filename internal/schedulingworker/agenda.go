package schedulingworker

import (
	"arkom/bia/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// One transaction locks a mailbox row and commits the read result or CRM note.
// A crash rolls the whole operation back; competing workers use SKIP LOCKED.
func (w *Worker) answerAgenda(ctx context.Context) (bool, error) {
	tx, e := w.runtime.DB.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id, request, kind, body string
	var expires time.Time
	e = tx.QueryRow(ctx, `SELECT id,voice_request_id,kind,body,expires_at FROM bia_go.agenda_requests WHERE completed_at IS NULL ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &request, &kind, &body, &expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	out := domain.MeetingContext{Status: "unavailable", Now: time.Now().UTC()}
	if !out.Now.Before(expires) {
		out.Status = "expired"
	} else {
		var lead, bid, purpose, mode, state string
		var start *time.Time
		var revoked, permissionExpires *time.Time
		e = tx.QueryRow(ctx, `SELECT lead_id,COALESCE(booking_id,''),booking_start,purpose,mode,state,revoked_at,permission_expires_at FROM bia_go.voice_requests WHERE id=$1`, request).Scan(&lead, &bid, &start, &purpose, &mode, &state, &revoked, &permissionExpires)
		if e != nil {
			return true, e
		}
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE id=$1`, lead).Scan(&raw)
		if e != nil {
			return true, e
		}
		var s domain.State
		if e = json.Unmarshal(raw, &s); e != nil {
			return true, e
		}
		out.Customer = domain.CustomerContext(s, nil, w.runtime.Policy)
		out.Goal = s.Profile.Goal
		if purpose != "confirm" || bid == "" || start == nil || s.OptedOut || s.Human || s.Paused || !s.Profile.Consent || (mode == "live" && s.Owner != "go") || revoked != nil {
			out.Status = "blocked"
		} else if s.Booking == nil || s.Booking.ID != bid || !s.Booking.Start.Equal(*start) {
			out.Status = "booking_changed"
		} else {
			var briefing []byte
			e = tx.QueryRow(ctx, `SELECT b.payload,b.briefing FROM bia_go.bookings b JOIN bia_go.slots s ON s.id=b.slot_id WHERE b.id=$1 AND b.lead_id=$2 AND b.cancelled_at IS NULL AND s.reserved_by=b.id AND s.starts_at=$3 AND s.starts_at>now()`, bid, lead, *start).Scan(&raw, &briefing)
			if errors.Is(e, pgx.ErrNoRows) {
				out.Status = "no_future_booking"
			} else if e != nil {
				return true, e
			} else {
				var b domain.Booking
				if e = json.Unmarshal(raw, &b); e != nil {
					return true, e
				}
				if e = json.Unmarshal(briefing, &out.Briefing); e != nil {
					return true, e
				}
				if b.ID != bid || !b.Start.Equal(*start) {
					out.Status = "booking_changed"
				} else {
					out.Status = "available"
					out.Booking = &b
					out.Customer = domain.CustomerContext(s, &b, w.runtime.Policy)
					if kind == "note" {
						if mode != "live" || permissionExpires == nil || !out.Now.Before(*permissionExpires) || (state != "dialing" && state != "queued" && state != "initiated" && state != "ringing" && state != "in-progress") || len(body) == 0 || len(body) > 2000 {
							out.Status = "blocked"
						} else {
							_, e = tx.Exec(ctx, `INSERT INTO bia_go.crm_notes(operation_id,lead_id,purpose,body) VALUES($1,$2,'voice_reminder',$3) ON CONFLICT DO NOTHING`, "agenda/"+id, lead, body)
							if e != nil {
								return true, e
							}
							out.Status = "recorded"
						}
					}
				}
			}
		}
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return true, e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.agenda_requests SET result=$2,completed_at=now() WHERE id=$1`, id, raw)
	if e != nil {
		return true, e
	}
	return true, tx.Commit(ctx)
}
