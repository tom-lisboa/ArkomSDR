package providers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"arkom/bia/internal/domain"
)

// SMS is a distinct channel: no WhatsApp addressing, templates or session checks.
// The write-before-POST ledger prevents a timeout/restart from sending twice.
type TwilioSMS struct {
	Transport *TwilioMessaging
	From      string
}

func (s *TwilioSMS) SendSMS(ctx context.Context, key string, lead domain.State, text string, now, expires time.Time) (Receipt, error) {
	phone := domain.SMSDestination(lead.Profile)
	if !lead.Profile.SMSConsent || phone == "" {
		return Receipt{}, &Failure{Code: "sms_consent_required"}
	}
	if lead.OptedOut || lead.Paused || lead.Human || !lead.Profile.Consent {
		return Receipt{}, &Failure{Code: "sms_contact_blocked"}
	}
	t := s.Transport
	if t == nil || t.Pool == nil || s.From == "" {
		return Receipt{}, &Failure{Code: "sms_not_enabled"}
	}
	if t.AllowedPhone != "" && t.AllowedPhone != lead.Profile.Phone {
		return Receipt{}, &Failure{Code: "sms_recipient_not_allowed"}
	}
	if text == "" || utf8.RuneCountInString(text) > 320 {
		return Receipt{}, &Failure{Code: "sms_invalid_text"}
	}
	remaining := int(expires.Sub(now).Seconds())
	if remaining < 1 {
		return Receipt{}, &Failure{Code: "sms_expired"}
	}
	if remaining > 300 {
		remaining = 300
	}
	fp := fmt.Sprintf("%x", sha256.Sum256([]byte(s.From+"\x00"+phone+"\x00"+text)))
	ct, err := t.Pool.Exec(ctx, `INSERT INTO bia_go.sms_messages(id,phone,fingerprint) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, key, phone, fp)
	if err != nil {
		return Receipt{}, err
	}
	var m TwilioMessage
	if ct.RowsAffected() == 1 {
		m, err = t.request(ctx, "POST", "/Messages.json", url.Values{"From": {s.From}, "To": {phone}, "Body": {text}, "ValidityPeriod": {strconv.Itoa(remaining)}})
		if err != nil {
			_, _ = t.Pool.Exec(ctx, `UPDATE bia_go.sms_messages SET error_code=$2,updated_at=now() WHERE id=$1`, key, Classify(err).Code)
			return Receipt{}, err
		}
		if !ValidMessageSID(m.SID) {
			return Receipt{}, &Failure{Code: "sms_missing_sid", Ambiguous: true}
		}
		if _, err = t.Pool.Exec(ctx, `UPDATE bia_go.sms_messages SET sid=$2,status=$3,updated_at=now() WHERE id=$1`, key, m.SID, m.Status); err != nil {
			return Receipt{}, &Failure{Code: "sms_receipt_storage", Ambiguous: true}
		}
	} else {
		var old string
		if err = t.Pool.QueryRow(ctx, `SELECT COALESCE(sid,''),fingerprint FROM bia_go.sms_messages WHERE id=$1`, key).Scan(&m.SID, &old); err != nil {
			return Receipt{}, err
		}
		if old != fp {
			return Receipt{}, &Failure{Code: "sms_idempotency_conflict", Ambiguous: true}
		}
		if m.SID == "" {
			return Receipt{}, &Failure{Code: "sms_interrupted_send", Ambiguous: true}
		}
	}
	// Poll only the existing SID; never repeat the POST. 'sent' is provider
	// handoff, not proof that the person has read the SMS.
	poll, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	interval := t.PollInterval
	if interval == 0 {
		interval = time.Second
	}
	for {
		switch m.Status {
		case "sent", "delivered":
			return Receipt{ID: m.SID, Status: m.Status}, nil
		case "failed", "undelivered", "canceled":
			code := "sms_delivery_failed"
			if m.ErrorCode != nil {
				code = "twilio_" + strconv.Itoa(*m.ErrorCode)
			}
			return Receipt{}, &Failure{Code: code}
		}
		select {
		case <-poll.Done():
			return Receipt{}, &Failure{Code: "sms_delivery_unconfirmed", Ambiguous: true}
		case <-time.After(interval):
		}
		next, e := t.Fetch(poll, m.SID)
		if e != nil {
			return Receipt{}, &Failure{Code: "sms_receipt_unavailable", Ambiguous: true}
		}
		if next.SID != m.SID {
			return Receipt{}, &Failure{Code: "sms_receipt_mismatch", Ambiguous: true}
		}
		m = next
		if _, e = t.Pool.Exec(poll, `UPDATE bia_go.sms_messages SET status=$2,updated_at=now() WHERE id=$1`, key, m.Status); e != nil {
			return Receipt{}, &Failure{Code: "sms_receipt_storage", Ambiguous: true}
		}
	}
}
