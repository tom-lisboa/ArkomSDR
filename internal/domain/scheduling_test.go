package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNaturalSelectionRequiresUniqueOfferedTime(t *testing.T) {
	s, p, now := fixture()
	first := Slot{ID: "private-slot-one", Provider: "local", CloserID: 1, Start: now.Add(2 * time.Hour), End: now.Add(165 * time.Minute)}
	second := first
	second.ID = "private-slot-two"
	second.Start = first.Start.Add(45 * time.Minute)
	second.End = first.End.Add(45 * time.Minute)
	s.Offered = []Slot{first, second}
	for _, tc := range []struct {
		text, id string
		book     bool
	}{
		{"o primeiro", first.ID, true}, {"pode ser a segunda opção", second.ID, true}, {"às 15h", first.ID, true},
		{"15:45", second.ID, true}, {"terça às 15h", first.ID, true}, {"27/10 às 15h", first.ID, true},
		{"sim", first.ID, false}, {"terça", first.ID, false}, {"27/10", first.ID, false},
		{"não quero o primeiro", first.ID, false}, {"talvez às 15h", first.ID, false}, {"o primeiro ou o segundo", first.ID, false},
		{"28/10 às 15h", first.ID, false}, {"o primeiro", second.ID, false},
	} {
		t.Run(tc.text+tc.id, func(t *testing.T) {
			r := Reduce(s, Event{ID: "choice", Kind: "message.received", At: now, Text: tc.text}, Decision{Action: "book", SlotID: tc.id, Evidence: tc.text}, s.Offered, p, now)
			if got := count(r, "book", "") > 0; got != tc.book {
				t.Fatalf("book=%v want=%v: %+v", got, tc.book, r.Effects)
			}
			if tc.book && count(r, "send", "") != 0 {
				t.Fatal("confirmed before calendar reserved")
			}
		})
	}
	second.Start = second.Start.AddDate(0, 0, 1)
	second.End = second.End.AddDate(0, 0, 1)
	s.Offered = []Slot{first, second}
	if id, ok := selectedOffer("quarta", s.Offered, p.Zone(s), now); !ok || id != second.ID {
		t.Fatal("unique weekday not resolved", id, ok)
	}
	r := Reduce(s, Event{ID: "literal", Kind: "message.received", At: now, Text: "Às 15h"}, Decision{Action: "book", SlotID: first.ID, Evidence: `current_message="Às 15h"`}, s.Offered, p, now)
	if count(r, "book", "") != 1 || r.Decision.Evidence != "Às 15h" {
		t.Fatal("literal human choice lost to model evidence formatting", r)
	}
}

func TestOffersUseValidatedPreferenceAndDoNotExposeIDs(t *testing.T) {
	s, p, now := fixture()
	slots := []Slot{}
	for i := 0; i < 4; i++ {
		slots = append(slots, Slot{ID: []string{"private-a", "private-b", "private-c", "private-d"}[i], Provider: "local", CloserID: 1, Start: now.Add(time.Duration(i+2) * time.Hour)})
	}
	r := Reduce(s, Event{ID: "offer", Kind: "message.received", Text: "mais tarde", At: now}, Decision{Action: "offer_slots", OfferSlotIDs: []string{"private-c", "private-d"}, Message: "Podemos avaliar as faltas na reunião."}, slots, p, now)
	if len(r.State.Offered) != 2 || r.State.Offered[0].ID != "private-c" || r.State.Offered[1].ID != "private-d" {
		t.Fatal(r.State.Offered)
	}
	if strings.Contains(r.Effects[0].Text, "private-") || !strings.Contains(r.Effects[0].Text, "terça-feira") || !strings.Contains(r.Effects[0].Text, "avaliar as faltas") {
		t.Fatal(r.Effects[0].Text)
	}
	r = Reduce(s, Event{ID: "bad-offer", Kind: "message.received", At: now}, Decision{Action: "offer_slots", OfferSlotIDs: []string{"invented", "private-d"}}, slots, p, now)
	if r.State.Offered[0].ID != "private-a" {
		t.Fatal("unverified preference accepted")
	}
}

func TestNoAvailabilityIsExplicitAndDoesNotRestartQualification(t *testing.T) {
	s, p, now := fixture()
	p.SchedulingURL = ""
	for _, unavailable := range []bool{false, true} {
		r := Reduce(s, Event{ID: "empty", Kind: "message.received", At: now, Text: "quero agendar", CalendarUnavailable: unavailable}, Decision{Action: "offer_slots"}, nil, p, now)
		if r.State.Human || r.State.Stage != "awaiting_availability" || count(r, "crm", "availability_requested") != 1 {
			t.Fatal(r.State)
		}
		for _, fx := range r.Effects {
			if fx.Kind == "send" && (strings.Contains(fx.Text, "?") || strings.Contains(fx.Text, "aqui:") || !strings.Contains(fx.Text, "agendamento ainda não foi feito")) {
				t.Fatal(fx.Text)
			}
		}
	}
}

func TestLocalBookingCanConfirmWithoutMeetingLink(t *testing.T) {
	s, p, now := fixture()
	b := Booking{ID: "local-booking", Provider: "local", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
	e := Event{ID: "booked", LeadID: s.ID, Kind: "booking.created", At: now, Booking: &b}
	if err := ValidateEvent(e); err != nil {
		t.Fatal(err)
	}
	r := Reduce(s, e, Decision{}, nil, p, now)
	if r.State.Booking == nil || count(r, "send", "booking_confirmation") != 1 {
		t.Fatal(r)
	}
	if strings.Contains(r.Effects[0].Text, "Link") || strings.Contains(r.Effects[0].Text, "http") {
		t.Fatal(r.Effects[0].Text)
	}
	b.Provider = "external"
	if ValidateEvent(e) == nil {
		t.Fatal("missing link accepted for external provider")
	}
}
