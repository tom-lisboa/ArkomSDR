package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFormContactMatchesTemplateAsset(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/form-contact-template.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Types map[string]struct {
			Body string `json:"body"`
		} `json:"types"`
	}
	if err = json.Unmarshal(raw, &v); err != nil || v.Types["twilio/text"].Body != FormContactText {
		t.Fatal("template text drift", err)
	}
}

func TestOpeningUsesCurrentBookingWithoutRepeatingKnownGoal(t *testing.T) {
	now := time.Now()
	start := now.Add(24 * time.Hour)
	s := State{Profile: Form{Name: "Ana Silva", Goal: "Quero vender mais", TimeZone: "America/Cuiaba"}, Booking: &Booking{ID: "confirmed", Start: start, End: start.Add(45 * time.Minute)}}
	o := ContactOpening(s, Policy{TimeZone: "America/Cuiaba"}, now)
	if o.Kind != "booked" || !strings.Contains(o.Text, "45 minutos") || strings.Contains(o.Text, "Quero vender mais") || strings.Contains(o.Text, "?") || strings.Contains(o.Text, "https://") {
		t.Fatal(o)
	}
	details := ContactContextOpening(s, Policy{}, now)
	if !strings.Contains(details.Text, "Quero vender mais") || strings.Contains(details.Text, "o que mais toma") {
		t.Fatal(details)
	}
	s.Profile.Goal = ""
	o = ContactOpening(s, Policy{}, now)
	if o.Kind != "booked_question" || strings.Contains(o.Text, "?") || !strings.Contains(ContactContextOpening(s, Policy{}, now).Text, "Qual rotina") {
		t.Fatal(o)
	}
	s.Booking = nil
	o = ContactOpening(s, Policy{}, now)
	if o.Kind != "unbooked" || strings.Contains(o.Text, "confirmada") || o.Variables["1"] != "Ana" {
		t.Fatal(o)
	}
	s.Profile.Name = "Ana\nextra"
	s.Profile.Goal = "{{1}}\nfoo"
	s.Booking = &Booking{ID: "confirmed", Start: start, End: start.Add(45 * time.Minute)}
	o = ContactContextOpening(s, Policy{}, now)
	if o.Variables["1"] != "{{1}} foo" || !strings.Contains(o.Text, "{{1}} foo") {
		t.Fatal("recursive replacement or multiline variable", o)
	}
	for file, want := range map[string]string{"preparation-template.json": PreparationText, "booked-contact-template.json": BookedContactText, "booked-question-template.json": BookedQuestionText, "booked-context-template.json": BookedContextText, "booked-context-question-template.json": BookedContextQuestionText} {
		raw, err := os.ReadFile("../../deploy/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Types map[string]struct {
				Body string `json:"body"`
			} `json:"types"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Types["twilio/text"].Body != want {
			t.Fatal("template drift", file)
		}
	}
}

func TestOpeningBubblesAreShortAndKeepFullFormObjective(t *testing.T) {
	now := time.Now()
	s := State{ID: "lead", Profile: Form{Name: strings.Repeat("A", 80), Goal: strings.Repeat("objetivo muito longo ", 30), Consent: true, TimeZone: "America/Cuiaba"}, Booking: &Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}}
	full := s.Profile.Goal
	for _, o := range []Opening{ContactOpening(s, Policy{}, now), ContactContextOpening(s, Policy{}, now)} {
		if utf8.RuneCountInString(o.Text) > 200 {
			t.Fatal("opening bubble too long", utf8.RuneCountInString(o.Text), o.Text)
		}
	}
	fx := ContactContextSequence(s, "opening", Policy{}, now)
	if len(fx) != 1 || fx[0].ID != "opening:context" || !fx[0].Due.Equal(now.Add(2*time.Second)) || fx[0].BookingID != "b" || s.Profile.Goal != full {
		t.Fatal(fx)
	}
	s.Booking = nil
	if len(ContactContextSequence(s, "unbooked", Policy{}, now)) != 0 {
		t.Fatal("unbooked contact got booked context")
	}
}
