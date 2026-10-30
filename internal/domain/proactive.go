package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// The exact bodies also live in deploy/cadence for WhatsApp approval. Never send
// arbitrary commercial copy using a utility reminder template.
var ProactivePatterns = map[string]string{
	"no_reply_15m":             "Oi, {{1}}! conseguiu ver minha mensagem? Posso te mostrar os horários para conversar com o time da Arkom.\n\nToque em Ver horários ou me diga sua dúvida.",
	"no_reply_2h":              "Oi, {{1}}! a conversa com o time serve para entender seu negócio e avaliar onde a IA pode ajudar. Sem precisar decidir nada agora.\n\nQuer escolher um horário ou tirar uma dúvida primeiro?",
	"no_reply_6h":              "Oi, {{1}}! se ficou alguma dúvida sobre a conversa com o time da Arkom, posso explicar como funciona antes de você escolher um horário.\n\nPrefere ver os horários ou entender a proposta primeiro?",
	"no_reply_24h":             "Oi, {{1}}! retomando seu cadastro na Arkom: ainda faz sentido avaliar como aplicar IA no seu negócio?\n\nPosso consultar os horários por aqui. Se não for o momento, toque em Agora não.",
	"no_reply_48h":             "Oi, {{1}}! ainda podemos conversar sobre o que você quer melhorar no seu negócio. Quer retomar seu cadastro na Arkom?\n\nEscolha Quero conversar para ver os horários, ou me conte o que está te deixando em dúvida.",
	"no_reply_72h":             "Oi, {{1}}! vou encerrar esta sequência para não ficar insistindo. Se quiser conversar com o time da Arkom, posso te mostrar os horários.\n\nToque abaixo para seguir ou pausar o contato.",
	"meeting_checkin_24h":      "Oi, {{1}}! lembrete da sua reunião com a Arkom: {{2}}, com duração de {{3}} minutos.\n\nVocê mantém esse horário? Confirme sua presença ou escolha remarcar.",
	"meeting_checkin_2h":       "Oi, {{1}}! sua reunião com a Arkom é hoje: {{2}}. Reserve {{3}} minutos para essa conversa.\n\nConsegue participar nesse horário? Confirme por aqui ou me avise se precisar remarcar.",
	"meeting_checkin_1h":       "Oi, {{1}}! falta uma hora para sua reunião com a Arkom: {{2}}, com duração de {{3}} minutos.\n\nVocê mantém esse horário? Confirme sua presença ou toque abaixo se precisar remarcar ou de ajuda.",
	"meeting_checkin_5m":       "Oi, {{1}}! sua reunião com a Arkom começa em poucos minutos: {{2}}. A duração é de {{3}} minutos.\n\nVai conseguir participar? Confirme abaixo. Se precisar, posso ajudar ou remarcar.",
	"meeting_checkin_7m_after": "Oi, {{1}}! o horário da sua reunião com a Arkom já começou: {{2}}. A duração prevista é de {{3}} minutos.\n\nSe ainda precisar de ajuda para participar, toque abaixo. Se teve um imprevisto, podemos remarcar.",
}

