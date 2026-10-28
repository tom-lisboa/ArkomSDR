package domain

import (
	"strings"
	"testing"
	"time"
)

func TestSMSReminderUsesOriginalMobileAndBooking(t *testing.T) {
	now := time.Date(2026, 10, 28, 11, 50, 0, 0, time.UTC)
	s := State{ID: "lead", Profile: Form{Phone: "+556599999900", SMSPhone: "+5565999999900", Consent: true, SMSConsent: true, SMSConsentVersion: "test", TimeZone: "America/Cuiaba"}, Booking: &Booking{ID: "b", Start: now.Add(10 * time.Minute)}}
	if SMSDestination(s.Profile) != "+5565999999900" {
		t.Fatal("WhatsApp alias used for SMS")
	}
	fx := SMSSequence(s, now)
	if len(fx) != 1 || !fx[0].Due.Equal(s.Booking.Start.Add(-5*time.Minute)) || !fx[0].Expires.Equal(s.Booking.Start) {
		t.Fatal(fx)
	}
	if SMSSequence(s, now.Add(time.Minute))[0].ID != fx[0].ID {
		t.Fatal("unstable id")
	}
	text := Render(s, fx[0], Policy{TimeZone: "America/Cuiaba", WhatsAppPhone: "+5511936246321"}, now)
	if !strings.Contains(text, "28/10 08:00") || strings.Contains(text, "http") || len(text) > 160 {
		t.Fatal(text, len(text))
	}
	for _, change := range []string{"revoked", "wrong_phone", "missing_original", "booking_changed", "started"} {
		t.Run(change, func(t *testing.T) {
			next := s
			b := *s.Booking
			next.Booking = &b
			at := now
			switch change {
			case "revoked":
				next.Profile.SMSConsent = false
			case "wrong_phone":
				next.Profile.SMSPhone = "+5511999999999"
			case "missing_original":
				next.Profile.SMSPhone = ""
			case "booking_changed":
				next.Booking.Start = next.Booking.Start.Add(time.Hour)
			case "started":
				at = next.Booking.Start
			}
			if ok, _ := Deliverable(next, fx[0], at); ok {
				t.Fatal("invalid SMS allowed")
			}
		})
	}
}
