package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
)

func phoneVariants(phone string) []string {
	key := testPhoneKey(phone)
	if key != phone {
		return []string{phone, key}
	}
	if strings.HasPrefix(phone, "+55") && len(phone) == 13 && phone[5] >= '6' && phone[5] <= '9' {
		return []string{phone, phone[:5] + "9" + phone[5:]}
	}
	return []string{phone}
}

// WhatsApp can report Brazilian mobiles with or without the ninth digit. Reuse
// an existing Twilio conversation; never overwrite its ownership or block flags.
func (d *DB) WhatsAppContact(ctx context.Context, phone string) (domain.State, error) {
	var raw []byte
	var s domain.State
	err := d.Pool.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE state->'profile'->>'phone'=ANY($1::text[]) AND (state->'profile'->>'phone'=$2 OR state->'profile'->>'conversation_id' LIKE 'twilio:%') ORDER BY (state->'profile'->>'phone'=$2) DESC LIMIT 1`, phoneVariants(phone), phone).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	return s, err
}

// Activating and enqueueing the form event are one transaction. Repeated POSTs
// (including retries after a lost response) never create another contact job.
func (d *DB) ActivateTestForm(ctx context.Context, id, mode, sender string) (string, error) {
	if mode != "live" && mode != "shadow" {
		return "", errors.New("invalid_mode")
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var eventID, lead *string
	var expires time.Time
	var slotID string
	err = tx.QueryRow(ctx, `SELECT profile,contact_event_id,claimed_by,expires_at,selected_slot_id FROM bia_go.test_forms WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &eventID, &lead, &expires, &slotID)
	if err != nil {
		return "", err
	}
	if eventID != nil {
		return *eventID, tx.Commit(ctx)
	}
	if !expires.After(time.Now()) {
		return "", ErrConflict
	}
	var f domain.Form
	if err = json.Unmarshal(raw, &f); err != nil {
		return "", err
	}
	// Serialize alternate spellings of the same phone across simultaneous forms.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "form-contact:"+testPhoneKey(f.Phone)); err != nil {
		return "", err
	}
	var existing []byte
	err = tx.QueryRow(ctx, `SELECT state FROM bia_go.leads WHERE state->'profile'->>'phone'=ANY($1::text[]) ORDER BY (state->'profile'->>'phone'=$2) DESC LIMIT 1`, phoneVariants(f.Phone), f.Phone).Scan(&existing)
	if err == nil {
		var s domain.State
		if err = json.Unmarshal(existing, &s); err != nil {
			return "", err
		}
		f.Phone = s.Profile.Phone
		f.ConversationID = s.Profile.ConversationID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if f.ConversationID == "" {
		f.ConversationID = "twilio:" + sender + ":" + f.Phone
	}
	leadID := domain.LeadKey(f.Phone)
	now := time.Now().UTC()
	initial, _ := json.Marshal(domain.State{ID: leadID, Owner: "go", Stage: "new", CreatedAt: now, Profile: domain.Form{Phone: f.Phone, ConversationID: f.ConversationID}})
	if _, err = tx.Exec(ctx, `INSERT INTO bia_go.leads(id,state) VALUES($1,$2) ON CONFLICT DO NOTHING`, leadID, initial); err != nil {
		return "", err
	}
	ev := domain.Event{ID: "form-contact-" + id, LeadID: leadID, Kind: "form.contact_requested", At: now, Form: &f}
	if err = domain.ValidateEvent(ev); err != nil {
		return "", err
	}
	raw, _ = json.Marshal(ev)
	fpEvent := ev
	fpEvent.At = time.Time{}
	fp, _ := json.Marshal(fpEvent)
	ct, err := tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,now()) ON CONFLICT DO NOTHING`, ev.ID, leadID, mode, raw, hash(fp))
	if err != nil {
		return "", err
	}
	if ct.RowsAffected() != 1 {
		return "", ErrConflict
	}
	if slotID != "" {
		if mode != "live" {
			return "", &FormBookingError{Code: "live_calendar_required"}
		}
		if _, _, err = reserveFormSlot(ctx, tx, "form-booking:"+id, leadID, slotID, f, mode); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE bia_go.test_forms SET contact_event_id=$2,claimed_by=$3,claimed_at=now() WHERE id=$1`, id, ev.ID, leadID); err != nil {
		return "", err
	}
	return ev.ID, tx.Commit(ctx)
}
