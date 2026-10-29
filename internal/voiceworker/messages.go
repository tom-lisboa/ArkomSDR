package voiceworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// A single Twilio callback URL routes native call permission and regular text.
// The signature is checked against this exact URL, without proxy rewriting.
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	if !s.verifiedSignature(w, r) {
		return
	}
	f := r.PostForm
	phone, e := domain.NormalizePhone(strings.TrimPrefix(f.Get("From"), "whatsapp:"))
	if e != nil || !strings.HasPrefix(f.Get("From"), "whatsapp:") || f.Get("To") != "whatsapp:"+s.Sender {
		http.Error(w, "invalid sender or recipient", 400)
		return
	}
	if s.TestPhone != "" && phone != s.TestPhone {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte("<Response/>"))
		return
	}
	if f.Get("Body") == "VOICE_CALL_REQUEST" {
		s.permission(w, r)
		return
	}
	if s.Messages == nil {
		http.Error(w, "messaging disabled", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	m, e := s.Messages.Fetch(ctx, f.Get("MessageSid"))
	if e != nil {
		http.Error(w, "message verification unavailable", 503)
		return
	}
	if m.SID != f.Get("MessageSid") || m.From != f.Get("From") || m.To != f.Get("To") || (m.Status != "receiving" && m.Body != f.Get("Body")) || (m.Status != "received" && m.Status != "receiving") {
		slog.Warn("Twilio inbound mismatch", "sid_match", m.SID == f.Get("MessageSid"), "from_match", m.From == f.Get("From"), "to_match", m.To == f.Get("To"), "body_match", m.Body == f.Get("Body"), "provider_status", m.Status)
		http.Error(w, "message mismatch", 400)
		return
	}
	at, e := providers.MessageCreated(m)
	if e != nil || at.After(time.Now().Add(time.Minute)) {
		http.Error(w, "invalid message timestamp", 400)
		return
	}
	// Old webhook deliveries may be persisted, but cannot open a fresh session.
	text := f.Get("Body")
	attachments, err := s.Messages.WebhookAttachments(ctx, m, f)
	if err != nil {
		http.Error(w, "media verification unavailable", 503)
		return
	}
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		text = "[Mensagem sem texto. Pergunte como pode ajudar.]"
	}
	name := strings.TrimSpace(f.Get("ProfileName"))
	if name == "" || len(name) > 120 {
		name = "Contato"
	}
	conv := "twilio:" + s.Sender + ":" + phone
	profile := domain.Form{Name: name, Phone: phone, Channel: "gateway", ConversationID: conv, Consent: true, ConsentTextVersion: "whatsapp_inbound_service_v1", TimeZone: s.TimeZone, Source: "twilio_whatsapp"}
	event := domain.Event{ID: "twilio-" + m.SID, LeadID: domain.LeadKey(phone), Kind: "message.received", At: at, Text: text, ConversationID: conv, Attachments: attachments}
	// ButtonPayload is authenticated by the Twilio signature and revalidated
	// against the current booking/offer by the execution worker.
	if payload := f.Get("ButtonPayload"); payload != "" {
		if len(payload) > 200 {
			http.Error(w, "invalid button payload", 400)
			return
		}
		event.ButtonPayload = payload
	}
	// A form-created lead may store the other Brazilian spelling of this mobile.
	// The signed provider message continues that same conversation and ownership.
	existing, lookupErr := s.Repo.DB.WhatsAppContact(ctx, phone)
	if lookupErr == nil {
		profile.Phone = existing.Profile.Phone
		if existing.Profile.ConversationID != "" {
			profile.ConversationID = existing.Profile.ConversationID
		}
		event.LeadID = existing.ID
		event.ConversationID = profile.ConversationID
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		http.Error(w, "contact lookup unavailable", 503)
		return
	}
	if e = domain.ValidateEvent(event); e != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if _, e = s.Repo.DB.SubmitWhatsAppInbound(ctx, event, s.Mode, profile); e != nil {
		if errors.Is(e, store.ErrConflict) {
			http.Error(w, "conflicting message", 409)
		} else {
			http.Error(w, "storage unavailable", 503)
		}
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte("<Response/>"))
}
