package httpapi

import "net/http"

func (s *Server) followupStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT queue,status,count(*) FROM bia_go.jobs WHERE payload->>'purpose' IN ('meeting_audio','meeting_call','meeting_sms','meeting_preparation','meeting_start') GROUP BY queue,status ORDER BY queue,status`)
	if err != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	defer rows.Close()
	type count struct {
		Queue  string `json:"queue"`
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	counts := []count{}
	for rows.Next() {
		var c count
		if err = rows.Scan(&c.Queue, &c.Status, &c.Count); err != nil {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
		counts = append(counts, c)
	}
	if rows.Err() != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, map[string]any{"enabled": s.Policy.MeetingReminders, "preparation_enabled": s.Policy.MeetingPreparation, "preparation_after_minutes": 15, "sms_enabled": s.Policy.SMSReminders, "sms_before_minutes": 5, "audio_configured": s.ReminderAudio.SHA256 != "", "audio_ready": s.ReminderAudio.Ready(), "audio_before_minutes": 30, "call_request_before_minutes": 15, "meeting_start_enabled": s.Policy.MeetingStart, "meeting_start_after_minutes": 0, "late_grace_minutes": 5, "jobs": counts})
}
