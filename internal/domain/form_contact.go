package domain

import (
	"fmt"
	"strings"
	"time"
)

// These patterns are mirrored in approved WhatsApp content assets. Values are
// generated only from the current profile and confirmed booking, never the LLM.
const FormContactText = "Oi, {{1}}! Sou a Bia, assistente de IA da Arkom. Recebi seu cadastro para conversar com nosso time sobre como aplicar IA no seu negócio.\n\nVamos escolher um horário para essa conversa?"
const BookedContactText = "Oi, {{1}}! Sou a Bia, assistente de IA da Arkom. Sua reunião com nosso time está confirmada para {{2}}, no fuso {{3}}, com duração de {{4}} minutos.\n\nNa conversa, vamos entender seu negócio e avaliar onde a IA pode ajudar. Seu objetivo informado foi: {{5}}. Esse será o ponto de partida da reunião.\n\nSe tiver alguma dúvida antes da conversa, pode me chamar por aqui."
const BookedQuestionText = "Oi, {{1}}! Sou a Bia, assistente de IA da Arkom. Sua reunião com nosso time está confirmada para {{2}}, no fuso {{3}}, com duração de {{4}} minutos.\n\nNa conversa, vamos entender seu negócio e avaliar onde a IA pode ajudar.\n\nPara preparar a conversa: o que mais consome seu tempo hoje no negócio, atendimento, vendas ou operação?"

type Opening struct {
	Kind, Text string
	Variables  map[string]string
}

func ContactOpening(s State, p Policy, now time.Time) Opening {
	clean := func(v string, limit int) string {
		v = strings.Join(strings.Fields(v), " ")
		r := []rune(v)
		if len(r) > limit {
			v = string(r[:limit]) + "…"
		}
		return v
	}
	name := clean(firstName(s.Profile.Name), 60)
	if name == "" {
		name = "tudo bem"
	}
	out := Opening{Kind: "unbooked", Variables: map[string]string{"1": name}}
	pattern := FormContactText
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.Start.After(now) && s.Booking.End.After(s.Booking.Start) {
		b := s.Booking
		out.Kind = "booked_question"
		pattern = BookedQuestionText
		out.Variables["2"] = b.Start.In(p.Zone(s)).Format("02/01/2006 às 15:04")
		out.Variables["3"] = p.Zone(s).String()
		out.Variables["4"] = fmt.Sprint(int(b.End.Sub(b.Start).Minutes()))
		if goal := strings.TrimRight(clean(s.Profile.Goal, 220), ".!?"); goal != "" {
			out.Kind = "booked"
			pattern = BookedContactText
			out.Variables["5"] = goal
		}
	}
	// Replace only template placeholders, never placeholders inside user data.
	pairs := []string{}
	for k, v := range out.Variables {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	out.Text = strings.NewReplacer(pairs...).Replace(pattern)
	return out
}

const PreparationText = "Oi, {{1}}! Uma dica para aproveitar melhor sua reunião com o time da Arkom: separe um exemplo de tarefa que toma tempo no dia a dia do negócio.\n\nPode ser uma conversa de atendimento, uma etapa de vendas ou uma rotina da operação. Não precisa montar apresentação; esse exemplo ajuda a deixar a conversa mais prática."

func PreparationOpening(s State) Opening {
	name := firstName(s.Profile.Name)
	if name == "" {
		name = "tudo bem"
	}
	name = strings.Join(strings.Fields(name), " ")
	r := []rune(name)
	if len(r) > 60 {
		name = string(r[:60])
	}
	return Opening{Kind: "preparation", Text: strings.ReplaceAll(PreparationText, "{{1}}", name), Variables: map[string]string{"1": name}}
}
