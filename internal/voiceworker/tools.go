package voiceworker

import (
	"arkom/bia/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type Agenda interface {
	Lookup(context.Context, string) (domain.MeetingContext, error)
	Note(context.Context, string, string, string) (string, error)
}
type Tools struct {
	Repo   Repository
	Agenda Agenda
	Policy domain.Policy
}

func (t Tools) Execute(ctx context.Context, r Request, name, callID, args string) any {
	if _, e := t.Repo.Active(ctx, r); e != nil {
		return map[string]string{"error": "call_no_longer_authorized"}
	}
	switch name {
	case "get_meeting":
		if t.Agenda == nil {
			return map[string]string{"error": "agenda_unavailable"}
		}
		m, e := t.Agenda.Lookup(ctx, r.ID)
		if e != nil {
			return map[string]string{"error": "agenda_unavailable", "instruction": "Não confirme nenhum dado de agenda. Encerre sem inventar informações."}
		}
		return m
	case "record_reminder_note":
		var a struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Text) == "" || len(a.Text) > 1500 {
			return map[string]string{"error": "invalid_note"}
		}
		switch a.Kind {
		case "confirmed", "reschedule_request", "cancel_request", "question", "wrong_number":
		default:
			return map[string]string{"error": "invalid_note_kind"}
		}
		if t.Agenda == nil {
			return map[string]string{"error": "agenda_unavailable"}
		}
		status, e := t.Agenda.Note(ctx, r.ID, callID, a.Kind+": "+a.Text)
		if e != nil || status != "recorded" {
			return map[string]string{"error": "note_not_recorded", "instruction": "Não diga que anotou, avisou alguém ou alterou a reunião."}
		}
		return map[string]string{"status": "recorded", "instruction": "Nota registrada para consulta da equipe. A reserva não foi alterada. Não prometa contato ou notificação automática."}
	default:
		// In particular, former list_slots/book_slot are no longer executable.
		return map[string]string{"error": "tool_not_allowed_in_reminder_call"}
	}
}
func toolDefinitions() []any {
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	return []any{
		map[string]any{"type": "function", "name": "get_meeting", "description": "Consulta os dados atuais da reunião desta ligação no worker de agenda. Não recebe nem permite escolher outro cliente.", "parameters": empty},
		map[string]any{"type": "function", "name": "record_reminder_note", "description": "Registra confirmação verbal, preferência, pedido de cancelamento ou dúvida para a equipe. Não altera nem cancela a reserva e não envia notificações.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"confirmed", "reschedule_request", "cancel_request", "question", "wrong_number"}}, "text": map[string]any{"type": "string", "description": "Nota fiel à fala do cliente, sem inventar intenção."}}, "required": []string{"kind", "text"}, "additionalProperties": false}},
		map[string]any{"type": "function", "name": "end_call", "description": "Encerra depois da despedida e da reprodução do áudio. Chame na mesma resposta da despedida.", "parameters": empty},
		map[string]any{"type": "function", "name": "voicemail_detection", "description": "Detectou caixa postal: o sistema gera um único lembrete curto e encerra depois da reprodução.", "parameters": empty},
	}
}

var errSession = errors.New("voice_session_error")
