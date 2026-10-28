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
	return fx.Purpose == "meeting_preparation" || fx.Purpose == "meeting_audio" || fx.Purpose == "meeting_call" || fx.Purpose == "meeting_sms"
}

// Preparation is measured from the successful opening, not from the meeting.
// It never catches up late or competes with the final 30-minute reminder window.
func PreparationSequence(s State, now time.Time) []Effect {
	if s.LastFormContactAt == nil || s.Booking == nil || s.Booking.Outcome != "" {
		return nil
	}
	if s.LastPreparationAt != nil && !s.LastPreparationAt.Before(*s.LastFormContactAt) {
		return nil
	}
	due := s.LastFormContactAt.Add(15 * time.Minute)
	cutoff := s.Booking.Start.Add(-30 * time.Minute)
	if !due.Before(cutoff) {
		return nil
	}
	expires := due.Add(5 * time.Minute)
	if cutoff.Before(expires) {
		expires = cutoff
	}
	if !now.Before(expires) {
		return nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+s.Booking.ID+"\x00"+s.Booking.Start.UTC().Format(time.RFC3339Nano))))
	return []Effect{{ID: "fx/meeting:" + key + ":meeting_preparation", LeadID: s.ID, Kind: "send", Purpose: "meeting_preparation", Due: due, Expires: expires, BookingID: s.Booking.ID, BookingStart: s.Booking.Start}}
}
