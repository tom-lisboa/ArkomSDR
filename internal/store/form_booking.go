package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"github.com/jackc/pgx/v5"
)

type FormBookingError struct{ Code string }

func (e *FormBookingError) Error() string { return e.Code }

// ReserveTestForm binds the request to an activated, unexpired submission and
// uses the same local calendar transaction/locks as WhatsApp scheduling.
func (d *DB) ReserveTestForm(ctx context.Context, id, submission, phone, slotID, mode string) (domain.Booking, bool, error) {
	var empty domain.Booking
	if mode != "live" {
		return empty, false, &FormBookingError{"live_calendar_required"}
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var storedPhone string
	var lead, activated *string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT profile,phone,claimed_by,contact_event_id,expires_at FROM bia_go.test_forms WHERE id=$1 FOR UPDATE`, submission).Scan(&raw, &storedPhone, &lead, &activated, &expires)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && storedPhone != testPhoneKey(phone)) {
		return empty, false, &FormBookingError{"test_submission_not_found"}
	}
	if err != nil {
		return empty, false, err
	}
	if !expires.After(time.Now()) {
		return empty, false, &FormBookingError{"test_submission_expired"}
	}
	if lead == nil || activated == nil {
		return empty, false, &FormBookingError{"form_activation_required"}
	}
	var f domain.Form
	if err = json.Unmarshal(raw, &f); err != nil {
		return empty, false, err
	}
	// Include the submission in the idempotency identity; an ID cannot be reused
	// for another contact or slot, including concurrent HTTP requests.
	key := "site-booking:" + id
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return empty, false, err
	}
	fp := hash([]byte(submission + "\n" + testPhoneKey(phone) + "\n" + slotID))
	var prior string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.jobs WHERE id=$1`, "booking-event/"+key).Scan(&prior)
	if err == nil && prior != fp {
		return empty, false, ErrConflict
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, false, err
	}
	b, duplicate, err := reserveFormSlot(ctx, tx, key, *lead, slotID, f, mode)
	if err != nil {
		return empty, false, err
	}
	if !duplicate {
		if _, err = tx.Exec(ctx, `UPDATE bia_go.jobs SET fingerprint=$2 WHERE id=$1`, "booking-event/"+key, fp); err != nil {
			return empty, false, err
		}
	}
	return b, duplicate, tx.Commit(ctx)
}

func reserveFormSlot(ctx context.Context, tx pgx.Tx, key, lead, slotID string, profile domain.Form, mode string) (domain.Booking, bool, error) {
	var empty domain.Booking
	var raw []byte
	var lease *time.Time
	if err := tx.QueryRow(ctx, `SELECT state,lease_until FROM bia_go.leads WHERE id=$1 FOR UPDATE`, lead).Scan(&raw, &lease); err != nil {
		return empty, false, err
	}
	// Safe retry even if a worker has since leased this contact.
	var existing []byte
	var previousLead, previousSlot string
	var cancelled *time.Time
	err := tx.QueryRow(ctx, `SELECT lead_id,slot_id,payload,cancelled_at FROM bia_go.bookings WHERE operation_id=$1`, key).Scan(&previousLead, &previousSlot, &existing, &cancelled)
	if err == nil {
		if previousLead != lead || previousSlot != slotID {
			return empty, false, ErrConflict
		}
		if cancelled != nil {
			return empty, false, &FormBookingError{"booking_no_longer_active"}
		}
		var b domain.Booking
		err = json.Unmarshal(existing, &b)
		return b, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, false, err
	}
	if lease != nil && lease.After(time.Now()) {
		return empty, false, &FormBookingError{"contact_busy"}
	}
	var state domain.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return empty, false, err
	}
	if state.Owner != "go" || state.OptedOut || state.Paused || state.Human || !profile.Consent || testPhoneKey(state.Profile.Phone) != testPhoneKey(profile.Phone) {
		return empty, false, &FormBookingError{"contact_not_eligible"}
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE lead_id=$1 AND status IN ('pending','running') AND kind='event' AND payload->>'kind'='message.received')`, lead).Scan(&pending); err != nil {
		return empty, false, err
	}
	if pending {
		return empty, false, &FormBookingError{"contact_busy"}
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE lead_id=$1 AND status IN ('pending','running','uncertain') AND kind='effect' AND payload->>'kind' IN ('book','cancel'))`, lead).Scan(&pending); err != nil {
		return empty, false, err
	}
	if pending {
		return empty, false, &FormBookingError{"booking_in_progress"}
	}
	var slot domain.Slot
	slot.ID, slot.Provider = slotID, "local"
	if err = tx.QueryRow(ctx, `SELECT starts_at,ends_at,closer_id FROM bia_go.slots WHERE id=$1`, slotID).Scan(&slot.Start, &slot.End, &slot.CloserID); errors.Is(err, pgx.ErrNoRows) {
		return empty, false, &FormBookingError{"slot_not_found"}
	} else if err != nil {
		return empty, false, err
	}
	b, err := providers.BookLocalTx(ctx, tx, key, state, slot, nil)
	if err != nil {
		return empty, false, err
	}
	// Booking event is committed with the reservation. All queues must process
	// due events before responses/effects, so the next AI run sees this booking.
	ev := domain.Event{ID: "booking-event/" + key, LeadID: lead, Kind: "booking.created", At: time.Now().UTC(), Booking: &b}
	payload, _ := json.Marshal(ev)
	if _, err = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,now())`, ev.ID, lead, mode, payload, hash(payload)); err != nil {
		return empty, false, err
	}
	// Invalidate undelivered text generated before the web reservation. Already
	// running work is excluded by the lead lease check above.
	if _, err = tx.Exec(ctx, `UPDATE bia_go.jobs SET status='skipped',error_code='web_booking_created',completed_at=now() WHERE lead_id=$1 AND kind='effect' AND status='pending' AND payload->>'kind'='send' AND payload->>'purpose' IN ('reply','offer','availability','followup')`, lead); err != nil {
		return empty, false, err
	}
	return b, false, nil
}
