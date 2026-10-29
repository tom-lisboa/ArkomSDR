package agent

import (
	_ "embed"
	"regexp"
	"strings"
	"time"
	"unicode"

	"arkom/bia/internal/domain"
)

//go:embed whatsapp_prompt.txt
var whatsappPrompt string

var fillerOpening = regexp.MustCompile(`(?i)^(perfeito|entendi|entendo|ótima pergunta|que ótimo|faz (todo )?sentido|com certeza|fico feliz)([!.,:]+[ \t]*|[ \t]*$)`)
var redundantOfferHeading = regexp.MustCompile(`(?i)^(tenho|separei|posso te passar|se quiser.{0,30}(posso|podemos)).{0,120}hor[aá]rios.{0,60}[.:]?$`)

// These are presentation rules for WhatsApp only. Operational decisions still
// pass through the domain's booking, consent and delivery checks.
func normalizeTextDecision(s domain.State, e domain.Event, d domain.Decision) domain.Decision {
	d.Message = domain.CustomerMessage(d.Message)
	if s.Booking != nil && shortAcknowledgment(e.Text) {
		last := ""
		for _, h := range s.History {
			if h.Role == "assistant" {
				last = h.Text
			}
		}
		if e.Context != nil {
			last = ""
			for _, h := range e.Context.History {
				if h.Role == "assistant" {
					last = h.Text
				}
			}
		}
		// An acknowledgment to a statement is not a new presence confirmation.
		// If the assistant asked a question, preserve the planner's interpretation.
		if last != "" && !strings.Contains(last, "?") {
			return domain.Decision{Action: "reply", Message: "Combinado.", Reason: "booking_acknowledgment"}
		}
	}
	d.Message = strings.ReplaceAll(d.Message, " — ", ", ")
	d.Message = strings.ReplaceAll(d.Message, "—", ", ")
	blocks := strings.Split(d.Message, "\n\n")
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		for {
			clean := strings.TrimSpace(fillerOpening.ReplaceAllString(block, ""))
			if clean == block {
				break
			}
			block = clean
		}
		if block == "" {
			continue
		}
		if d.Action == "offer_slots" && redundantOfferHeading.MatchString(block) {
			continue
		}
		runes := []rune(block)
		runes[0] = unicode.ToUpper(runes[0])
		out = append(out, string(runes))
	}
	d.Message = strings.Join(out, "\n\n")
	if d.Action == "reply" && d.Message == "" {
		d.Message = "Combinado."
	}
	return d
}

func shortAcknowledgment(text string) bool {
	switch strings.Trim(strings.ToLower(strings.TrimSpace(text)), ".! ") {
	case "ok", "okay", "obrigado", "obrigada", "certo", "combinado":
		return true
	}
	return false
}

func textInstructions(brand, agentID string) string {
	return strings.ReplaceAll(whatsappPrompt, "{{brand}}", brand) + "\nEspecialidade: " + agentID + ". " + specialty(agentID)
}

func textContext(s domain.State, e domain.Event, slots []domain.Slot, schedulingURL string, now time.Time) map[string]any {
	p := domain.Policy{TimeZone: "America/Cuiaba"}
	zone := p.Zone(s)
	var history any = s.History
	turns := 0
	for _, h := range s.History {
		if h.Role == "user" {
			turns++
		}
	}
	if e.Context != nil {
		history = e.Context.History
		turns = 0
		for _, h := range e.Context.History {
			if h.Role == "user" {
				turns++
			}
		}
	}
	labels := func(list []domain.Slot) []map[string]any {
		out := make([]map[string]any, 0, len(list))
		for _, slot := range list {
			out = append(out, map[string]any{"id": slot.ID, "start": slot.Start, "end": slot.End, "label": domain.SlotLabel(slot, zone), "duration_minutes": int(slot.End.Sub(slot.Start).Minutes())})
		}
		return out
	}
	status := "available"
	if len(slots) == 0 {
		status = "empty"
	}
	if e.CalendarUnavailable {
		status = "unavailable"
	}
	meetingStatus := domain.MeetingStatus(s, now)
	return map[string]any{
		"dialogue":        s.Dialogue,
		"allowed_actions": dialogueAllowedActions(s),
		"response_goal":   dialogueResponseGoal(s),
		"meeting_status":  meetingStatus,
		"meeting_results": s.MeetingResults,
		"customer":        domain.CustomerContext(s, s.Booking, p),
		"profile":         map[string]any{"company": s.Profile.Company, "name": s.Profile.Name, "goal": s.Profile.Goal, "revenue": s.Profile.Revenue, "timezone": zone.String()},
		"history":         history, "booking": s.Booking, "offered": labels(s.Offered), "available_slots": labels(slots),
		"calendar":              map[string]any{"status": status, "timezone": zone.String()},
		"conversation_progress": map[string]any{"previous_user_turns": turns, "goal_already_in_form": strings.TrimSpace(s.Profile.Goal) != "", "stage": s.Stage},
		"current_message":       e.Text, "scheduling_url": schedulingURL, "now": now.In(zone),
	}
}

func dialogueResponseGoal(s domain.State) string {
	if s.Dialogue == nil || s.Dialogue.Agent != "objections" {
		return "Responder à fala atual e executar apenas o próximo passo autorizado."
	}
	if s.Dialogue.BookingCondition != "" {
		return "Condição de reserva ainda NÃO confirmada: esclareça-a; não sugira seguir com o horário enquanto depender dessa condição. Não invente gratuidade ou outra condição comercial."
	}
	o := s.Dialogue.Objection
	if o == nil || o.Clarification == "" {
		return "Identificar o motivo ainda desconhecido da objeção com uma pergunta curta, aproveitando o histórico."
	}
	if len(o.Attempts) >= 2 {
		return "Já houve duas tentativas. Responda apenas a uma dúvida nova concreta ou respeite o retorno/recusa; não repita pressão ou convite."
	}
	return "O motivo JÁ foi explicado em dialogue.objection.clarification. Responda diretamente a esse motivo e, se houver abertura, faça uma pergunta curta para verificar se quer avaliar isso na reunião. Não pergunte novamente o que fez sentir que não se encaixa, nem repita pergunta equivalente."
}

func dialogueAllowedActions(s domain.State) []string {
	if s.Dialogue == nil {
		return []string{"reply", "offer_slots", "book", "reschedule", "cancel", "confirm", "pause", "opt_out", "handoff"}
	}
	return specialistActions(s.Dialogue.Agent, s.Dialogue.Assessment)
}
