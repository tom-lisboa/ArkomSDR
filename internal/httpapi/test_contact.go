package httpapi

import (
	"arkom/bia/internal/domain"
	"net/http"
	"time"
)

type testIdentity struct {
	Phone        string `json:"phone"`
	SubmissionID string `json:"submission_id"`
	SMSConsent   *bool  `json:"sms_consent,omitempty"`
}

func readTestIdentity(w http.ResponseWriter, r *http.Request) (testIdentity, bool) {
	var in testIdentity
	if !decode(w, r, &in) {
		return in, false
	}
	phone, err := domain.NormalizePhone(in.Phone)
	if err != nil || !domain.ValidID(in.SubmissionID) || len(in.SubmissionID) > 100 {
		testValidationError(w, r, "valid_phone_and_submission_required", "phone", "submission_id")
		return in, false
	}
	in.Phone = phone
	return in, true
}
func (s *Server) testSMSConsent(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	in, ok := readTestIdentity(w, r)
	if !ok {
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if in.SMSConsent == nil || !domain.ValidID(id) || len(id) > 100 {
		testValidationError(w, r, "explicit_sms_consent_and_idempotency_key_required", "sms_consent", "Idempotency-Key")
		return
	}
	ev, duplicate, err := s.DB.QueueSMSConsent(r.Context(), id, in.SubmissionID, in.Phone, *in.SMSConsent, s.Mode)
	if calendarError(w, err) {
		return
	}
	write(w, 202, map[string]any{"status": "consent_update_queued", "event_id": ev, "duplicate": duplicate, "message": "Preferência de SMS recebida. Consulte /test-status até sms_consent_pending ficar false antes de testar o envio."})
}
func (s *Server) testContactStatus(w http.ResponseWriter, r *http.Request) {
	if !s.testAccess(w, r) {
		return
	}
	in, ok := readTestIdentity(w, r)
	if !ok {
		return
	}
	st, err := s.DB.TestContact(r.Context(), in.SubmissionID, in.Phone)
	if calendarError(w, err) {
		return
	}
	var pending bool
	err = s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bia_go.jobs WHERE lead_id=$1 AND kind='event' AND payload->>'kind'='contact.sms_consent' AND status IN ('pending','running'))`, st.ID).Scan(&pending)
	if calendarError(w, err) {
		return
	}
	upcoming := st.Booking != nil && st.Booking.Outcome == "" && st.Booking.Start.After(time.Now())
	write(w, 200, map[string]any{"booking": st.Booking, "has_future_booking": upcoming, "sms_consent": st.Profile.SMSConsent, "sms_consent_pending": pending, "sms_phone_valid": domain.SMSDestination(st.Profile) != "", "sms_configured": s.SMSEnabled, "audio_ready": s.ReminderAudio.Ready(), "call_configured": s.VoiceEnabled, "contact_blocked": st.Owner != "go" || st.OptedOut || st.Human || st.Paused || !st.Profile.Consent})
}

func writeActionError(w http.ResponseWriter, status int, code string) {
	message := "Não foi possível concluir esta ação."
	next := "check_details"
	retry := false
	switch code {
	case "sms_consent_required":
		message = "Este contato ainda não autorizou lembretes por SMS."
		next = "request_sms_consent"
	case "sms_phone_required":
		message = "Informe o celular completo, com DDI, DDD e nono dígito, para receber SMS."
		next = "correct_phone"
	case "contact_update_pending":
		message = "A autorização de SMS está sendo atualizada. Aguarde alguns segundos."
		next = "retry_same_request"
		retry = true
	case "contact_busy":
		message = "A Bia está processando uma interação. Aguarde alguns segundos e tente novamente com o mesmo horário."
		next = "retry_same_request"
		retry = true
	case "reminder_audio_not_ready":
		message = "O áudio de lembrete ainda não foi instalado."
		next = "wait_for_audio_setup"
	case "future_booking_required":
		message = "Agende uma reunião futura antes de testar este lembrete."
		next = "book_meeting"
	case "lead_already_booked":
		message = "Este contato já tem uma reunião marcada. Consulte a reserva antes de tentar agendar novamente."
		next = "show_current_booking"
	case "slot_no_longer_available":
		message = "Esse horário não está mais disponível. Escolha outra vaga."
		next = "reload_slots"
	case "whatsapp_reply_required":
		message = "Envie uma mensagem para a Bia no WhatsApp antes de testar áudio ou ligação."
		next = "open_whatsapp"
	case "voice_request_in_progress":
		message = "Já existe uma solicitação de ligação em andamento. Confira o WhatsApp."
		next = "check_whatsapp_permission"
	case "permission_request_limit":
		message = "O limite de solicitações de permissão de ligação foi atingido. Aguarde para solicitar novamente."
		next = "wait_for_permission_limit"
	case "test_trigger_rate_limit":
		message = "Já existe um disparo recente ou pendente. Consulte o resultado antes de repetir."
		next = "check_trigger_status"
	case "idempotency_conflict":
		message = "Esta chave já foi usada com outros dados. Preserve a chave nas repetições e use outra apenas para uma nova operação."
		next = "check_request_identity"
	case "contact_not_eligible", "contact_blocked":
		message = "O contato está pausado, sem autorização ou em atendimento humano."
		next = "review_contact"
	}
	if retry {
		w.Header().Set("Retry-After", "3")
	}
	write(w, status, map[string]any{"error": code, "message": message, "retryable": retry, "next_action": next})
}
