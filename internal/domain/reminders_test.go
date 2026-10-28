package domain

import (
	"testing"
	"time"
)

func TestMeetingSequenceTimingAndIdentity(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	s := State{ID: "lead", Booking: &Booking{ID: "booking", Start: now.Add(time.Hour)}}
	fx := MeetingSequence(s, now)
	if len(fx) != 2 || fx[0].Kind != "prepare_audio" || fx[1].Kind != "send" || !fx[0].Due.Equal(s.Booking.Start.Add(-30*time.Minute)) || !fx[1].Due.Equal(s.Booking.Start.Add(-15*time.Minute)) {
		t.Fatal(fx)
	}
	if retry := MeetingSequence(s, now.Add(time.Minute)); retry[0].ID != fx[0].ID {
		t.Fatal("unstable identity")
	}
	if overdue := MeetingSequence(s, s.Booking.Start.Add(-25*time.Minute)); len(overdue) != 1 {
		t.Fatal("late audio replay", overdue)
	}
	if overdue := MeetingSequence(s, s.Booking.Start.Add(-10*time.Minute)); len(overdue) != 0 {
		t.Fatal("late reminder replay")
	}
	s.Booking.Start = s.Booking.Start.Add(time.Hour)
	if MeetingSequence(s, now)[0].ID == fx[0].ID {
		t.Fatal("reschedule reused identity")
	}
	if ok, _ := Deliverable(s, fx[0], now); ok {
		t.Fatal("stale booking delivered")
	}
}

func TestMeetingReminderGuardsAllSteps(t *testing.T) {
	now := time.Now()
	baseline := State{Profile: Form{Consent: true}, Booking: &Booking{ID: "b", Start: now.Add(time.Hour)}}
	for _, kind := range []string{"prepare_audio", "send_audio", "send", "request_call"} {
		fx := Effect{Kind: kind, Purpose: "meeting_call", BookingID: "b", BookingStart: baseline.Booking.Start, Expires: now.Add(time.Minute)}
		for _, change := range []string{"optout", "pause", "human", "consent", "cancel", "reschedule", "outcome"} {
			s := baseline
			b := *baseline.Booking
			s.Booking = &b
			switch change {
			case "optout":
				s.OptedOut = true
			case "pause":
				s.Paused = true
			case "human":
				s.Human = true
			case "consent":
				s.Profile.Consent = false
			case "cancel":
				s.Booking = nil
			case "reschedule":
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
			case "outcome":
				s.Booking.Outcome = "attended"
			}
			if ok, _ := Deliverable(s, fx, now); ok {
				t.Fatal(kind, change)
			}
		}
	}
}
