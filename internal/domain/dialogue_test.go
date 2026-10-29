package domain

import (
	"testing"
	"time"
)

func TestDialogueMemoryUsesDeliveredArgumentsAndKeepsClarification(t *testing.T) {
	now := time.Now()
	s := State{}
	e := Event{ID: "turn-one", Text: "acho que não é para mim"}
	a := DialogueAssessment{Intent: "objection", Objection: "fit", Readiness: "considering", Progress: "open", Source: "decisions"}
	s.Dialogue = AdvanceDialogue(s, e, a, "objections", "v1", now)
	if len(s.Dialogue.Objection.Attempts) != 0 {
		t.Fatal("draft consumed an attempt")
	}
	fx := Effect{Dialogue: &DialogueDelivery{Agent: "objections", TurnID: e.ID, ObjectionID: e.ID}}
	RecordDialogueDelivery(&s, fx, "O que te fez sentir isso?", now)
	RecordDialogueDelivery(&s, fx, "Podemos esclarecer esse ponto.", now)
	if len(s.Dialogue.Objection.Attempts) != 1 || s.Dialogue.LastQuestion != "O que te fez sentir isso?" {
		t.Fatal(s.Dialogue)
	}
	old := s.Dialogue
	a.Progress = "clarifying"
	s.Dialogue = AdvanceDialogue(s, Event{ID: "turn-two", Text: "minha empresa é pequena"}, a, "objections", "v1", now)
	if s.Dialogue.Objection.Clarification != "minha empresa é pequena" || len(s.Dialogue.Objection.Attempts) != 1 || old.Objection.Clarification != "" {
		t.Fatal("lost evidence or mutated snapshot", s.Dialogue)
	}
	RecordDialogueDelivery(&s, Effect{Dialogue: &DialogueDelivery{Agent: "objections", TurnID: "turn-two", ObjectionID: e.ID}}, "Podemos avaliar seu atendimento na conversa.", now)
	if len(old.Objection.Attempts) != 1 || len(s.Dialogue.Objection.Attempts) != 2 {
		t.Fatal("mutated old attempts")
	}
	a = DialogueAssessment{Intent: "schedule", Objection: "none", Readiness: "ready", Progress: "resolved", Source: "decisions"}
	s.Dialogue = AdvanceDialogue(s, Event{ID: "turn-three", Text: "quais horários?"}, a, "scheduling", "v1", now)
	if s.Dialogue.Objection.Status != "resolved" || len(s.Dialogue.Objection.Attempts) != 2 || s.Dialogue.Objection.Clarification == "" {
		t.Fatal("handoff lost shared memory", s.Dialogue)
	}
}

func TestUnknownObjectionHasBoundedMemoryWithoutInventingCause(t *testing.T) {
	s := State{}
	for i := 0; i < 10; i++ {
		e := Event{ID: time.Now().Add(time.Duration(i) * time.Second).String(), Text: "não sei se quero"}
		a := DialogueAssessment{Intent: "objection", Objection: "unknown", Progress: "open"}
		s.Dialogue = AdvanceDialogue(s, e, a, "objections", "v1", time.Now())
		RecordDialogueDelivery(&s, Effect{Dialogue: &DialogueDelivery{Agent: "objections", TurnID: e.ID, ObjectionID: s.Dialogue.Objection.ID}}, "Dúvida esclarecida em texto.", time.Now())
	}
	if s.Dialogue.Objection.Kind != "unknown" || s.Dialogue.Objection.Clarification != "" || len(s.Dialogue.Objection.Attempts) != 4 {
		t.Fatal(s.Dialogue)
	}
}

func TestConditionalBookingSurvivesSimpleAcceptance(t *testing.T) {
	a := DialogueAssessment{Intent: "clarify", Objection: "price", Cause: "provided", Progress: "open"}
	s := State{}
	s.Dialogue = AdvanceDialogue(s, Event{ID: "condition", Text: "Pode ser o primeiro, se for gratuito"}, a, "objections", "v1", time.Now())
	if s.Dialogue.BookingCondition == "" || s.Dialogue.Objection.Clarification == "" {
		t.Fatal(s.Dialogue)
	}
	s.Dialogue = AdvanceDialogue(s, Event{ID: "yes", Text: "sim"}, a, "objections", "v1", time.Now())
	if s.Dialogue.BookingCondition == "" {
		t.Fatal("simple yes withdrew condition")
	}
	s.Dialogue = AdvanceDialogue(s, Event{ID: "withdraw", Text: "Pode ser pago, quero reservar mesmo assim"}, DialogueAssessment{Intent: "select", Objection: "none", Progress: "resolved"}, "scheduling", "v1", time.Now())
	if s.Dialogue.BookingCondition != "" {
		t.Fatal("explicit withdrawal ignored")
	}
}
