package quickreply

import (
	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	"strings"
	"testing"
	"time"
)

func fixture() (Signer, domain.State, domain.Policy, time.Time) {
	now := time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)
	s := domain.State{ID: "lead-a", CreatedAt: now.Add(-time.Hour), Profile: domain.Form{Phone: "+5565999999900", Consent: true}, Owner: "go", Stage: "choosing_slot", OfferVersion: "offer-a"}
	s.Offered = []domain.Slot{{ID: "one", Start: now.Add(3 * time.Hour), End: now.Add(3*time.Hour + 45*time.Minute), Provider: "local", CloserID: 1}, {ID: "two", Start: now.Add(4 * time.Hour), End: now.Add(4*time.Hour + 45*time.Minute), Provider: "local", CloserID: 1}}
	return Signer{Key: strings.Repeat("test", 8)}, s, domain.Policy{TimeZone: "America/Cuiaba", QuietStart: 0, QuietEnd: 24, DisableFollowups: true}, now
}
func TestSignedSelectionUsesExactOfferAndDomainChecks(t *testing.T) {
	k, s, p, now := fixture()
	for index, action := range []string{"first", "second"} {
		b := k.Button(s, action, "Escolher", now)
		if len(b.Payload) > 200 {
			t.Fatal("WhatsApp payload limit")
		}
		e := domain.Event{ID: "click", LeadID: s.ID, Kind: "message.received", At: now, Text: "Escolher", ButtonPayload: b.Payload}
		d, handled := k.Resolve(s, &e, p, now)
		if !handled || d.SlotID != s.Offered[index].ID || e.ButtonPayload != "" || e.SelectedSlotID != "" {
			t.Fatal(d, e)
		}
		r := domain.Reduce(s, e, d, s.Offered, p, now)
		found := false
		for _, fx := range r.Effects {
			if fx.Kind == "book" {
				found = fx.Slot != nil && fx.Slot.ID == d.SlotID
			}
		}
		if !found {
			t.Fatalf("verified button did not reach booking: %+v", r)
		}
		// A button never bypasses an unresolved condition negotiated in the chat.
		s.Dialogue = &domain.DialogueState{BookingCondition: "somente se for gratuito"}
		r = domain.Reduce(s, e, d, s.Offered, p, now)
		for _, fx := range r.Effects {
			if fx.Kind == "book" {
				t.Fatal("button bypassed condition")
			}
		}
		s.Dialogue = nil
	}
}
func TestStaleTamperedOtherContactAndMixedClicksCannotConfirm(t *testing.T) {
	k, s, p, now := fixture()
	s.Booking = &domain.Booking{ID: "b1", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}
	original := k.Button(s, "confirm", "Confirmar presença", now).Payload
	cases := []string{"booking changed", "contact changed", "expired", "tampered", "mixed", "reset"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			current := s
			token := original
			at := now
			e := domain.Event{ID: "click", Kind: "message.received", At: now, Text: "Confirmar presença"}
			switch name {
			case "booking changed":
				b := *s.Booking
				b.ID = "b2"
				current.Booking = &b
			case "contact changed":
				current.ID = "lead-b"
			case "expired":
				at = now.Add(24 * time.Hour)
			case "tampered":
				token = strings.Replace(token, "confirm", "reschedule", 1)
			case "mixed":
				e.Context = &conversation.Window{MessageIDs: []string{"click", "text"}}
			case "reset":
				current.CreatedAt = now
			}
			e.ButtonPayload = token
			d, handled := k.Resolve(current, &e, p, at)
			if !handled || d.Action != "reply" || d.Reason != "quick_reply_stale" {
				t.Fatal(d)
			}
			r := domain.Reduce(current, e, d, nil, p, at)
			if r.State.Booking.ConfirmedAt != nil {
				t.Fatal("stale click confirmed current booking")
			}
		})
	}
}
func TestOptionsMatchExactSlotsAndAvoidTechnicalZone(t *testing.T) {
	k, s, p, now := fixture()
	text := "Tenho estes horários: " + domain.SlotLabel(s.Offered[0], p.Zone(s)) + " ou " + domain.SlotLabel(s.Offered[1], p.Zone(s)) + ". Qual prefere?"
	buttons := k.Options(s, text, "reply", p, now)
	if len(buttons) != 3 || buttons[0].Title != "Primeiro horário" || buttons[1].Title != "Segundo horário" {
		t.Fatal(buttons)
	}
	if len(k.Options(s, "Antes dos horários, vou explicar.", "reply", p, now)) != 0 {
		t.Fatal("numbered buttons on wrong bubble")
	}
	for _, b := range buttons {
		if len([]rune(b.Title)) > 20 {
			t.Fatal(b)
		}
	}
}
func TestValidConfirmationAndRescheduleKeepCurrentBooking(t *testing.T) {
	k, s, p, now := fixture()
	s.Booking = &domain.Booking{ID: "b1", Provider: "local", CloserID: 1, Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)}
	for _, action := range []string{"confirm", "reschedule"} {
		e := domain.Event{ID: "click-" + action, Kind: "message.received", At: now, ButtonPayload: k.Button(s, action, "click", now).Payload}
		d, _ := k.Resolve(s, &e, p, now)
		r := domain.Reduce(s, e, d, s.Offered, p, now)
		if r.State.Booking.ID != "b1" {
			t.Fatal("lost current booking")
		}
		if action == "confirm" && r.State.Booking.ConfirmedAt == nil {
			t.Fatal("not confirmed")
		}
		if action == "reschedule" && len(r.State.Offered) != 2 {
			t.Fatal("reschedule did not query offers")
		}
	}
}

func TestOptOutWinsOverAnyButton(t *testing.T) {
	k, s, p, now := fixture()
	e := domain.Event{ID: "click", Kind: "message.received", At: now, Text: "Ver horários\nSAIR", ButtonPayload: "mixed"}
	d, _ := k.Resolve(s, &e, p, now)
	r := domain.Reduce(s, e, d, nil, p, now)
	if !r.State.OptedOut || e.ButtonPayload != "" {
		t.Fatal("button swallowed opt-out", r)
	}
}
