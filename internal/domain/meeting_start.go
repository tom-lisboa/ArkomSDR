package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

const MeetingStartGrace = 5 * time.Minute
const MeetingStartText = "Oi, {{1}}! Sua reunião com a Arkom é agora: {{2}}. Separe {{3}} minutos para avaliar a IA no seu negócio.\n\nVocê participa agora? Responda CONFIRMO ou REMARCAR."

// A meeting stays relevant throughout its real interval. It is not a no-show
// merely because the scheduled start passed.
func MeetingStatus(s State, now time.Time) string {
	if s.Booking == nil || s.Booking.Outcome != "" {
		return "not_scheduled"
	}
	if s.Booking.Start.After(now) {
		return "upcoming"
	}
	if s.Booking.End.After(s.Booking.Start) && now.Before(s.Booking.End) {
		return "in_progress"
	}
	return "not_scheduled"
}
func HasActiveMeeting(s State, now time.Time) bool { return MeetingStatus(s, now) != "not_scheduled" }

func MeetingStartSequence(s State, now time.Time) []Effect {
	if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.End.After(s.Booking.Start) {
		return nil
	}
	b := s.Booking
	expires := b.Start.Add(MeetingStartGrace)
	if b.End.Before(expires) {
		expires = b.End
	}
	if !now.Before(expires) {
		return nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+b.ID+"\x00"+b.Start.UTC().Format(time.RFC3339Nano))))
	return []Effect{{ID: "fx/meeting:" + key + ":meeting_start", LeadID: s.ID, Kind: "send", Purpose: "meeting_start", Due: b.Start, Expires: expires, BookingID: b.ID, BookingStart: b.Start}}
}

func MeetingStartOpening(s State, p Policy) Opening {
	if s.Booking == nil || !s.Booking.End.After(s.Booking.Start) {
		return Opening{Kind: "meeting_start"}
	}
	name := CustomerMessage(firstName(s.Profile.Name))
	if name == "" {
		name = "tudo bem"
	}
	name = strings.Join(strings.Fields(name), " ")
	r := []rune(name)
	if len(r) > 60 {
		name = string(r[:60])
	}
	vars := map[string]string{"1": name, "2": s.Booking.Start.In(p.Zone(s)).Format("02/01 às 15:04"), "3": fmt.Sprint(int(s.Booking.End.Sub(s.Booking.Start).Minutes()))}
	pairs := []string{}
	for k, v := range vars {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return Opening{Kind: "meeting_start", Text: strings.NewReplacer(pairs...).Replace(MeetingStartText), Variables: vars}
}
