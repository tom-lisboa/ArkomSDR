package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMeetingStartTimingAndCurrentMeetingControls(t *testing.T) {
	start := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	s := State{ID: "lead", Profile: Form{Name: "Ana", Consent: true, TimeZone: "America/Cuiaba"}, Booking: &Booking{ID: "booking", Start: start, End: start.Add(45 * time.Minute)}}
	fx := MeetingStartSequence(s, start.Add(-time.Hour))
	if len(fx) != 1 || !fx[0].Due.Equal(start) || !fx[0].Expires.Equal(start.Add(5*time.Minute)) {
		t.Fatal(fx)
	}
	if retry := MeetingStartSequence(s, start.Add(time.Minute)); len(retry) != 1 || retry[0].ID != fx[0].ID {
		t.Fatal("restart lost T0 reminder", retry)
	}
	for _, at := range []time.Time{start.Add(-time.Second), start.Add(5 * time.Minute)} {
		if ok, _ := Deliverable(s, fx[0], at); ok {
			t.Fatal("sent outside T0 interval", at)
		}
	}
	if ok, why := Deliverable(s, fx[0], start); !ok {
		t.Fatal("T0 blocked", why)
	}
	if len(MeetingStartSequence(s, start.Add(5*time.Minute))) != 0 {
		t.Fatal("late catch up")
	}
	text := Render(s, fx[0], Policy{}, start)
	for _, part := range []string{"Ana", "14:00", "45 minutos", "CONFIRMO", "REMARCAR"} {
		if !strings.Contains(text, part) {
			t.Fatal("missing direct response fact", part, text)
		}
	}
	for _, bad := range []string{"https://", "sala aberta", "já está esperando", "última chance"} {
		if strings.Contains(text, bad) {
			t.Fatal("unverified claim", text)
		}
	}
	for _, change := range []string{"cancel", "reschedule", "optout", "pause", "human", "consent", "attended"} {
		v := s
		b := *s.Booking
		v.Booking = &b
		switch change {
		case "cancel":
			v.Booking = nil
		case "reschedule":
			v.Booking.Start = v.Booking.Start.Add(time.Hour)
		case "optout":
			v.OptedOut = true
		case "pause":
			v.Paused = true
		case "human":
			v.Human = true
		case "consent":
			v.Profile.Consent = false
		case "attended":
			v.Booking.Outcome = "attended"
		}
		if ok, _ := Deliverable(v, fx[0], start); ok {
			t.Fatal("guard ignored", change)
		}
	}
}

func TestConfirmAtMeetingStartKeepsReservationAndDoesNotMarkAttendance(t *testing.T) {
	now := time.Now().UTC()
	s := State{ID: "lead", Owner: "go", Profile: Form{Consent: true}, Booking: &Booking{ID: "current", Start: now.Add(-time.Minute), End: now.Add(44 * time.Minute)}}
	e := Event{ID: "confirm", LeadID: s.ID, Kind: "message.received", At: now, Text: "CONFIRMO"}
	r := Reduce(s, e, Decision{}, []Slot{{ID: "future", Start: now.Add(24 * time.Hour)}}, Policy{}, now)
	if r.State.Booking == nil || r.State.Booking.ID != "current" || r.State.Booking.ConfirmedAt == nil || r.State.Booking.Outcome != "" || len(r.State.Offered) != 0 {
		t.Fatal("lost meeting or fabricated attendance", r)
	}
	r = Reduce(s, Event{ID: "hello", Kind: "message.received", At: now, Text: "oi"}, Decision{Action: "offer_slots"}, []Slot{{ID: "future", Start: now.Add(24 * time.Hour)}}, Policy{}, now)
	if len(r.State.Offered) != 0 {
		t.Fatal("reopened scheduling during meeting", r)
	}
	if MeetingStatus(s, now) != "in_progress" || MeetingStatus(s, now.Add(45*time.Minute)) != "not_scheduled" {
		t.Fatal("incorrect interval status")
	}
	if !RescheduleRequested("REMARCAR") {
		t.Fatal("CTA is not recognized")
	}
}
