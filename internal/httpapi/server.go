package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/remindermedia"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"github.com/jackc/pgx/v5"
)

type Server struct {
	FormAutoContact bool
	ReminderAudio   remindermedia.Asset
	TestFormToken   string
	VoiceSender     string
	VoiceEnabled    bool
	DB              *store.DB
	Calendar        *providers.LocalCalendar
	Token, Mode     string
	Policy          domain.Policy
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.Handle("/media/reminders/", s.ReminderAudio)
	m.Handle("GET /v1/followups", s.auth(http.HandlerFunc(s.followupStatus)))
	m.HandleFunc("POST /webhooks/test-form/{key}", s.testForm)
	m.HandleFunc("OPTIONS /webhooks/test-form/{key}", s.testForm)
	m.HandleFunc("POST /webhooks/test-form/{key}/reminders/{action}", s.testReminder)
	m.HandleFunc("OPTIONS /webhooks/test-form/{key}/reminders/{action}", s.testReminder)
	m.HandleFunc("GET /webhooks/test-form/{key}/reminders/{action}/{id}", s.testReminderStatus)
	m.HandleFunc("OPTIONS /webhooks/test-form/{key}/reminders/{action}/{id}", s.testReminderStatus)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), time.Second)
		defer c()
		if s.DB.Pool.Ping(ctx) != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	m.Handle("POST /v1/leads/{id}/voice", s.auth(http.HandlerFunc(s.requestVoice)))
	m.Handle("GET /v1/voice/{id}", s.auth(http.HandlerFunc(s.voiceStatus)))
	m.Handle("GET /v1/leads/{id}/context", s.auth(http.HandlerFunc(s.customerContext)))
	m.Handle("GET /v1/leads/{id}/reminder-notes", s.auth(http.HandlerFunc(s.reminderNotes)))
	m.Handle("PUT /v1/leads/{id}/bookings/{booking}/briefing", s.auth(http.HandlerFunc(s.meetingBriefing)))
	m.Handle("POST /v1/forms", s.auth(http.HandlerFunc(s.form)))
	m.Handle("POST /v1/events", s.auth(http.HandlerFunc(s.event)))
	m.Handle("POST /v1/slots", s.auth(http.HandlerFunc(s.addSlot)))
	m.Handle("GET /v1/leads/{id}/availability", s.auth(http.HandlerFunc(s.availability)))
	m.Handle("GET /v1/leads/{id}", s.auth(http.HandlerFunc(s.lead)))
	m.Handle("GET /v1/jobs/{id}", s.auth(http.HandlerFunc(s.job)))
	m.Handle("GET /v1/metrics", s.auth(http.HandlerFunc(s.metrics)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		m.ServeHTTP(w, r)
	})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.Token)) != 1 {
			write(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		write(w, 400, map[string]string{"error": "invalid_json"})
		return false
	}
	if e := d.Decode(new(any)); e != io.EOF {
		write(w, 400, map[string]string{"error": "trailing_json"})
		return false
	}
	return true
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request, e domain.Event) (bool, bool) {
	if err := domain.ValidateEvent(e); err != nil {
		write(w, 422, map[string]string{"error": err.Error()})
		return false, false
	}
	if e.At.After(time.Now().Add(5 * time.Minute)) {
		write(w, 422, map[string]string{"error": "event timestamp is in the future"})
		return false, false
	}
	inserted, err := s.DB.Submit(r.Context(), e, s.Mode)
	if errors.Is(err, store.ErrConflict) {
		write(w, 409, map[string]string{"error": "idempotency_conflict"})
		return false, false
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return false, false
	}
	return true, inserted
}
func (s *Server) form(w http.ResponseWriter, r *http.Request) {
	var f domain.Form
	if !decode(w, r, &f) {
		return
	}
	ph, err := domain.NormalizePhone(f.Phone)
	if err != nil {
		write(w, 422, map[string]string{"error": err.Error()})
		return
	}
	f.Phone = ph
	if f.TimeZone == "" {
		f.TimeZone = s.Policy.TimeZone
	}
	e := domain.Event{ID: r.Header.Get("Idempotency-Key"), Kind: "form.submitted", LeadID: domain.LeadKey(ph), At: time.Now().UTC(), Form: &f}
	if ok, inserted := s.submit(w, r, e); ok {
		contact := "queued"
		if s.Mode == "shadow" {
			contact = "queued_for_simulation"
		}
		if !f.Consent {
			contact = "not_requested"
		}
		write(w, 202, map[string]any{"event_id": e.ID, "lead_id": e.LeadID, "duplicate": !inserted, "mode": s.Mode, "contact_status": contact, "next_step": map[string]any{"type": "schedule", "label": "Escolher meu horário", "url": s.Policy.SchedulingURL, "availability_endpoint": "/v1/leads/" + e.LeadID + "/availability", "revenue_gate": false}, "message": "Cadastro recebido. Você já pode consultar os horários; se não houver vagas publicadas, registre sua preferência."})
	}
}
func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	var e domain.Event
	if !decode(w, r, &e) {
		return
	}
	if ok, inserted := s.submit(w, r, e); ok {
		write(w, 202, map[string]any{"event_id": e.ID, "duplicate": !inserted, "mode": s.Mode})
	}
}
func (s *Server) lead(w http.ResponseWriter, r *http.Request) {
	v, e := s.DB.Lead(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, v)
}
func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	v, e := s.DB.Job(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, v)
}
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	st, e := s.DB.Lead(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"status": "unavailable", "message": "Não foi possível consultar a agenda agora."})
		return
	}
	slots, e := s.Calendar.Slots(r.Context(), st, time.Now().UTC())
	if e != nil {
		write(w, 503, map[string]string{"status": "unavailable", "message": "Não foi possível consultar a agenda agora.", "next_step": "record_preference"})
		return
	}
	status := "available"
	message := "Escolha um horário."
	if len(slots) == 0 {
		status = "empty"
		message = "Nenhum horário publicado neste momento. Registre o dia e período que prefere."
	}
	write(w, 200, map[string]any{"status": status, "message": message, "slots": slots, "timezone": s.Policy.Zone(st).String(), "next_step": "record_preference", "preference_event": "schedule.preference"})
}
func (s *Server) addSlot(w http.ResponseWriter, r *http.Request) {
	var v providers.LocalSlot
	if !decode(w, r, &v) {
		return
	}
	if e := s.Calendar.Add(r.Context(), v); e != nil {
		write(w, 422, map[string]string{"error": "invalid_or_overlapping_slot"})
		return
	}
	write(w, 201, map[string]string{"id": v.ID, "provider": "local"})
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	v, e := s.DB.Metrics(r.Context())
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, v)
}

