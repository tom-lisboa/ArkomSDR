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
	if len(steps) != 6 {
		t.Fatal(steps)
	}
	for i, delay := range []time.Duration{15 * time.Minute, 2 * time.Hour, 6 * time.Hour, 24 * time.Hour, 48 * time.Hour, 72 * time.Hour} {
		if !steps[i].Due.Equal(a.Add(delay)) {
			t.Fatal("wrong cadence", steps[i])
		}
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
		for _, fx := range steps {
			if ok, _ := Deliverable(v, fx, fx.Due); ok {
				t.Fatal("sent after", kind, fx.Purpose)
			}
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
	if len(steps) != 6 || steps[0].Due.In(zone).Hour() != 8 {
		t.Fatal(steps)
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].Due.Sub(steps[i-1].Due) < 90*time.Minute {
			t.Fatal("stacked reminders", steps[i-1], steps[i])
		}
	}
	if len(NoReplySequence(s, p, a.Add(4*24*time.Hour))) != 0 {
		t.Fatal("late catch-up burst")
	}
}

func TestOneHourMeetingCheckinExpiresAndSkipsFreshBookings(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := State{ID: "lead", Owner: "go", Profile: Form{Consent: true}, Booking: &Booking{ID: "booking", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}}
	p := Policy{MeetingCheckins: true}
	find := func(s State, at time.Time) *Effect {
		for _, fx := range MeetingCheckinSequence(s, p, at) {
			if fx.Purpose == "meeting_checkin_1h" {
				return &fx
			}
		}
		return nil
	}
	fx := find(s, now)
	if fx == nil || !fx.Due.Equal(now) || !fx.Expires.Equal(now.Add(10*time.Minute)) {
		t.Fatal(fx)
	}
	if ok, why := Deliverable(s, *fx, now); !ok {
		t.Fatal(why)
	}
	if find(s, now.Add(10*time.Minute)) != nil {
		t.Fatal("late one-hour checkin was recovered")
	}
	s.LastBookingEventAt = now.Add(-5 * time.Minute)
	if find(s, now) != nil {
		t.Fatal("reminder stacked on new booking")
	}
	s.LastBookingEventAt = time.Time{}
	b := *s.Booking
	b.Start = b.Start.Add(time.Hour)
	b.End = b.End.Add(time.Hour)
	s.Booking = &b
	if ok, _ := Deliverable(s, *fx, now); ok {
		t.Fatal("old reminder followed a changed booking")
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
