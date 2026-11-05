package management

import (
	"arkom/bia/internal/dualcalendar"
	"net/http"
)

func (s *Server) googleScheduling(w http.ResponseWriter, r *http.Request) {
	if s.Calendar == nil {
		fail(w, 503, "google_scheduling_not_enabled")
		return
	}
	p, err := s.Calendar.Profile(r.Context(), actor(r).ID)
	if readError(w, err) {
		return
	}
	write(w, 200, p)
}
func (s *Server) googleSchedulingSave(w http.ResponseWriter, r *http.Request) {
	if s.Calendar == nil {
		fail(w, 503, "google_scheduling_not_enabled")
		return
	}
	var p dualcalendar.Profile
	if !decode(w, r, &p) {
		return
	}
	if err := s.Calendar.Save(r.Context(), actor(r).ID, p); err != nil {
		switch err.Error() {
		case "google_account_not_found", "google_scheduling_permission_required", "google_calendar_write_required", "google_destination_required", "invalid_calendar_selection", "too_many_blocking_calendars", "google_scheduling_operation_pending":
			fail(w, 409, err.Error())
		default:
			fail(w, 502, "google_scheduling_setup_failed")
		}
		return
	}
	s.googleScheduling(w, r)
}
