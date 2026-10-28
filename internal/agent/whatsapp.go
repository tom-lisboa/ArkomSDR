package agent

import (
	_ "embed"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

//go:embed whatsapp_prompt.txt
var whatsappPrompt string

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
	return map[string]any{
		"customer": domain.CustomerContext(s, s.Booking, p),
		"profile":  map[string]any{"company": s.Profile.Company, "name": s.Profile.Name, "goal": s.Profile.Goal, "revenue": s.Profile.Revenue, "timezone": zone.String()},
		"history":  history, "booking": s.Booking, "offered": labels(s.Offered), "available_slots": labels(slots),
		"calendar":              map[string]any{"status": status, "timezone": zone.String()},
		"conversation_progress": map[string]any{"previous_user_turns": turns, "goal_already_in_form": strings.TrimSpace(s.Profile.Goal) != "", "stage": s.Stage},
		"current_message":       e.Text, "scheduling_url": schedulingURL, "now": now.In(zone),
	}
}
