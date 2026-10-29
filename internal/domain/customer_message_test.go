package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMessagesHideTimezoneWithoutChangingLocalTime(t *testing.T) {
	utc := time.Date(2026, 10, 30, 20, 30, 0, 0, time.UTC)
	for _, tc := range []struct{ zone, want string }{{"America/Cuiaba", "16:30"}, {"America/Sao_Paulo", "17:30"}} {
		s := State{ID: "lead", Profile: Form{Name: "Ana", Goal: "Atender melhor", Consent: true, TimeZone: tc.zone}, Booking: &Booking{ID: "book", Start: utc, End: utc.Add(45 * time.Minute)}}
		p := Policy{TimeZone: "America/Cuiaba", Brand: "Arkom", WhatsAppPhone: "+5511936246321", DisableFollowups: true}
		for _, purpose := range []string{"form_contact", "booking_confirmation", "meeting_call", "meeting_sms", "meeting_start"} {
			text := Render(s, Effect{Kind: "send", Purpose: purpose, Text: "mensagem antiga (America/Sao_Paulo)"}, p, utc.Add(-time.Hour))
			if !strings.Contains(text, tc.want) || strings.Contains(text, "America/") || strings.Contains(text, "Horário de") {
				t.Fatalf("%s %s: %s", tc.zone, purpose, text)
			}
		}
		if s.Profile.TimeZone != tc.zone || !s.Booking.Start.Equal(utc) {
			t.Fatal("changed source timezone or meeting")
		}
		r := Reduce(State{ID: s.ID, Owner: "go", Profile: s.Profile}, Event{ID: "offer", Kind: "message.received", At: utc.Add(-time.Hour), Text: "Quero agendar"}, Decision{Action: "offer_slots"}, []Slot{{ID: "s", Start: utc, End: utc.Add(45 * time.Minute)}}, p, utc.Add(-time.Hour))
		if len(r.Effects) != 1 || !strings.Contains(r.Effects[0].Text, strings.ReplaceAll(tc.want, ":", "h")) || strings.Contains(r.Effects[0].Text, "America/") {
			t.Fatal(r)
		}
	}
}

func TestCustomerMessagePreservesContentAndURLs(t *testing.T) {
	for in, want := range map[string]string{
		"29/10 às 16:30 (America/Sao_Paulo).":                           "29/10 às 16:30.",
		"29/10 às 16:30. Horário de America/Cuiaba. Qual você escolhe?": "29/10 às 16:30. Qual você escolhe?",
		"Sua reunião é às 16h no fuso America/Sao_Paulo.":               "Sua reunião é às 16h.",
		"Veja https://example.com/agenda/America/Sao_Paulo.":            "Veja https://example.com/agenda/America/Sao_Paulo.",
	} {
		if got := CustomerMessage(in); got != want {
			t.Fatal(in, got, want)
		}
	}
}

func TestOpeningDirectResponseConfirmationWorksWithBareYes(t *testing.T) {
	now := time.Now()
	s := State{ID: "lead", Owner: "go", Profile: Form{Name: "Ana", Goal: "Atender melhor", Consent: true}, Booking: &Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}}
	text := ContactContextOpening(s, Policy{}, now).Text
	if !strings.Contains(text, "Você confirma sua presença?") {
		t.Fatal(text)
	}
	s.History = []Turn{{Role: "assistant", Text: text, At: now}}
	r := Reduce(s, Event{ID: "yes", Kind: "message.received", At: now, Text: "Sim"}, Decision{Action: "confirm"}, nil, Policy{}, now)
	if r.State.Booking.ConfirmedAt == nil || len(r.State.Offered) != 0 {
		t.Fatal(r)
	}
	if !strings.Contains(FormContactText, "Responda AGENDAR") || !strings.Contains(PreparationText, "Me envie um exemplo") || !strings.Contains(MeetingStartText, "Responda CONFIRMO ou REMARCAR") {
		t.Fatal("missing executable call to action")
	}
}