func IsNoReply(purpose string) bool {
	return strings.HasPrefix(purpose, "no_reply_") && ProactivePatterns[purpose] != ""
}
func IsMeetingCheckin(purpose string) bool {
	return strings.HasPrefix(purpose, "meeting_checkin_") && ProactivePatterns[purpose] != ""
}
func IsProactive(purpose string) bool { return IsNoReply(purpose) || IsMeetingCheckin(purpose) }
func NoReplyAnchor(s State) time.Time {
	if s.LastFormContactAt != nil {
		return *s.LastFormContactAt
	}
	if s.FirstContactAt != nil {
		return *s.FirstContactAt
	}
	return time.Time{}
}
func ProactiveOpening(s State, p Policy, purpose string) Opening {
	pattern := ProactivePatterns[purpose]
	if pattern == "" {
		return Opening{}
	}
	name := strings.Join(strings.Fields(CustomerMessage(firstName(s.Profile.Name))), " ")
	if name == "" {
		name = "tudo bem"
	}
	r := []rune(name)
	if len(r) > 60 {
		name = string(r[:60])
	}
	vars := map[string]string{"1": name}
	if IsMeetingCheckin(purpose) {
		if s.Booking == nil || !s.Booking.End.After(s.Booking.Start) {
			return Opening{}
		}
		vars["2"] = s.Booking.Start.In(p.Zone(s)).Format("02/01 às 15:04")
		vars["3"] = fmt.Sprint(int(s.Booking.End.Sub(s.Booking.Start).Minutes()))
	}
	pairs := []string{}
	for k, v := range vars {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return Opening{Kind: purpose, Text: strings.NewReplacer(pairs...).Replace(pattern), Variables: vars}
}
func NoReplySequence(s State, p Policy, now time.Time) []Effect {
	a := NoReplyAnchor(s)
	if !p.NoReplyEnabled || a.IsZero() || p.NoReplySince.IsZero() || a.Before(p.NoReplySince) || s.Booking != nil || !s.LastInbound.Before(a) {
		return nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+a.UTC().Format(time.RFC3339Nano))))
	var out []Effect
	var previous time.Time
	for _, step := range []struct {
		purpose      string
		after, grace time.Duration
	}{
		{"no_reply_15m", 15 * time.Minute, 45 * time.Minute}, {"no_reply_2h", 2 * time.Hour, 2 * time.Hour}, {"no_reply_6h", 6 * time.Hour, 2 * time.Hour}, {"no_reply_24h", 24 * time.Hour, 12 * time.Hour}, {"no_reply_48h", 48 * time.Hour, 12 * time.Hour}, {"no_reply_72h", 72 * time.Hour, 12 * time.Hour},
	} {
		due := p.NextContact(a.Add(step.after), s)
		if !previous.IsZero() && due.Before(previous.Add(90*time.Minute)) {
			due = p.NextContact(previous.Add(90*time.Minute), s)
		}
		previous = due
		expires := due.Add(step.grace)
		if !now.Before(expires) {
			continue
		}
		out = append(out, Effect{ID: "fx/noreply:" + key + ":" + step.purpose, LeadID: s.ID, Kind: "send", Purpose: step.purpose, Due: due, Expires: expires, CadenceAnchor: a})
	}
	return out
}
func MeetingCheckinSequence(s State, p Policy, now time.Time) []Effect {
	if !p.MeetingCheckins || s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.End.After(s.Booking.Start) {
		return nil
	}
	b := s.Booking
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID+"\x00"+b.ID+"\x00"+b.Start.UTC().Format(time.RFC3339Nano))))
	var out []Effect
	for _, step := range []struct {
		purpose       string
		offset, grace time.Duration
	}{
		{"meeting_checkin_24h", -24 * time.Hour, 30 * time.Minute}, {"meeting_checkin_2h", -2 * time.Hour, 15 * time.Minute}, {"meeting_checkin_1h", -time.Hour, 10 * time.Minute}, {"meeting_checkin_5m", -5 * time.Minute, 4 * time.Minute}, {"meeting_checkin_7m_after", 7 * time.Minute, 5 * time.Minute},
	} {
		due := b.Start.Add(step.offset)
		expires := due.Add(step.grace)
		if b.End.Before(expires) {
			expires = b.End
		}
		// Do not stack a reminder on top of a freshly created booking/opening.
		anchor := s.LastBookingEventAt
		if a := NoReplyAnchor(s); a.After(anchor) {
			anchor = a
		}
		if step.offset < 0 && !anchor.IsZero() && due.Sub(anchor) < 15*time.Minute {
			continue
		}
		if !now.Before(expires) {
			continue
		}
		out = append(out, Effect{ID: "fx/checkin:" + key + ":" + step.purpose, LeadID: s.ID, Kind: "send", Purpose: step.purpose, Due: due, Expires: expires, BookingID: b.ID, BookingStart: b.Start})
	}
	return out
}

func proactiveDeliverable(s State, fx Effect, now time.Time) (bool, string) {
	if !s.Profile.Consent || s.Paused {
		return false, "proactive_contact_blocked"
	}
	if now.Before(fx.Due) {
		return false, "proactive_not_due"
	}
	if IsNoReply(fx.Purpose) {
		a := NoReplyAnchor(s)
		if s.Booking != nil || fx.CadenceAnchor.IsZero() || !a.Equal(fx.CadenceAnchor) || !s.LastInbound.Before(a) {
			return false, "no_reply_superseded"
		}
		return true, ""
	}
	if fx.Purpose == "meeting_checkin_7m_after" {
		if s.Booking == nil || s.Booking.Outcome != "" || !now.Before(s.Booking.End) || now.Before(s.Booking.Start.Add(7*time.Minute)) {
			return false, "meeting_not_in_progress"
		}
		cutoff := s.Booking.Start.Add(-15 * time.Minute)
		if !s.LastInbound.Before(cutoff) || s.Booking.ConfirmedAt != nil && !s.Booking.ConfirmedAt.Before(cutoff) {
			return false, "recent_meeting_response"
		}
		return true, ""
	}
	if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) {
		return false, "meeting_not_upcoming"
	}
	return true, ""
}
