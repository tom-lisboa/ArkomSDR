package agent

import (
	"arkom/bia/internal/domain"
	"context"
	"strings"
)

type Outcome struct {
	Decision     domain.Decision
	ResponseID   string
	InputTokens  int64
	UsedPrevious bool
}
type Runner interface {
	Run(context.Context, domain.State, domain.Event, []domain.Slot, string) (Outcome, error)
}

// Routing uses application state and a closed agent registry, never model-supplied names.
func Select(s domain.State, e domain.Event) string {
	if s.Stage == "no_show" {
		return "recovery"
	}
	t := strings.ToLower(e.Text)
	if s.Booking != nil || e.SelectedSlotID != "" || strings.Contains(t, "agend") || strings.Contains(t, "horário") || strings.Contains(t, "horario") || strings.Contains(t, "reunião") {
		return "scheduling"
	}
	return "sdr"
}
func specialty(id string) string {
	switch id {
	case "scheduling":
		return "Ajude a escolher, confirmar ou remarcar horários reais, explicando o próximo passo."
	case "recovery":
		return "Acolha a ausência sem culpa e facilite uma nova escolha de horário."
	default:
		return "Entenda a necessidade sem repetir o formulário e encaminhe ao próximo passo adequado."
	}
}
func Execute(ctx context.Context, p Planner, s domain.State, e domain.Event, slots []domain.Slot, id string) (Outcome, error) {
	if r, ok := p.(Runner); ok {
		return r.Run(ctx, s, e, slots, id)
	}
	d, err := p.Plan(ctx, s, e, slots)
	return Outcome{Decision: d}, err
}
