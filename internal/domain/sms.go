package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// WhatsApp sometimes removes the Brazilian ninth digit. SMS must use the
// original mobile supplied with consent, never the altered WhatsApp identifier.
func SMSDestination(f Form) string {
	phone, err := NormalizePhone(f.SMSPhone)
	if err != nil || phone != f.SMSPhone {
		return ""
	}
	key := func(p string) string {
		if strings.HasPrefix(p, "+55") && len(p) == 14 && p[5] == '9' && p[6] >= '6' && p[6] <= '9' {
			return p[:5] + p[6:]
		}
		return p
	}
	if key(phone) != key(f.Phone) {
		return ""
	}
	if strings.HasPrefix(phone, "+55") && (len(phone) != 14 || phone[5] != '9') {
		return ""
	}
	return phone
}

func SMSSequence(s State, now time.Time) []Effect {
	if !s.Profile.SMSConsent || SMSDestination(s.Profile) == "" || s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) {
		return nil
	}
	b := s.Booking
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+b.ID+"\x00"+b.Start.UTC().Format(time.RFC3339Nano))))
	return []Effect{{ID: "fx/meeting:" + key + ":meeting_sms", LeadID: s.ID, Kind: "send_sms", Purpose: "meeting_sms", Due: b.Start.Add(-5 * time.Minute), Expires: b.Start, BookingID: b.ID, BookingStart: b.Start}}
}

func SMSReminderText(s State, p Policy) string {
	if s.Booking == nil {
		return ""
	}
	start := s.Booking.Start.In(p.Zone(s))
	// Fixed transactional copy: no model, links, revenue or other form answers.
	return fmt.Sprintf("Arkom: sua reuniao e %s. Confirme presenca pelo WhatsApp %s. Para parar avisos, envie SAIR por la.", start.Format("02/01 15:04"), p.WhatsAppPhone)
}
