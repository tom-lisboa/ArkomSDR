package store

import (
	"arkom/bia/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// The public capability exposes only the contact paired to this submission.
func (d *DB) TestContact(ctx context.Context, submission, phone string) (domain.State, error) {
	var s domain.State
	var raw []byte
	var expires time.Time
	err := d.Pool.QueryRow(ctx, `SELECT l.state,f.expires_at FROM bia_go.test_forms f JOIN bia_go.leads l ON l.id=f.claimed_by WHERE f.id=$1 AND f.phone=$2`, submission, testPhoneKey(phone)).Scan(&raw, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, &FormBookingError{"test_submission_not_found"}
	}
	if err != nil {
		return s, err
	}
	if !expires.After(time.Now()) {
		return s, &FormBookingError{"test_submission_expired"}
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	if testPhoneKey(s.Profile.Phone) != testPhoneKey(phone) {
		return domain.State{}, &FormBookingError{"test_submission_not_found"}
	}
	return s, nil
}

func (d *DB) QueueSMSConsent(ctx context.Context, id, submission, phone string, consent bool, mode string) (string, bool, error) {
	if mode != "live" {
		return "", false, &FormBookingError{"live_mode_required"}
	}
	s, err := d.TestContact(ctx, submission, phone)
	if err != nil {
		return "", false, err
	}
	if s.Owner != "go" || (consent && (s.OptedOut || s.Paused || s.Human || !s.Profile.Consent)) {
		return "", false, &FormBookingError{"contact_not_eligible"}
	}
	if consent && domain.SMSDestination(domain.Form{Phone: s.Profile.Phone, SMSPhone: phone}) == "" {
		return "", false, &FormBookingError{"sms_phone_required"}
	}
	ev := domain.Event{ID: "sms-consent:" + id, LeadID: s.ID, Kind: "contact.sms_consent", At: time.Now().UTC(), SMSConsent: &consent, SMSPhone: phone, Reason: "test-form-sms-v1:" + submission}
	if err = domain.ValidateEvent(ev); err != nil {
		return "", false, err
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "sms-consent:"+s.ID); err != nil {
		return "", false, err
	}
	raw, _ := json.Marshal(ev)
	ev.At = time.Time{}
	normalized, _ := json.Marshal(ev)
	fp := hash(normalized)
	var prior string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.jobs WHERE id=$1`, ev.ID).Scan(&prior)
	if err == nil {
		if prior != fp {
			return "", false, ErrConflict
		}
		return ev.ID, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE lead_id=$1 AND payload->>'kind'='contact.sms_consent' AND created_at>now()-interval '1 hour'`, s.ID).Scan(&count); err != nil {
		return "", false, err
	}
	if count >= 10 {
		return "", false, &FormBookingError{"test_consent_rate_limit"}
	}
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,now()) ON CONFLICT DO NOTHING`, ev.ID, s.ID, mode, raw, fp)
	if err != nil {
		return "", false, err
	}
	// Different contacts can concurrently reuse the same request ID; verify the
	// actual stored fingerprint instead of reporting success for the wrong lead.
	if err = tx.QueryRow(ctx, `SELECT fingerprint FROM bia_go.jobs WHERE id=$1`, ev.ID).Scan(&prior); err != nil {
		return "", false, err
	}
	if prior != fp {
		return "", false, ErrConflict
	}
	return ev.ID, false, tx.Commit(ctx)
}
