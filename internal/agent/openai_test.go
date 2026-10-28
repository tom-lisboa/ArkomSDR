package agent

import (
	"arkom/bia/internal/conversation"
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

func TestSDKStructuredRequestAndDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p["model"] != "test-model" || p["store"] != false {
			t.Error("wrong model or retention")
		}
		f := p["text"].(map[string]any)["format"].(map[string]any)
		if f["type"] != "json_schema" || f["strict"] != true {
			t.Error("schema not strict")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"offer_slots\",\"message\":\"\",\"slot_id\":\"\",\"evidence\":\"\",\"reason\":\"lead quer agendar\"}"}]}]}`))
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0)), Model: "test-model", Brand: "Exemplo", SchedulingURL: "https://example.com"}
	d, e := a.Plan(context.Background(), domain.State{}, domain.Event{Text: "Quero agendar"}, nil)
	if e != nil || d.Action != "offer_slots" {
		t.Fatalf("%+v %v", d, e)
	}
}
func TestTextModelReceivesCurrentFormAndMeeting(t *testing.T) {
	zero := int64(0)
	start := time.Now().Add(time.Hour)
	state := domain.State{Profile: domain.Form{Name: "Ana", Company: "Acme", Revenue: &zero, TimeZone: "America/Cuiaba"}, Booking: &domain.Booking{Start: start}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
		}
		var payload struct {
			Customer domain.Customer `json:"customer"`
		}
		if e := json.Unmarshal([]byte(req.Input), &payload); e != nil {
			t.Error(e)
		}
		c := payload.Customer
		if c.Name != "Ana" || c.Company != "Acme" || c.Revenue == nil || *c.Revenue != 0 || c.MeetingDate == nil || !c.MeetingDate.Equal(start) {
			t.Error("missing shared context", c)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"refused","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"test"}]}]}`))
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0)), Model: "test"}
	_, _ = a.Plan(context.Background(), state, domain.Event{}, nil)
}

func TestRefusalNeverBecomesAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"refused","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"No"}]}]}`))
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0)), Model: "test"}
	if _, e := a.Plan(context.Background(), domain.State{}, domain.Event{}, nil); e == nil {
		t.Fatal("accepted refusal")
	}
}

func TestPreviousResponseFallbackAndAgentIsolation(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		count++
		if p["store"] != true || p["instructions"] == "" {
			t.Error("chain must store response and resend instructions")
		}
		if count == 1 {
			if p["previous_response_id"] != "resp_scheduling" {
				t.Error("wrong agent head", p["previous_response_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"Previous response not found","type":"invalid_request_error","code":"not_found"}}`))
			return
		}
		if p["previous_response_id"] != nil {
			t.Error("fallback kept invalid head")
		}
		if !strings.Contains(p["input"].(string), "histórico preservado") {
			t.Error("fallback lost durable history")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_new","status":"completed","usage":{"input_tokens":123},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"action\":\"reply\",\"message\":\"Olá\",\"slot_id\":\"\",\"evidence\":\"\",\"reason\":\"\"}"}]}]}`))
	}))
	defer srv.Close()
	a := &OpenAI{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/"), option.WithMaxRetries(0)), Model: "test"}
	ev := domain.Event{Text: "qual horário?", Context: &conversation.Window{History: []conversation.Message{{Role: "user", Text: "histórico preservado"}}, Heads: map[string]conversation.Head{"sdr": {ResponseID: "resp_other"}, "scheduling": {ResponseID: "resp_scheduling"}}}}
	o, e := a.Run(context.Background(), domain.State{}, ev, nil, "scheduling")
	if e != nil || count != 2 || o.ResponseID != "resp_new" || o.InputTokens != 123 || o.UsedPrevious {
		t.Fatalf("%+v %v requests=%d", o, e, count)
	}
}
