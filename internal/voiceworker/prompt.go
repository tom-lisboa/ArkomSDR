package voiceworker

import (
	"arkom/bia/internal/domain"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

//go:embed reminder_prompt.txt
var reminderPrompt string

func reminderInstructions(m domain.MeetingContext, history []map[string]string) string {
	zone, e := time.LoadLocation(m.Customer.TimeZone)
	if e != nil {
		zone = time.UTC
	}
	now := m.Now.In(zone)
	when := ""
	format := m.Briefing.Format
	if m.Booking != nil {
		when = m.Booking.Start.In(zone).Format("02/01/2006 às 15:04")
		if format == "" && m.Booking.MeetingURL != "" {
			format = "Reunião online. Link de acesso registrado: " + m.Booking.MeetingURL
		}
	}
	env := m.Briefing.Environment
	if env == "" {
		env = "silencioso"
	}
	raw, _ := json.Marshal(map[string]any{"cliente": m.Customer, "dados": map[string]any{
		"customer_name": m.Customer.Name, "empresa": m.Customer.Company, "faturamento": m.Customer.Revenue,
		"hoje_falado": now.Format("02/01/2006"), "reuniao_quando": when, "reuniao_formato": format,
		"reuniao_pauta": m.Briefing.Agenda, "contexto": m.Briefing.Context, "objetivo_formulario": m.Goal,
		"consultor": m.Briefing.Consultant, "consultor_genero": m.Briefing.ConsultantGender, "ambiente": env,
	}, "agenda": m, "historico": history})
	return reminderPrompt + "\n\nLeia datas e horas em português por extenso preservando exatamente o dia e a hora local fornecidos. O fuso é um dado interno de conversão: não fale códigos como America/Sao_Paulo, UTC ou GMT ao cliente.\nDADOS DA LIGAÇÃO (JSON):\n" + string(raw)
}
func voicemailInstructions(m domain.MeetingContext) string {
	// No company, revenue, briefing, URL or conversation history in voicemail.
	return fmt.Sprintf("Diga apenas: Aqui é a Bia, da Arkom. Ligamos para lembrar da reunião em %s. Obrigada. Fale a data e a hora em português por extenso. Não acrescente perguntas, dados pessoais nem outras informações. Não chame ferramentas.", m.Customer.MeetingDate.Format("02/01/2006 às 15:04"))
}
