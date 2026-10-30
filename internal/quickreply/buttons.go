// Package quickreply binds WhatsApp buttons to the contact and the current
// booking/offer. Clicking an old card can never select a newer offer by position.
package quickreply

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

type Signer struct{ Key string }
type Button struct{ Title, Payload string }

func binding(s domain.State, action string) string {
	value := s.ID + "\x00" + s.CreatedAt.UTC().Format(time.RFC3339Nano)
	if s.Booking != nil {
		value += "\x00" + s.Booking.ID + "\x00" + s.Booking.Start.UTC().Format(time.RFC3339Nano)
	}
	if action == "first" || action == "second" {
		value += "\x00" + s.OfferVersion
		for _, slot := range s.Offered {
			value += "\x00" + slot.ID + "\x00" + slot.Start.UTC().Format(time.RFC3339Nano)
		}
	}
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}
func (k Signer) mac(value string) string {
	h := hmac.New(sha256.New, []byte(k.Key))
	h.Write([]byte("arkom-whatsapp-button-v1\x00" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}
func (k Signer) Button(s domain.State, action, title string, now time.Time) Button {
	if len(k.Key) < 32 {
		return Button{}
	}
	expiry := now.Add(24 * time.Hour)
	if action == "first" || action == "second" {
		expiry = now.Add(20 * time.Minute)
	}
	raw := "bia1." + action + "." + strconv.FormatInt(expiry.Unix(), 36) + "." + binding(s, action)
	return Button{Title: title, Payload: raw + "." + k.mac(raw)}
}
func (k Signer) action(s domain.State, token string, now time.Time) string {
	parts := strings.Split(token, ".")
	if len(k.Key) < 32 || len(token) > 200 || len(parts) != 5 || parts[0] != "bia1" {
		return ""
	}
	expiry, err := strconv.ParseInt(parts[2], 36, 64)
	if err != nil || now.Unix() >= expiry || expiry > now.Add(25*time.Hour).Unix() {
		return ""
	}
	if !hmac.Equal([]byte(parts[3]), []byte(binding(s, parts[1]))) || !hmac.Equal([]byte(parts[4]), []byte(k.mac(strings.Join(parts[:4], ".")))) {
		return ""
	}
	return parts[1]
}

// Resolve runs under the contact lease, before any LLM. Tokens never reach the
// model. Mixed clicks/text require a fresh explicit choice, not a guessed action.
func (k Signer) Resolve(s domain.State, e *domain.Event, p domain.Policy, now time.Time) (domain.Decision, bool) {
	if e.ButtonPayload == "" {
		return domain.Decision{}, false
	}
	token := e.ButtonPayload
	e.ButtonPayload = ""
	// An opt-out always wins, including a text fragment batched with a click.
	for _, line := range strings.Split(e.Text, "\n") {
		if domain.IsOptOut(line) {
			e.Text = line
			e.SelectedSlotID = ""
			return domain.Decision{}, false
		}
	}
	action := k.action(s, token, now)
	if e.Context != nil && (len(e.Context.MessageIDs) != 1 || e.Context.MediaFailed) {
		action = ""
	}
	if action == "" {
		e.Text = "Usei um botão antigo ou enviei mais de uma escolha."
		e.SelectedSlotID = ""
		return domain.Decision{Action: "reply", Message: "Vou atualizar as opções por aqui. Toque em um dos botões abaixo para continuar.", Reason: "quick_reply_stale"}, true
	}
	d := domain.Decision{Reason: "quick_reply_" + action}
	switch action {
	case "schedule", "more":
		e.Text = "Quero ver horários para agendar minha reunião."
		d.Action = "offer_slots"
	case "reschedule":
		e.Text = "Quero remarcar minha reunião."
		d.Action = "offer_slots"
	case "confirm":
		e.Text = "CONFIRMO"
		d.Action = "confirm"
	case "first", "second":
		index := 0
		if action == "second" {
			index = 1
		}
		if len(s.Offered) <= index || !s.Offered[index].Start.After(now.Add(30*time.Minute)) {
			e.Text = "Quero consultar novos horários."
			d.Action = "offer_slots"
			break
		}
		slot := s.Offered[index]
		e.Text = "Escolho " + domain.SlotLabel(slot, p.Zone(s))
		d.Action, d.SlotID = "book", slot.ID
		if s.Booking != nil {
			d.Action = "reschedule"
		}
	case "help":
		e.Text = "Tenho uma dúvida sobre a reunião."
		d.Action, d.Message = "reply", "Claro! Me diga sua dúvida para eu te ajudar."
	case "not_now":
		e.Text = "Agora não. Quero pausar os contatos."
		d.Action = "pause"
	case "about":
		e.Text = "Como funciona a reunião?"
		d.Action, d.Message = "reply", "É uma conversa com o time para entender seu negócio e avaliar onde a IA pode ajudar. Vamos escolher um horário?"
	case "service", "sales", "operations":
		topic := map[string]string{"service": "atendimento", "sales": "vendas", "operations": "operação"}[action]
		e.Text = "Quero priorizar " + topic + " na reunião."
		return domain.Decision{}, false
	default:
		e.Text = "Quero atualizar as opções da conversa."
		d.Action, d.Message = "reply", "Toque em um dos botões abaixo para continuar."
	}
	return d, true
}

// Options adds a CTA at a meaningful decision point. Text and audio replies
// remain valid; only the final bubble of a split reply should receive buttons.
func (k Signer) Options(s domain.State, text, purpose string, p domain.Policy, now time.Time) []Button {
	makeButtons := func(items ...string) []Button {
		out := []Button{}
		for i := 0; i < len(items); i += 2 {
			b := k.Button(s, items[i], items[i+1], now)
			if b.Payload != "" {
				out = append(out, b)
			}
		}
		return out
	}
	if domain.IsNoReply(purpose) {
		if purpose == "no_reply_6h" {
			return makeButtons("schedule", "Ver horários", "about", "Como funciona", "not_now", "Agora não")
		}
		if purpose == "no_reply_48h" {
			return makeButtons("schedule", "Quero conversar", "help", "Tenho uma dúvida", "not_now", "Agora não")
		}
		return makeButtons("schedule", "Ver horários", "help", "Tirar dúvida", "not_now", "Agora não")
	}
	if domain.IsMeetingCheckin(purpose) {
		return makeButtons("confirm", "Confirmar presença", "reschedule", "Remarcar", "help", "Preciso de ajuda")
	}
	if purpose == "form_context" || purpose == "meeting_preparation" {
		if purpose == "meeting_preparation" || strings.TrimSpace(s.Profile.Goal) == "" {
			return makeButtons("service", "Atendimento", "sales", "Vendas", "operations", "Operação")
		}
		return makeButtons("confirm", "Confirmar presença", "reschedule", "Remarcar", "help", "Tirar dúvida")
	}
	if purpose == "form_contact" {
		if domain.HasActiveMeeting(s, now) {
			return nil
		} // CTA follows in the short context bubble.
		return makeButtons("schedule", "Ver horários", "help", "Tirar dúvida")
	}
	if purpose == "meeting_call" || purpose == "meeting_start" || purpose == "booking_confirmation" || purpose == "reminder" || purpose == "voice_reminder" {
		if domain.HasActiveMeeting(s, now) {
			return makeButtons("confirm", "Confirmar presença", "reschedule", "Remarcar", "help", "Preciso de ajuda")
		}
	}
	if purpose == "recovery" || purpose == "followup" {
		return makeButtons("schedule", "Ver horários", "help", "Tirar dúvida")
	}
	if purpose != "reply" {
		return nil
	}
	// A numbered button is attached only to a bubble containing every exact slot.
	if s.Stage == "choosing_slot" && len(s.Offered) > 0 {
		all := true
		for _, slot := range s.Offered {
			all = all && strings.Contains(text, domain.SlotLabel(slot, p.Zone(s)))
		}
		if all {
			if len(s.Offered) == 1 {
				return makeButtons("first", "Escolher horário", "more", "Outros horários", "help", "Tirar dúvida")
			}
			return makeButtons("first", "Primeiro horário", "second", "Segundo horário", "more", "Outros horários")
		}
		if strings.Contains(text, "botões abaixo") {
			return makeButtons("schedule", "Ver horários", "help", "Tirar dúvida")
		}
		return nil
	}
	// Avoid reattaching a menu to every sentence of a natural conversation.
	lower := strings.ToLower(text)
	if strings.Contains(text, "?") || strings.Contains(lower, "botões abaixo") || strings.Contains(lower, "presença confirmada") {
		if domain.HasActiveMeeting(s, now) {
			if s.Booking.ConfirmedAt != nil {
				return makeButtons("reschedule", "Remarcar", "help", "Tirar dúvida")
			}
			return makeButtons("confirm", "Confirmar presença", "reschedule", "Remarcar", "help", "Tirar dúvida")
		}
		return makeButtons("schedule", "Ver horários", "about", "Como funciona", "help", "Tirar dúvida")
	}
	return nil
}
