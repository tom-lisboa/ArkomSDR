package domain

import "time"

type MeetingResult struct {
	BookingID  string    `json:"booking_id"`
	Start      time.Time `json:"start"`
	Outcome    string    `json:"outcome"`
	RecordedAt time.Time `json:"recorded_at"`
}

func (s *State) RecordMeetingResult(v MeetingResult) {
	for i, old := range s.MeetingResults {
		if old.BookingID == v.BookingID {
			s.MeetingResults[i] = v
			return
		}
	}
	s.MeetingResults = append(s.MeetingResults, v)
	if len(s.MeetingResults) > 10 {
		s.MeetingResults = s.MeetingResults[len(s.MeetingResults)-10:]
	}
}
