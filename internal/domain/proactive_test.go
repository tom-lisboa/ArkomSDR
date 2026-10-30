package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNoReplyCadenceStopsOnResponseBookingAndActivation(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	a := now.Add(-20 * time.Minute)
	s := State{ID: "lead", Owner: "go", FirstContactAt: &a, Profile: Form{Name: "Ana", Consent: true}}
	p := Policy{NoReplyEnabled: true, NoReplySince: a.Add(-time.Minute), QuietStart: 0, QuietEnd: 24}
	steps := NoReplySequence(s, p, now)
	if len(steps) != 4 {
		t.Fatal(steps)
	}
	if !steps[0].Due.Equal(a.Add(15*time.Minute)) || !steps[3].Due.Equal(a.Add(72*time.Hour)) {
		t.Fatal("wrong cadence")
	}
	if ok, why := Deliverable(s, steps[0], now); !ok {
		t.Fatal(why)
	}
	for _, kind := range []string{"reply", "booking", "form_changed", "paused", "human", "optout", "no_consent"} {
		v := s
		switch kind {
		case "reply":
			v.LastInbound = now
		case "booking":
			v.Booking = &Booking{ID: "new"}
		case "form_changed":
			newAnchor := a.Add(time.Minute)
			v.LastFormContactAt = &newAnchor
		case "paused":
			v.Paused = true
		case "human":
			v.Human = true
		case "optout":
			v.OptedOut = true
		case "no_consent":
			v.Profile.Consent = false
		}
		if ok, _ := Deliverable(v, steps[0], now); ok {
			t.Fatal("sent after", kind)
		}
	}
	p.NoReplySince = a.Add(time.Second)
	if len(NoReplySequence(s, p, now)) != 0 {
		t.Fatal("backfilled pre-activation contact")
	}
	s.LastInbound = a
	if len(NoReplySequence(s, Policy{NoReplyEnabled: true, NoReplySince: a.Add(-time.Minute)}, now)) != 0 {
		t.Fatal("equal-time reply ignored")
	}
}
func TestNoReplyQuietHoursNeverStackMessagesInTheMorning(t *testing.T) {
	zone, _ := time.LoadLocation("America/Cuiaba")
	a := time.Date(2026, 10, 30, 19, 55, 0, 0, zone)
	s := State{ID: "lead", FirstContactAt: &a, Profile: Form{Consent: true, TimeZone: "America/Cuiaba"}}
	p := Policy{NoReplyEnabled: true, NoReplySince: a.Add(-time.Minute), QuietStart: 8, QuietEnd: 20}
	steps := NoReplySequence(s, p, a)
	if len(steps) != 4 || steps[0].Due.In(zone).Hour() != 8 || steps[1].Due.Sub(steps[0].Due) < 90*time.Minute {
		t.Fatal(steps)
	}
	if len(NoReplySequence(s, p, a.Add(4*24*time.Hour))) != 0 {
		t.Fatal("late catch-up burst")
	}
}
func TestMeetingCheckinsPreserveBookingAndDoNotAssumeNoShow(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := State{ID: "lead", Profile: Form{Consent: true}, Booking: &Booking{ID: "booking", Start: now.Add(-8 * time.Minute), End: now.Add(37 * time.Minute)}}
	steps := MeetingCheckinSequence(s, Policy{MeetingCheckins: true}, now)
	if len(steps) != 1 || steps[0].Purpose != "meeting_checkin_7m_after" {
		t.Fatal(steps)
	}
	if ok, why := Deliverable(s, steps[0], now); !ok {
		t.Fatal(why)
	}
	for _, kind := range []string{"answered", "confirmed", "attended", "rescheduled", "cancelled"} {
		v := s
		b := *s.Booking
		v.Booking = &b
		switch kind {
		case "answered":
			v.LastInbound = now.Add(-time.Minute)
		case "confirmed":
			confirmed := now.Add(-time.Minute)
			b.ConfirmedAt = &confirmed
		case "attended":
			b.Outcome = "attended"
		case "rescheduled":
			b.Start = b.Start.Add(time.Hour)
		case "cancelled":
			v.Booking = nil
		}
		if ok, _ := Deliverable(v, steps[0], now); ok {
			t.Fatal("unexpected reminder", kind)
		}
	}
	if s.Booking.Outcome != "" {
		t.Fatal("clock inferred no-show")
	}
	text := Render(s, steps[0], Policy{}, now)
	if strings.Contains(text, "America/") || strings.Contains(text, "não te vimos") || strings.Contains(text, "estamos esperando") {
		t.Fatal(text)
	}
}
func TestProactiveTemplateAssetsMatchExactly(t *testing.T) {
	for purpose, pattern := range ProactivePatterns {
		raw, err := os.ReadFile("../../deploy/cadence/" + purpose + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var content struct {
			Variables map[string]string `json:"variables"`
			Types     map[string]struct {
				Body    string                       `json:"body"`
				Actions []struct{ Title, ID string } `json:"actions"`
			} `json:"types"`
		}
		if err = json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
		body := content.Types["twilio/quick-reply"]
		if body.Body != pattern || len(body.Actions) != 3 {
			t.Fatal("template mismatch", purpose)
		}
		for _, action := range body.Actions {
			if len([]rune(action.Title)) > 20 || !strings.HasPrefix(action.ID, "{{") {
				t.Fatal(action)
			}
		}
	}
}
