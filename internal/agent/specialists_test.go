package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type classified struct {
	a   domain.DialogueAssessment
	err error
}

func (c classified) Assess(context.Context, domain.State, domain.Event) (domain.DialogueAssessment, error) {
	return c.a, c.err
}

func TestSpecialistRoutingDoesNotTrapObjectionInCalendar(t *testing.T) {
	now := time.Now()
	for _, booked := range []bool{false, true} {
		s := domain.State{Offered: []domain.Slot{{ID: "offered"}}}
		if booked {
			s.Booking = &domain.Booking{Start: now.Add(time.Hour)}
		}
		a := domain.DialogueAssessment{Intent: "objection", Objection: "fit", Progress: "open", Source: "decisions"}
		if got := SelectAssessment(s, domain.Event{Text: "não é para mim"}, a, now); got != "objections" {
			t.Fatal(got)
		}
		a.Intent = "schedule"
		want := "scheduling"
		if booked {
			want = "attendance"
		}
		if got := SelectAssessment(s, domain.Event{}, a, now); got != want {
			t.Fatal(got, want)
		}
		a.Intent = "reschedule"
		if got := SelectAssessment(s, domain.Event{}, a, now); got != "scheduling" {
			t.Fatal(got)
		}
	}
}

func TestSpecialistGetsSharedMemoryAndVersionedOwnHead(t *testing.T) {
	for _, version := range []string{SpecialistPromptVersion, "old-prompt"} {
		t.Run(version, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)
				if !strings.Contains(p["instructions"].(string), "TAREFA INTERNA: ESCLARECER") || strings.Contains(p["instructions"].(string), "TAREFA INTERNA: AGENDAMENTO") {
					t.Error("prompts are not independent")
				}
				if version == SpecialistPromptVersion && p["previous_response_id"] != "resp_objection" {
					t.Error("wrong own head", p["previous_response_id"])
				}
				if version != SpecialistPromptVersion && p["previous_response_id"] != nil {
					t.Error("old prompt head reused")
				}
				input := p["input"].(string)
				for _, fact := range []string{"Ana", "melhorar atendimento", "já falei sobre porte", "resposta recebida da outra especialidade"} {
					if !strings.Contains(input, fact) {
						t.Error("lost shared fact", fact)
					}
				}
				f := p["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]any)
				for _, v := range f {
					if v == "book" || v == "offer_slots" {
						t.Error("open objection can mutate/offer", f)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_next","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"reply\",\"message\":\"O que te fez sentir isso?\",\"slot_id\":\"\",\"offer_slot_ids\":[],\"evidence\":\"\",\"reason\":\"fit\"}"}]}]}`)
			}))
			defer srv.Close()
			a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "test", Specialists: true, Classifier: classified{a: domain.DialogueAssessment{Intent: "objection", Objection: "fit", Readiness: "considering", Progress: "open", Source: "decisions"}}}
			s := domain.State{Profile: domain.Form{Name: "Ana", Goal: "melhorar atendimento"}, Dialogue: &domain.DialogueState{Objection: &domain.DialogueObjection{ID: "old", Kind: "fit", Status: "open", Attempts: []domain.DialogueAttempt{{ID: "old", Text: "já falei sobre porte"}}}}}
			e := domain.Event{ID: "turn", Text: "acho que não serve", Context: &conversation.Window{History: []conversation.Message{{Role: "assistant", Text: "resposta recebida da outra especialidade"}}, Heads: map[string]conversation.Head{"scheduling": {ResponseID: "wrong_agent"}, "objections": {ResponseID: "resp_objection", PromptVersion: version}}}}
			o, err := a.Run(context.Background(), s, e, nil, "scheduling")
			if err != nil || o.Agent != "objections" || o.PromptVersion != SpecialistPromptVersion || o.Dialogue.Objection.ID != "old" || len(o.Dialogue.Objection.Attempts) != 1 {
				t.Fatal(o, err)
			}
		})
	}
}

func TestClassifierFailureKeepsSafeSpecialistWithoutHumanTakeover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"fallback","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"reply\",\"message\":\"O que te fez sentir isso?\",\"slot_id\":\"\",\"offer_slot_ids\":[],\"evidence\":\"\",\"reason\":\"clarify\"}"}]}]}`)
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "test", Specialists: true, Classifier: classified{err: errors.New("offline")}}
	o, err := a.Run(context.Background(), domain.State{}, domain.Event{ID: "turn", Text: "Arkom não é para mim"}, nil, "sdr")
	if err != nil || o.Agent != "objections" || o.Decision.Action != "reply" || o.Dialogue.Assessment.Source != "decisions_unavailable" {
		t.Fatal(o, err)
	}
}

func TestSpecialistGuardsConditionalAcceptanceAndExistingBooking(t *testing.T) {
	d := domain.Decision{Action: "book", SlotID: "one"}
	a := domain.DialogueAssessment{Intent: "clarify", Objection: "price", Progress: "open"}
	if v := constrainSpecialist(domain.State{}, domain.Event{}, d, a, time.Now()); v.Action != "reply" || v.SlotID != "" {
		t.Fatal(v)
	}
	s := domain.State{Booking: &domain.Booking{Start: time.Now().Add(time.Hour)}}
	if v := constrainSpecialist(s, domain.Event{}, d, domain.DialogueAssessment{Intent: "value"}, time.Now()); v.Action != "reply" {
		t.Fatal(v)
	}
	if v := constrainSpecialist(s, domain.Event{}, domain.Decision{Action: "pause"}, domain.DialogueAssessment{Intent: "defer"}, time.Now()); v.Action != "reply" {
		t.Fatal(v)
	}
	if v := constrainSpecialist(s, domain.Event{}, domain.Decision{Action: "confirm", Message: "Separe um exemplo do atendimento."}, domain.DialogueAssessment{Intent: "value"}, time.Now()); v.Action != "reply" || v.Message != "Separe um exemplo do atendimento." {
		t.Fatal(v)
	}
}

