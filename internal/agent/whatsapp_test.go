package agent

import (
	"context"
	"encoding/json"
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

func TestTextCleanupPreservesActionsAndUsefulContent(t *testing.T) {
	d := domain.Decision{Action: "offer_slots", OfferSlotIDs: []string{"a", "b"}, Message: "Perfeito!\n\nEntendi, dá pra avaliar isso na reunião — com o especialista."}
	got := normalizeTextDecision(domain.State{}, domain.Event{}, d)
	if got.Message != "Dá pra avaliar isso na reunião, com o especialista." || got.Action != d.Action || len(got.OfferSlotIDs) != 2 {
		t.Fatal(got)
	}
	d = domain.Decision{Action: "reply", Message: `Você escreveu "Perfeito" e pediu ajuda com seu atendimento.`}
	if got := normalizeTextDecision(domain.State{}, domain.Event{}, d); got.Message != d.Message {
		t.Fatal("removed quoted customer content", got)
	}
}

func TestBookingAcknowledgmentDoesNotBecomePresenceConfirmation(t *testing.T) {
	s := domain.State{Booking: &domain.Booking{ID: "booking"}, History: []domain.Turn{{Role: "assistant", Text: "Seu diagnóstico está marcado."}}}
	d := domain.Decision{Action: "confirm", Evidence: "Ok"}
	if got := normalizeTextDecision(s, domain.Event{Text: "Ok"}, d); got.Action != "reply" || got.Message != "Combinado." {
		t.Fatal(got)
	}
	e := domain.Event{Text: "Ok", Context: &conversation.Window{History: []conversation.Message{{Role: "assistant", Text: "Você confirma sua presença?"}}}}
	if got := normalizeTextDecision(s, e, d); got.Action != "confirm" {
		t.Fatal("ignored actual confirmation question", got)
	}
	if got := normalizeTextDecision(domain.State{}, domain.Event{Text: "Ok"}, domain.Decision{Action: "offer_slots"}); got.Action != "offer_slots" {
		t.Fatal("blocked first-contact acceptance", got)
	}
	if got := normalizeTextDecision(s, domain.Event{Text: "Confirmo minha presença"}, d); got.Action != "confirm" {
		t.Fatal("blocked explicit confirmation", got)
	}
}

func TestWhatsAppContextHasLocalLabelsAndCalendarFailure(t *testing.T) {
	now := time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC)
	s := domain.State{Profile: domain.Form{Goal: "Melhorar atendimento", TimeZone: "America/Cuiaba"}}
	slot := domain.Slot{ID: "internal-id", Start: now.Add(24 * time.Hour), End: now.Add(24*time.Hour + 45*time.Minute)}
	c := textContext(s, domain.Event{}, []domain.Slot{slot}, "", now)
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), "quinta-feira, 29/10, às 14h") || !strings.Contains(string(raw), `"duration_minutes":45`) {
		t.Fatal(string(raw))
	}
	c = textContext(s, domain.Event{CalendarUnavailable: true}, nil, "", now)
	if c["calendar"].(map[string]any)["status"] != "unavailable" {
		t.Fatal(c["calendar"])
	}
	if Select(domain.State{Offered: []domain.Slot{slot}}, domain.Event{Text: "o primeiro"}) != "scheduling" {
		t.Fatal("natural selection not routed to scheduling")
	}
}

func TestIncompleteModelBookingAsksForChoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_empty_slot","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"book\",\"message\":\"\",\"slot_id\":\"\",\"offer_slot_ids\":[],\"evidence\":\"sim\",\"reason\":\"ambiguous\"}"}]}]}`))
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "test"}
	d, err := a.Plan(context.Background(), domain.State{Offered: []domain.Slot{{ID: "a"}, {ID: "b"}}}, domain.Event{Text: "sim"}, nil)
	if err != nil || d.Action != "reply" || !strings.Contains(d.Message, "Qual") {
		t.Fatal(d, err)
	}
}

func TestCurrentBookingOverridesOldConversationInContext(t *testing.T) {
	now := time.Now()
	s := domain.State{Booking: &domain.Booking{Start: now.Add(time.Hour)}, History: []domain.Turn{{Role: "assistant", Text: "Quer agendar?"}}}
	c := textContext(s, domain.Event{Text: "Sim"}, nil, "", now)
	if c["meeting_status"] != "upcoming" {
		t.Fatal(c)
	}
	s.Booking = nil
	if c = textContext(s, domain.Event{}, nil, "", now); c["meeting_status"] != "not_scheduled" {
		t.Fatal(c)
	}
}

func TestOngoingMeetingKeepsAttendanceContextAndDirectResponse(t *testing.T) {
	now := time.Now()
	s := domain.State{Booking: &domain.Booking{ID: "ongoing", Start: now.Add(-time.Minute), End: now.Add(44 * time.Minute)}}
	if got := textContext(s, domain.Event{}, nil, "", now)["meeting_status"]; got != "in_progress" {
		t.Fatal(got)
	}
	a := domain.DialogueAssessment{Intent: "value", Source: "decisions"}
	if got := SelectAssessment(s, domain.Event{Text: "vou entrar agora"}, a, now); got != "attendance" {
		t.Fatal(got)
	}
	d := constrainSpecialist(s, domain.Event{}, domain.Decision{Action: "offer_slots"}, a, now)
	if d.Action != "reply" {
		t.Fatal("reopened agenda at meeting start", d)
	}
	if !strings.Contains(specialistInstructions("Arkom", "persuasion"), "RESPOSTA DIRETA") {
		t.Fatal("missing direct-response instructions")
	}
	s.Stage = "choosing_slot"
	s.Offered = []domain.Slot{{ID: "replacement", Start: now.Add(time.Hour)}}
	d = constrainSpecialist(s, domain.Event{Text: "o primeiro"}, domain.Decision{Action: "reschedule", SlotID: "replacement"}, domain.DialogueAssessment{Intent: "select"}, now)
	if d.Action != "reschedule" {
		t.Fatal("blocked choice after explicit reschedule", d)
	}
	d = constrainSpecialist(s, domain.Event{Text: "Vou me atrasar dez minutos"}, domain.Decision{Action: "reply", Message: "Pode entrar, o consultor espera."}, a, now)
	if d.Action != "reply" || strings.Contains(d.Message, "consultor espera") || !strings.Contains(d.Message, "intervalo") {
		t.Fatal("invented host availability", d)
	}
}
