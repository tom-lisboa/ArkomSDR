package domain

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// MeetingSequence is a separate, explicitly enabled cadence. IDs include the
// booking time so rescheduling invalidates the old jobs without duplicating new ones.
func MeetingSequence(s State, now time.Time) []Effect {
	if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) {
		return nil
	}
	b := s.Booking
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+b.ID+"\x00"+b.Start.UTC().Format(time.RFC3339Nano))))
	var out []Effect
	for _, step := range []struct {
		purpose, kind string
		before        time.Duration
	}{
		{"meeting_audio", "prepare_audio", 30 * time.Minute},
		{"meeting_call", "send", 15 * time.Minute},
	} {
		due := b.Start.Add(-step.before)
		expires := due.Add(5 * time.Minute)
		if !now.Before(expires) {
			continue
		}
		out = append(out, Effect{ID: "fx/meeting:" + key + ":" + step.purpose, LeadID: s.ID, Kind: step.kind, Purpose: step.purpose, Due: due, Expires: expires, BookingID: b.ID, BookingStart: b.Start})
	}
	return out
}

func IsMeetingReminder(fx Effect) bool {
	return fx.Purpose == "meeting_audio" || fx.Purpose == "meeting_call"
}
