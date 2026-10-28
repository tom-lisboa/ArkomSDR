package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

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
