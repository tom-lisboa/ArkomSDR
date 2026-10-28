package voiceworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"time"
)

type Worker struct {
	Agenda              Agenda
	Repo                Repository
	Provider            Provider
	Policy              domain.Policy
	Live                bool
	AllowOutsideSession bool
	Now                 func() time.Time
}

func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, e := w.Once(ctx); e != nil {
				slog.Error("voice worker step failed")
			}
		}
	}
}
func (w *Worker) Once(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	var token [16]byte
	if _, e := rand.Read(token[:]); e != nil {
		return false, e
	}
	r, e := w.Repo.Claim(ctx, hex.EncodeToString(token[:]))
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	finish := func(state, id, code string) (bool, error) { return true, w.Repo.Finish(ctx, r, state, id, code) }
	st, e := w.Repo.DB.Lead(ctx, r.LeadID)
	if e != nil {
		return true, e
	}
	if st.OptedOut || st.Human || st.Paused || !st.Profile.Consent || (r.Mode == "live" && st.Owner != "go") {
		return finish("skipped", "", "contact_blocked")
	}
	pending, e := w.Repo.DB.PendingEvent(ctx, st.ID)
	if e != nil {
		return true, e
	}
	newInput, e := w.Repo.DB.HasNewInput(ctx, st.ID, "", 0)
	if e != nil {
		return true, e
	}
	if pending || newInput {
		return true, w.Repo.Defer(ctx, r, now.Add(5*time.Second))
	}
	due := w.Policy.NextContact(now, st)
	if !due.Before(r.Expires) {
		return finish("expired", "", "contact_window_expired")
	}
	if due.After(now) {
		return true, w.Repo.Defer(ctx, r, due)
	}
	if r.Purpose != "confirm" || st.Booking == nil || !st.Booking.Start.After(now) || r.BookingStart == nil || r.BookingID != st.Booking.ID || !r.BookingStart.Equal(st.Booking.Start) {
		return finish("skipped", "", "no_matching_future_booking")
	}
	if w.Agenda == nil {
		return finish("skipped", "", "agenda_not_configured")
	}
	meeting, err := w.Agenda.Lookup(ctx, r.ID)
	if err != nil {
		return true, w.Repo.Defer(ctx, r, now.Add(30*time.Second))
	}
	if meeting.Status != "available" {
		return finish("skipped", "", meeting.Status)
	}
	if r.Mode == "shadow" {
		return finish("simulated", "", "permission_request_only")
	}
	if !w.Live || w.Provider == nil {
		return finish("skipped", "", "voice_not_enabled")
	}
	if r.State == "requesting" {
		if !w.AllowOutsideSession && (st.LastInbound.IsZero() || now.Sub(st.LastInbound) > 24*time.Hour) {
			return finish("skipped", "", "approved_template_required_outside_session")
		}
		daily, weekly, e := w.Repo.Counts(ctx, r)
		if e != nil {
			return true, e
		}
		if daily >= 1 || weekly >= 2 {
			return finish("skipped", "", "permission_request_limit")
		}
		id, e := w.Provider.Permission(ctx, st.Profile.Phone, r.Sender)
		if e != nil {
			return w.failed(ctx, r, e)
		}
		return finish("awaiting_permission", id, "")
	}
	if r.PermissionAt == nil || r.PermissionExpires == nil || !now.Before(*r.PermissionExpires) || r.Revoked != nil {
		return finish("skipped", "", "call_permission_missing_or_expired")
	}
	id, e := w.Provider.Dial(ctx, r, st.Profile.Phone)
	if e != nil {
		return w.failed(ctx, r, e)
	}
	// Webhooks may arrive before the POST response. Do not regress callback state.
	ct, e := w.Repo.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET call_sid=$3,state=CASE WHEN state='dialing' THEN 'queued' ELSE state END,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND (call_sid IS NULL OR call_sid=$3)`, r.ID, r.Token, id)
	if e != nil {
		return true, e
	}
	if ct.RowsAffected() != 1 {
		return true, ErrBlocked
	}
	return true, nil
}
func (w *Worker) failed(ctx context.Context, r Request, e error) (bool, error) {
	f := providers.Classify(e)
	state := "failed"
	if f.Ambiguous {
		state = "uncertain"
	}
	return true, w.Repo.Finish(ctx, r, state, "", f.Code)
}
