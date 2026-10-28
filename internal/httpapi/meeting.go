package httpapi

import (
	"arkom/bia/internal/domain"
	"encoding/json"
	"net/http"
)

// Meeting metadata enriches a known reservation, never creates one from form input.
func (s *Server) meetingBriefing(w http.ResponseWriter, r *http.Request) {
	var b domain.MeetingBriefing
	if !decode(w, r, &b) {
		return
	}
	if len(b.Consultant) > 120 || len(b.Format) > 1000 || len(b.Agenda) > 2000 || len(b.Context) > 4000 || (b.ConsultantGender != "" && b.ConsultantGender != "ele" && b.ConsultantGender != "ela") || (b.Environment != "" && b.Environment != "silencioso" && b.Environment != "publico") {
		write(w, 422, map[string]string{"error": "invalid_briefing"})
		return
	}
	raw, _ := json.Marshal(b)
	ct, e := s.DB.Pool.Exec(r.Context(), `UPDATE bia_go.bookings SET briefing=$3 WHERE id=$1 AND lead_id=$2 AND cancelled_at IS NULL`, r.PathValue("booking"), r.PathValue("id"), raw)
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	if ct.RowsAffected() != 1 {
		write(w, 404, map[string]string{"error": "active_booking_not_found"})
		return
	}
	write(w, 200, map[string]string{"status": "saved"})
}
func (s *Server) customerContext(w http.ResponseWriter, r *http.Request) {
	st, e := s.DB.Lead(r.Context(), r.PathValue("id"))
	if e != nil {
		write(w, 404, map[string]string{"error": "lead_not_found"})
		return
	}
	write(w, 200, domain.CustomerContext(st, st.Booking, s.Policy))
}
func (s *Server) reminderNotes(w http.ResponseWriter, r *http.Request) {
	rows, e := s.DB.Pool.Query(r.Context(), `SELECT body,created_at FROM bia_go.crm_notes WHERE lead_id=$1 AND purpose='voice_reminder' ORDER BY created_at DESC LIMIT 100`, r.PathValue("id"))
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	defer rows.Close()
	notes := []map[string]any{}
	for rows.Next() {
		var body string
		var at any
		if e = rows.Scan(&body, &at); e != nil {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
		notes = append(notes, map[string]any{"text": body, "created_at": at})
	}
	if rows.Err() != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, map[string]any{"notes": notes})
}
