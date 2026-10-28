package httpapi

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/store"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This capability is scoped to the test form and its demo actions, never admin.
func (s *Server) testAccess(w http.ResponseWriter, r *http.Request) bool {
	if len(s.TestFormToken) < 32 || subtle.ConstantTimeCompare([]byte(r.PathValue("key")), []byte(s.TestFormToken)) != 1 {
		http.NotFound(w, r)
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key")
	w.Header().Set("Access-Control-Max-Age", "600")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

func (s *Server) testForm(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	var input struct {
		Name     string `json:"name"`
		Company  string `json:"company"`
		Phone    string `json:"phone"`
		Email    string `json:"email,omitempty"`
		Revenue  *int64 `json:"revenue"`
		Goal     string `json:"goal,omitempty"`
		TimeZone string `json:"timezone,omitempty"`
		Consent  bool   `json:"consent"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if !domain.ValidID(id) || len(id) > 100 {
		write(w, 422, map[string]string{"error": "valid_idempotency_key_required"})
		return
	}
	phone, e := domain.NormalizePhone(input.Phone)
	if e != nil {
		write(w, 422, map[string]string{"error": "phone_must_be_e164"})
		return
	}
	f := domain.Form{Name: strings.TrimSpace(input.Name), Company: strings.TrimSpace(input.Company), Phone: phone, Email: strings.TrimSpace(input.Email), Revenue: input.Revenue, Goal: strings.TrimSpace(input.Goal), TimeZone: input.TimeZone, Consent: input.Consent, ConsentTextVersion: "test-form-v1", Source: "test_form", Channel: "gateway"}
	if !f.Consent {
		write(w, 422, map[string]string{"error": "test_contact_consent_required"})
		return
	}
	if f.TimeZone == "" {
		f.TimeZone = s.Policy.TimeZone
	}
	if e = domain.ValidateEvent(domain.Event{ID: id, LeadID: domain.LeadKey(phone), Kind: "form.submitted", At: time.Now(), Form: &f}); e != nil {
		write(w, 422, map[string]string{"error": e.Error()})
		return
	}
	code, duplicate, e := s.DB.RegisterTestForm(r.Context(), id, f)
	if errors.Is(e, store.ErrConflict) {
		write(w, 409, map[string]string{"error": "idempotency_conflict"})
		return
	}
	if errors.Is(e, store.ErrTestFormLimit) {
		w.Header().Set("Retry-After", "3600")
		write(w, 429, map[string]string{"error": "test_form_rate_limit"})
		return
	}
	if e != nil {
		write(w, 503, map[string]string{"error": "storage_unavailable"})
		return
	}
	link := "https://wa.me/" + strings.TrimPrefix(s.VoiceSender, "+") + "?text=" + url.QueryEscape("Oi, quero testar a Bia. ARKOM-"+code)
	if s.FormAutoContact {
		eventID, err := s.DB.ActivateTestForm(r.Context(), id, s.Mode, s.VoiceSender)
		if errors.Is(err, store.ErrConflict) {
			write(w, 409, map[string]string{"error": "expired_or_conflicting_submission"})
			return
		}
		if err != nil {
			write(w, 503, map[string]string{"error": "storage_unavailable"})
			return
		}
		status, message := "contact_queued", "Formulário recebido. A Bia vai iniciar o contato no seu WhatsApp; você não precisa enviar uma mensagem primeiro."
		if s.Mode == "shadow" {
			status, message = "queued_for_simulation", "Formulário recebido para simulação. Nenhuma mensagem real será enviada."
		}
		write(w, 202, map[string]any{"status": status, "submission_id": id, "event_id": eventID, "duplicate": duplicate, "requires_whatsapp_action": false, "whatsapp_url": link, "message": message})
		return
	}
	write(w, 202, map[string]any{"status": "awaiting_whatsapp", "submission_id": id, "duplicate": duplicate, "whatsapp_url": link, "message": "Formulário recebido. Abra o WhatsApp e envie a mensagem pronta usando o número informado. A Bia receberá os dados do formulário.", "link_validity": "24 horas a partir da primeira submissão"})
}
