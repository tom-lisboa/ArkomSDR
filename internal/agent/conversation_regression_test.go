package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"arkom/bia/internal/domain"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestHumanAndPauseRequestsDoNotDependOnPlannerAgreement(t *testing.T) {
	for _, tc := range []struct{ intent, action string }{{"human", "handoff"}, {"pause_contact", "pause"}} {
		d := constrainSpecialist(domain.State{}, domain.Event{Text: "Pedido do cliente"}, domain.Decision{Action: "reply", Message: "Por qual motivo?"}, domain.DialogueAssessment{Intent: tc.intent}, time.Now())
		if d.Action != tc.action {
			t.Fatal(tc, d)
		}
	}
}

func TestRescheduleShowsOptionsBeforeAskingForAChoice(t *testing.T) {
	s := domain.State{Booking: &domain.Booking{ID: "current", Start: time.Now().Add(time.Hour)}}
	d := constrainSpecialist(s, domain.Event{Text: "Quero remarcar minha reunião"}, domain.Decision{Action: "reply", Message: "Qual horário você prefere?"}, domain.DialogueAssessment{Intent: "reschedule"}, time.Now())
	if d.Action != "offer_slots" || d.Message != "" {
		t.Fatal(d)
	}
}

func TestPresentationCleanupKeepsGrammarAndOneOfferIntroduction(t *testing.T) {
	for _, text := range []string{"Entendo o receio.", "Entendi sua dúvida.", "Fico feliz em ajudar com a agenda."} {
		got := normalizeTextDecision(domain.State{}, domain.Event{}, domain.Decision{Action: "reply", Message: text})
		if got.Message != text {
			t.Fatal("removed a verb from a complete sentence", got.Message)
		}
	}
	d := normalizeTextDecision(domain.State{}, domain.Event{}, domain.Decision{Action: "offer_slots", Message: "Podemos avaliar esse atendimento.\n\nTenho estes horários para você escolher:"})
	if d.Message != "Podemos avaliar esse atendimento." {
		t.Fatal("duplicated the domain's offer heading", d)
	}
}

func TestWithdrawnConditionRoutesToSchedulingAndExecutesLiteralChoice(t *testing.T) {
	now := time.Now()
	s := domain.State{Stage: "choosing_slot", Offered: []domain.Slot{{ID: "first", Start: now.Add(time.Hour)}}, Dialogue: &domain.DialogueState{BookingCondition: "se for gratuito"}}
	e := domain.Event{Text: "Não precisa ser gratuito, pode marcar o primeiro mesmo assim"}
	a := normalizeAssessment(s, e, domain.DialogueAssessment{Intent: "clarify", Objection: "price", Progress: "open", Readiness: "ready"})
	if a.Intent != "select" || SelectAssessment(s, e, a, now) != "scheduling" {
		t.Fatal("still trapped in old condition", a)
	}
	d := constrainSpecialist(s, e, domain.Decision{Action: "reply", Message: "Anotei sua escolha"}, a, now)
	if d.Action != "book" || d.SlotID != "first" {
		t.Fatal("choice was not actionable", d)
	}
	e.Text = "O primeiro, se for gratuito"
	d = constrainSpecialist(s, e, domain.Decision{Action: "reply", Message: "Qual condição?"}, domain.DialogueAssessment{Intent: "select"}, now)
	if d.Action == "book" {
		t.Fatal("new condition skipped")
	}
}

func TestConditionalChoiceOverridesInconsistentClassifier(t *testing.T) {
	a := normalizeAssessment(domain.State{}, domain.Event{Text: "O primeiro, se for gratuito"}, domain.DialogueAssessment{Intent: "select", Objection: "price", Readiness: "ready", Progress: "none"})
	if a.Intent != "clarify" || a.Progress != "open" || SelectAssessment(domain.State{}, domain.Event{}, a, time.Now()) != "objections" {
		t.Fatal("conditional choice bypassed the objection", a)
	}
	a = normalizeAssessment(domain.State{}, domain.Event{Text: "Pode manter o horário. O que devo preparar?"}, domain.DialogueAssessment{Intent: "objection", Objection: "none", Cause: "none", Readiness: "considering"})
	if SelectAssessment(domain.State{Booking: &domain.Booking{Start: time.Now().Add(time.Hour)}}, domain.Event{}, a, time.Now()) != "attendance" {
		t.Fatal("stuck on an absent objection", a)
	}
}

func TestObjectionQualityRequiresDirectResponseOnlyWhileThereIsOpening(t *testing.T) {
	s := domain.State{Dialogue: &domain.DialogueState{Agent: "objections", Assessment: domain.DialogueAssessment{Intent: "objection", Readiness: "considering"}, Objection: &domain.DialogueObjection{Kind: "fit", Status: "open", Clarification: "já usa ChatGPT"}}}
	if specialistQuality(s, domain.Decision{Action: "reply", Message: "Podemos avaliar seu atendimento. Se quiser, a gente olha isso na reunião."}) == "" {
		t.Fatal("vague invitation passed")
	}
	if specialistQuality(s, domain.Decision{Action: "reply", Message: "A reunião pode avaliar seu atendimento. Vamos olhar esse processo com o time?"}) != "" {
		t.Fatal("concrete next step rejected")
	}
	s.Dialogue.Objection.Attempts = []domain.DialogueAttempt{{ID: "1"}, {ID: "2"}}
	if specialistQuality(s, domain.Decision{Action: "reply", Message: "Tudo bem. Você pode pensar e me avisar."}) != "" {
		t.Fatal("forced third persuasion attempt")
	}
	s.Dialogue.Objection.Attempts = nil
	s.Dialogue.Assessment.Intent = "defer"
	if specialistQuality(s, domain.Decision{Action: "reply", Message: "Combinado."}) != "" {
		t.Fatal("forced invitation after defer")
	}
}

func TestLongDiagnosticMetadataDoesNotStopValidConversation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := json.Marshal(domain.Decision{Action: "reply", Message: "A conversa pode avaliar esse atendimento.", Reason: strings.Repeat("ação ", 200), Evidence: strings.Repeat("evidência ", 400)})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_test", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(d)}}}}})
	}))
	defer server.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/")), Model: "test"}
	out, err := a.Run(context.Background(), domain.State{}, domain.Event{Text: "Como ajuda?"}, nil, "sdr")
	if err != nil || out.Decision.Action != "reply" || len(out.Decision.Reason) > 500 || len(out.Decision.Evidence) > 2000 || !utf8.ValidString(out.Decision.Reason) || !utf8.ValidString(out.Decision.Evidence) {
		t.Fatal(out, err)
	}
}

func TestEmptyAgendaUsesDomainOutcomeWithoutUnscheduledPromises(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := json.Marshal(domain.Decision{Action: "reply", Message: "Posso avisar quando abrir uma vaga."})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_empty", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(d)}}}}})
	}))
	defer server.Close()
	for _, intent := range []string{"schedule", "reschedule"} {
		a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL+"/")), Model: "test", Specialists: true, Classifier: classified{a: domain.DialogueAssessment{Intent: intent, Source: "decisions"}}}
		out, err := a.Run(context.Background(), domain.State{}, domain.Event{Text: "Quais vagas?"}, nil, "sdr")
		if err != nil || out.Decision.Action != "offer_slots" || out.Decision.Message != "" {
			t.Fatal(out, err)
		}
	}
}