func (s *Server) requestVoice(w http.ResponseWriter, r *http.Request) {
	if s.Mode == "live" && !s.VoiceEnabled {
		write(w, 503, map[string]string{"error": "voice_not_enabled"})
		return
	}
	var req struct {
		Purpose string `json:"purpose"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Purpose == "" {
		req.Purpose = "confirm"
	}
	id := r.Header.Get("Idempotency-Key")
	repo := voiceworker.Repository{DB: s.DB}
	inserted, e := repo.Create(r.Context(), id, r.PathValue("id"), s.Mode, s.VoiceSender, req.Purpose)
	if errors.Is(e, pgx.ErrNoRows) {
		write(w, 404, map[string]string{"error": "lead_not_found"})
		return
	}
	if errors.Is(e, voiceworker.ErrBlocked) || errors.Is(e, store.ErrConflict) {
		write(w, 409, map[string]string{"error": "voice_request_blocked_or_conflicting"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 202, map[string]any{"id": id, "duplicate": !inserted, "mode": s.Mode, "next_step": "request_call_permission"})
}
func (s *Server) voiceStatus(w http.ResponseWriter, r *http.Request) {
	v, e := (voiceworker.Repository{DB: s.DB}).Get(r.Context(), r.PathValue("id"))
	if errors.Is(e, pgx.ErrNoRows) {
		write(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	write(w, 200, v)
}