func TestAssessmentConsistencyAndPendingConditionAcrossTurns(t *testing.T) {
	a := domain.DialogueAssessment{Intent: "objection", Objection: "fit", Readiness: "declined", Progress: "declined", Confidence: map[string]float64{"progress": 0.5, "readiness": 0.4}}
	a = normalizeAssessment(domain.State{}, domain.Event{}, a)
	if a.Progress != "open" || a.Readiness != "considering" || len(a.Adjusted) != 2 || len(a.Confidence) != 0 {
		t.Fatal(a)
	}
	s := domain.State{Dialogue: &domain.DialogueState{BookingCondition: "se for gratuito", Objection: &domain.DialogueObjection{Kind: "price", Status: "open"}}}
	a = normalizeAssessment(s, domain.Event{Text: "o primeiro"}, domain.DialogueAssessment{Intent: "select", Objection: "none", Progress: "resolved", Readiness: "ready"})
	if a.Intent != "clarify" || a.Progress != "open" || SelectAssessment(s, domain.Event{}, a, time.Now()) != "objections" {
		t.Fatal("forgot outstanding condition", a)
	}
	a = normalizeAssessment(s, domain.Event{Text: "Pode marcar mesmo assim"}, domain.DialogueAssessment{Intent: "select", Objection: "none", Progress: "resolved", Readiness: "ready"})
	if a.Intent != "select" {
		t.Fatal("blocked explicit withdrawal", a)
	}
	d := constrainSpecialist(domain.State{}, domain.Event{}, domain.Decision{Action: "offer_slots", Message: "Tenho amanhã às 14h e 14h45."}, domain.DialogueAssessment{Intent: "schedule"}, time.Now())
	if d.Message != "" {
		t.Fatal("schedule duplicated by model", d)
	}
	d = constrainSpecialist(domain.State{Offered: []domain.Slot{{ID: "a"}, {ID: "b"}}}, domain.Event{Text: "sim"}, domain.Decision{Action: "book", SlotID: "a"}, domain.DialogueAssessment{Intent: "clarify", Objection: "none"}, time.Now())
	if d.Action != "reply" || d.Reason != "clarify_offered_slot" {
		t.Fatal("ambiguous yes became objection question", d)
	}
	a = normalizeAssessment(domain.State{Booking: &domain.Booking{Start: time.Now().Add(time.Hour)}}, domain.Event{Text: "Pode manter, o que preciso preparar?"}, domain.DialogueAssessment{Intent: "objection", Objection: "none", Cause: "none", Readiness: "ready", Progress: "resolved"})
	if SelectAssessment(domain.State{Booking: &domain.Booking{Start: time.Now().Add(time.Hour)}}, domain.Event{}, a, time.Now()) != "attendance" {
		t.Fatal("stuck in old objection", a)
	}
}

func TestSpecialistQualityRepairIsBoundedAndDoesNotReuseBadHead(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		t.Run(fmt.Sprint(fixed), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				if calls == 2 && !strings.Contains(req["instructions"].(string), "REVISÃO OBRIGATÓRIA") {
					t.Error("missing repair instruction")
				}
				text := "O que te fez sentir que não se encaixa?"
				if calls == 2 && fixed {
					text = "A conversa pode avaliar seu atendimento nesse contexto."
				}
				decision, _ := json.Marshal(domain.Decision{Action: "reply", Message: text})
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("resp_%d", calls), "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(decision)}}}}})
			}))
			defer srv.Close()
			a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "test", Specialists: true, Classifier: classified{a: domain.DialogueAssessment{Intent: "objection", Objection: "fit", Cause: "provided", Progress: "open", Source: "decisions"}}}
			o, err := a.Run(context.Background(), domain.State{}, domain.Event{ID: "known-cause", Text: "Não serve, minha empresa é pequena"}, nil, "sdr")
			if err != nil || calls != 2 || repeatedCause.MatchString(o.Decision.Message) {
				t.Fatal(o, err, calls)
			}
			if !fixed && (o.ResponseID != "" || o.Decision.Reason != "specialist_quality_fallback") {
				t.Fatal("bad draft retained as memory head", o)
			}
		})
	}
}

func TestLegacyRollbackDoesNotReuseSpecialistHeadOrInstructions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["previous_response_id"] != nil || strings.Contains(body["instructions"].(string), "TAREFA INTERNA:") || strings.Contains(body["input"].(string), "private-specialist-memory") {
			t.Error("rollback kept specialist context", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_legacy","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"reply\",\"message\":\"Combinado.\",\"slot_id\":\"\",\"offer_slot_ids\":[],\"evidence\":\"\",\"reason\":\"ack\"}"}]}]}`)
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "test"}
	o, err := a.Run(context.Background(), domain.State{Dialogue: &domain.DialogueState{LastQuestion: "private-specialist-memory"}}, domain.Event{Text: "ok", Context: &conversation.Window{Heads: map[string]conversation.Head{"scheduling": {ResponseID: "resp_specialist", PromptVersion: SpecialistPromptVersion}}}}, nil, "scheduling")
	if err != nil || o.UsedPrevious || o.Dialogue != nil {
		t.Fatal(o, err)
	}
}
