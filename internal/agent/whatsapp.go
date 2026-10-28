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

var fillerOpening = regexp.MustCompile(`(?i)^(perfeito|entendi|ótima pergunta|que ótimo|faz todo sentido|com certeza|fico feliz)([!.,: ]+|$)`)

// These are presentation rules for WhatsApp only. Operational decisions still
// pass through the domain's booking, consent and delivery checks.
func normalizeTextDecision(s domain.State, e domain.Event, d domain.Decision) domain.Decision {
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
	meetingStatus := "not_scheduled"
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.Start.After(now) {
		meetingStatus = "upcoming"
	}
	return map[string]any{
		"meeting_status": meetingStatus,
		"customer":       domain.CustomerContext(s, s.Booking, p),
		"profile":        map[string]any{"company": s.Profile.Company, "name": s.Profile.Name, "goal": s.Profile.Goal, "revenue": s.Profile.Revenue, "timezone": zone.String()},
		"history":        history, "booking": s.Booking, "offered": labels(s.Offered), "available_slots": labels(slots),
		"calendar":              map[string]any{"status": status, "timezone": zone.String()},
		"conversation_progress": map[string]any{"previous_user_turns": turns, "goal_already_in_form": strings.TrimSpace(s.Profile.Goal) != "", "stage": s.Stage},
		"current_message":       e.Text, "scheduling_url": schedulingURL, "now": now.In(zone),
	}
}
