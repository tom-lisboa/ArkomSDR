package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
)

// Only vacant slot IDs and times are public. No identities, room URLs or
// reservations are exposed through the scoped test-form capability.
func (s *Server) testAvailability(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	if s.Calendar == nil {
		write(w, 503, map[string]string{"error": "local_calendar_not_enabled"})
		return
	}
	tz := r.URL.Query().Get("timezone")
	if tz == "" {
		tz = s.Policy.TimeZone
	}
	zone, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		testValidationError(w, r, "invalid_timezone", "timezone")
		return
	}
	now := time.Now()
	start := time.Date(now.In(zone).Year(), now.In(zone).Month(), now.In(zone).Day(), 0, 0, 0, 0, zone)
	if date := r.URL.Query().Get("date"); date != "" {
		start, err = time.ParseInLocation("2006-01-02", date, zone)
		if err != nil {
			testValidationError(w, r, "invalid_date", "date")
			return
		}
	}
	end := start.AddDate(0, 0, 14)
	if r.URL.Query().Get("date") != "" {
		end = start.AddDate(0, 0, 1)
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT id,starts_at,ends_at FROM bia_go.slots WHERE reserved_by IS NULL AND starts_at>$1 AND starts_at>=$2 AND starts_at<$3 ORDER BY starts_at,id LIMIT 500`, now.Add(30*time.Minute), start, end)
	if err != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	defer rows.Close()
	slots := []map[string]any{}
	for rows.Next() {
		var slot domain.Slot
		if err = rows.Scan(&slot.ID, &slot.Start, &slot.End); err != nil {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
		slots = append(slots, map[string]any{"id": slot.ID, "start": slot.Start.In(zone), "end": slot.End.In(zone), "date": slot.Start.In(zone).Format("2006-01-02"), "label": domain.SlotLabel(slot, zone), "duration_minutes": int(slot.End.Sub(slot.Start).Minutes())})
	}
	if rows.Err() != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, map[string]any{"timezone": tz, "minimum_notice_minutes": 30, "slots": slots})
}

func (s *Server) testBooking(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	if s.Calendar == nil {
		write(w, 503, map[string]string{"error": "local_calendar_not_enabled"})
		return
	}
	var in struct {
		Phone        string `json:"phone"`
		SubmissionID string `json:"submission_id"`
		SlotID       string `json:"slot_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if !domain.ValidID(id) || len(id) > 100 || !domain.ValidID(in.SubmissionID) || len(in.SubmissionID) > 100 || !domain.ValidID(in.SlotID) || len(in.SlotID) > 200 {
		testValidationError(w, r, "valid_submission_slot_and_idempotency_key_required", "submission_id", "slot_id", "Idempotency-Key")
		return
	}
	phone, err := domain.NormalizePhone(in.Phone)
	if err != nil {
		testValidationError(w, r, "phone_must_be_e164", "phone")
		return
	}
	b, duplicate, err := s.DB.ReserveTestForm(r.Context(), id, in.SubmissionID, phone, in.SlotID, s.Mode)
	if calendarError(w, err) {
		return
	}
	status := 201
	if duplicate {
		status = 200
	}
	write(w, status, map[string]any{"status": "booked", "duplicate": duplicate, "submission_id": in.SubmissionID, "booking": b})
}

func calendarError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	code, status := "storage_unavailable", 503
	var blocked *store.FormBookingError
	var provider *providers.Failure
	switch {
	case errors.Is(err, store.ErrConflict):
		code, status = "idempotency_conflict", 409
	case errors.As(err, &blocked):
		code, status = blocked.Code, 409
		if code == "test_consent_rate_limit" {
			status = 429
		}
		if code == "test_submission_not_found" || code == "slot_not_found" {
			status = 404
		}
	case errors.As(err, &provider):
		code, status = provider.Code, 409
	}
	writeActionError(w, status, code)
	return true
}

func (s *Server) formBooking(r *http.Request, id string) (*domain.Booking, error) {
	var raw []byte
	err := s.DB.Pool.QueryRow(r.Context(), `SELECT payload FROM bia_go.bookings WHERE operation_id=$1 AND cancelled_at IS NULL`, "form-booking:"+id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &store.FormBookingError{Code: "booking_no_longer_active"}
	}
	if err != nil {
		return nil, err
	}
	var b domain.Booking
	err = json.Unmarshal(raw, &b)
	return &b, err
}
