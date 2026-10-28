package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"github.com/jackc/pgx/v5"
)

func (s *Server) testReminder(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	action := r.PathValue("action")
	if action != "audio" && action != "call" && action != "sms" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Phone        string `json:"phone"`
		SubmissionID string `json:"submission_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if !domain.ValidID(id) || len(id) > 100 || !domain.ValidID(input.SubmissionID) || len(input.SubmissionID) > 100 {
		write(w, 422, map[string]string{"error": "valid_submission_and_idempotency_key_required"})
		return
	}
	phone, err := domain.NormalizePhone(input.Phone)
	if err != nil {
		write(w, 422, map[string]string{"error": "phone_must_be_e164"})
		return
	}
	job, duplicate, err := s.DB.QueueTestReminder(r.Context(), id, input.SubmissionID, phone, action, s.Mode, s.ReminderAudio.Ready(), s.VoiceEnabled, s.SMSEnabled)
	var blocked *store.TestReminderError
	switch {
	case errors.Is(err, store.ErrConflict):
		write(w, 409, map[string]string{"error": "idempotency_conflict"})
		return
	case errors.As(err, &blocked):
		code := 409
		switch blocked.Code {
		case "test_submission_not_found":
			code = 404
		case "test_trigger_rate_limit", "permission_request_limit":
			code = 429
		case "reminder_audio_not_ready", "voice_not_enabled", "sms_not_enabled":
			code = 503
		}
		write(w, code, map[string]string{"error": blocked.Code})
		return
	case err != nil:
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	status := "queued"
	if s.Mode == "shadow" {
		status = "queued_for_simulation"
	}
	write(w, 202, map[string]any{"status": status, "action": action, "request_id": id, "job_id": job, "duplicate": duplicate, "status_url": r.URL.Path + "/" + id, "requires_call_permission": action == "call"})
}

// Return only delivery progress; no profile, conversation or admin data.
func (s *Server) testReminderStatus(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	action, id := r.PathValue("action"), r.PathValue("id")
	if (action != "audio" && action != "call" && action != "sms") || !domain.ValidID(id) || len(id) > 100 {
		http.NotFound(w, r)
		return
	}
	root := "fx/demo:" + id
	j, err := s.DB.Job(r.Context(), root)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	var fx domain.Effect
	if json.Unmarshal(j.Payload, &fx) != nil || !fx.ManualReminder || (action == "audio" && fx.Purpose != "meeting_audio") || (action == "call" && fx.Purpose != "meeting_call") || (action == "sms" && fx.Purpose != "meeting_sms") {
		http.NotFound(w, r)
		return
	}
	type progress struct {
		Step   string `json:"step"`
		Status string `json:"status"`
		Reason string `json:"reason,omitempty"`
	}
	steps := []progress{}
	for _, step := range []struct{ id, name string }{{root, "followup"}, {root + ":media", "audio"}, {root + ":permission", "permission_request"}} {
		job, e := s.DB.Job(r.Context(), step.id)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
		var result struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(job.Result, &result)
		if job.ErrorCode != nil {
			result.Reason = *job.ErrorCode
		}
		steps = append(steps, progress{step.name, job.Status, result.Reason})
	}
	if action == "call" {
		voiceID := "meeting:" + domain.LeadKey(root + ":permission")[5:]
		v, e := (voiceworker.Repository{DB: s.DB}).Get(r.Context(), voiceID)
		if e == nil {
			steps = append(steps, progress{"call", v.State, v.Error})
		} else if !errors.Is(e, pgx.ErrNoRows) {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
	}
	write(w, 200, map[string]any{"request_id": id, "action": action, "mode": j.Mode, "steps": steps})
}
