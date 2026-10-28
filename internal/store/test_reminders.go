package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
)

type TestReminderError struct{ Code string }

func (e *TestReminderError) Error() string { return e.Code }

// QueueTestReminder accepts only a phone bound to an activated test submission.
// The transaction stores an immediate effect, never edits the booking or cadence.
func (d *DB) QueueTestReminder(ctx context.Context, id, submission, phone, action, mode string, audioReady, voiceEnabled, smsEnabled bool) (string, bool, error) {
	jobID := "fx/demo:" + id
	deny := func(code string) (string, bool, error) { return "", false, &TestReminderError{Code: code} }
	if !domain.ValidID(id) || !domain.ValidID(submission) || (action != "audio" && action != "call" && action != "sms") || (mode != "live" && mode != "shadow") {
		return deny("invalid_trigger")
	}
	fp, _ := json.Marshal([]string{submission, testPhoneKey(phone), action, mode})
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	// Serialize idempotency, cooldown and global caps across API instances.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('bia:demo-reminder',0))`); err != nil {
		return "", false, err
	}
	var old string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.jobs WHERE id=$1`, jobID).Scan(&old)
	if err == nil {
		if old != hash(fp) {
			return "", false, ErrConflict
		}
		return jobID, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var lead *string
	var registeredPhone string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT claimed_by,phone,expires_at FROM bia_go.test_forms WHERE id=$1`, submission).Scan(&lead, &registeredPhone, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return deny("test_submission_not_found")
	}
	if err != nil {
		return "", false, err
	}
	now := time.Now().UTC()
	if registeredPhone != testPhoneKey(phone) {
		return deny("submission_phone_mismatch")
	}
	if lead == nil || !expires.After(now) {
		return deny("test_submission_not_active")
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE id=$1 FOR UPDATE`, *lead).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return deny("test_submission_not_active")
	}
	if err != nil {
		return "", false, err
	}
	var s domain.State
	if err = json.Unmarshal(raw, &s); err != nil {
		return "", false, err
	}
	if testPhoneKey(s.Profile.Phone) != registeredPhone {
		return deny("submission_phone_mismatch")
	}
	if s.Owner != "go" || !s.Profile.Consent || s.OptedOut || s.Paused || s.Human {
		return deny("contact_blocked")
	}
	if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) {
		return deny("future_booking_required")
	}
	if action != "sms" && (s.LastInbound.IsZero() || !s.LastInbound.After(now.Add(-23*time.Hour)) || s.LastInbound.After(now.Add(time.Minute))) {
		return deny("whatsapp_reply_required")
	}
	if action == "sms" {
		if !smsEnabled {
			return deny("sms_not_enabled")
		}
		if !s.Profile.SMSConsent || domain.SMSDestination(s.Profile) == "" {
			return deny("sms_consent_required")
		}
	}
	if action == "audio" && !audioReady {
		return deny("reminder_audio_not_ready")
	}
	if action == "call" && !voiceEnabled {
		return deny("voice_not_enabled")
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.voice_requests WHERE lead_id=$1 AND mode=$2 AND state IN ('new','requesting','awaiting_permission','ready','dialing','queued','initiated','ringing','in-progress','uncertain'))`, s.ID, mode).Scan(&active)
	if err != nil {
		return "", false, err
	}
	if active {
		return deny("voice_request_in_progress")
	}
	if action == "call" {
		var daily, weekly int
		err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE created_at>now()-interval '24 hours'),count(*) FROM bia_go.voice_requests WHERE lead_id=$1 AND mode=$2 AND created_at>now()-interval '7 days' AND (permission_sid IS NOT NULL OR state='uncertain')`, s.ID, mode).Scan(&daily, &weekly)
		if err != nil {
			return "", false, err
		}
		if daily >= 1 || weekly >= 2 {
			return deny("permission_request_limit")
		}
	}
	purpose, kind := "meeting_audio", "prepare_audio"
	if action == "call" {
		purpose, kind = "meeting_call", "send"
	}
	if action == "sms" {
		purpose, kind = "meeting_sms", "send_sms"
	}
	var recent, total int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE lead_id=$1 AND payload->>'purpose'=$2 AND (created_at>now()-interval '1 minute' OR status IN ('pending','running','uncertain'))),count(*) FILTER(WHERE created_at>now()-interval '1 hour' AND payload->>'kind' IN ('prepare_audio','send','send_sms')) FROM bia_go.jobs WHERE id LIKE 'fx/demo:%' AND payload->>'manual_reminder'='true'`, s.ID, purpose).Scan(&recent, &total)
	if err != nil {
		return "", false, err
	}
	if recent != 0 || total >= 60 {
		return deny("test_trigger_rate_limit")
	}
	until := now.Add(10 * time.Minute)
	if s.Booking.Start.Before(until) {
		until = s.Booking.Start
	}
	fx := domain.Effect{ID: jobID, LeadID: s.ID, Kind: kind, Purpose: purpose, ManualReminder: true, Due: now, Expires: until, BookingID: s.Booking.ID, BookingStart: s.Booking.Start}
	raw, _ = json.Marshal(fx)
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'effect',$3,$4,$5,$6)`, jobID, s.ID, mode, raw, hash(fp), now)
	if err != nil {
		return "", false, err
	}
	return jobID, false, tx.Commit(ctx)
}
