package domain

import (
	"testing"
	"time"
)

func TestPreparationTimingAndCurrentBookingGuards(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	s := State{ID: "lead", Profile: Form{Consent: true}, LastFormContactAt: &now, Booking: &Booking{ID: "booking", Start: now.Add(2 * time.Hour)}}
	fx := PreparationSequence(s, now)
	if len(fx) != 1 || !fx[0].Due.Equal(now.Add(15*time.Minute)) || !fx[0].Expires.Equal(now.Add(20*time.Minute)) {
		t.Fatal(fx)
	}
	if next := PreparationSequence(s, now.Add(time.Minute)); next[0].ID != fx[0].ID {
		t.Fatal("restart changed identity")
	}
	if len(PreparationSequence(s, now.Add(20*time.Minute))) != 0 {
		t.Fatal("late catch-up")
	}
	for _, change := range []string{"cancel", "reschedule", "optout", "pause", "human", "consent"} {
		changed := s
		b := *s.Booking
		changed.Booking = &b
		switch change {
		case "cancel":
			changed.Booking = nil
		case "reschedule":
			changed.Booking.Start = b.Start.Add(time.Hour)
		case "optout":
			changed.OptedOut = true
		case "pause":
			changed.Paused = true
		case "human":
			changed.Human = true
		case "consent":
			changed.Profile.Consent = false
		}
		if ok, _ := Deliverable(changed, fx[0], fx[0].Due); ok {
			t.Fatal("guard failed", change)
		}
	}
	s.Booking.Start = now.Add(45 * time.Minute)
	if len(PreparationSequence(s, now)) != 0 {
		t.Fatal("overlaps final reminder window")
	}
	s.Booking.Start = now.Add(47 * time.Minute)
	if got := PreparationSequence(s, now); len(got) != 1 || !got[0].Expires.Equal(now.Add(17*time.Minute)) {
		t.Fatal(got)
	}
	s.LastFormContactAt = nil
	if len(PreparationSequence(s, now)) != 0 {
		t.Fatal("scheduled before successful opening")
	}
}

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
