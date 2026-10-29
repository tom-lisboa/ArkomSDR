package domain

import (
	"fmt"
	"strings"
	"time"
)

// These patterns are mirrored in approved WhatsApp content assets. Values are
// generated only from the current profile and confirmed booking, never the LLM.
const FormContactText = "Oi, {{1}}! Sou a Bia, IA da Arkom. Recebi seu cadastro.\n\nResponda AGENDAR para eu te mostrar os horários da sua conversa com o time."
const BookedContactText = "Oi, {{1}}! Sou a Bia, IA da Arkom. Sua reunião está confirmada para {{2}}. Separe {{3}} minutos para essa conversa."
const BookedQuestionText = BookedContactText
const BookedContextText = "Na reunião, vamos avaliar onde a IA pode ajudar. Seu foco: {{1}}.\n\nVocê confirma sua presença?"
const BookedContextQuestionText = "Vamos avaliar onde a IA pode ajudar na sua empresa.\n\nQual rotina vamos priorizar nessa reunião: atendimento, vendas ou operação?"

type Opening struct {
	Kind, Text string
	Variables  map[string]string
}

func ContactOpening(s State, p Policy, now time.Time) Opening {
	clean := func(v string, limit int) string {
		v = CustomerMessage(v)
		v = strings.Join(strings.Fields(v), " ")
		r := []rune(v)
		if len(r) > limit {
			v = string(r[:limit]) + "…"
		}
		return v
	}
	name := clean(firstName(s.Profile.Name), 20)
	if name == "" {
		name = "tudo bem"
	}
	out := Opening{Kind: "unbooked", Variables: map[string]string{"1": name}}
	pattern := FormContactText
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.Start.After(now) && s.Booking.End.After(s.Booking.Start) {
		b := s.Booking
		out.Kind = "booked_question"
		pattern = BookedQuestionText
		out.Variables["2"] = b.Start.In(p.Zone(s)).Format("02/01 às 15:04")
		out.Variables["3"] = fmt.Sprint(int(b.End.Sub(b.Start).Minutes()))
		if goal := strings.TrimRight(clean(s.Profile.Goal, 90), ".!?"); goal != "" {
			out.Kind = "booked"
			pattern = BookedContactText
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

// ContactContextOpening is a distinct, approved short bubble. The full form
// objective stays in State; the display excerpt only bounds this opening.
func ContactContextOpening(s State, p Policy, now time.Time) Opening {
	if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) || !s.Booking.End.After(s.Booking.Start) {
		return Opening{}
	}
	o := Opening{Kind: "booked_context_question", Text: BookedContextQuestionText, Variables: map[string]string{}}
	goal := strings.TrimRight(strings.Join(strings.Fields(CustomerMessage(s.Profile.Goal)), " "), ".!?")
	if goal != "" {
		r := []rune(goal)
		if len(r) > 90 {
			goal = string(r[:89]) + "…"
		}
		o.Kind = "booked_context"
		o.Variables["1"] = goal
		o.Text = strings.ReplaceAll(BookedContextText, "{{1}}", goal)
	}
	return o
}

func ContactContextSequence(s State, openingID string, p Policy, now time.Time) []Effect {
	o := ContactContextOpening(s, p, now)
	if openingID == "" || o.Text == "" {
		return nil
	}
	return []Effect{{ID: openingID + ":context", LeadID: s.ID, Kind: "send", Purpose: "form_context", Text: o.Text, AfterMessageID: openingID, Due: now.Add(2 * time.Second), Expires: minTime(now.Add(15*time.Minute), s.Booking.Start), BookingID: s.Booking.ID, BookingStart: s.Booking.Start, TriggerAt: now}}
}

const PreparationText = "Oi, {{1}}! Para aproveitar a reunião, escolha uma tarefa que toma seu tempo no atendimento, nas vendas ou na operação.\n\nMe envie um exemplo para começarmos por ele."

func PreparationOpening(s State) Opening {
	name := CustomerMessage(firstName(s.Profile.Name))
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
