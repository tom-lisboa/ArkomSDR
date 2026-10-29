package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestDecisionsProtocolAndFailureBoundaries(t *testing.T) {
	for _, tc := range []string{"valid", "refusal", "incomplete", "invalid_category", "duplicate", "server_error"} {
		t.Run(tc, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/decisions" || r.Method != "POST" {
					t.Error("not native Decisions", r.URL.Path)
				}
				var body struct {
					Model, Input string
					Questions    []map[string]any
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Model != "gpt-6-luna" || len(body.Questions) != 5 || !strings.Contains(body.Input, "pergunta anterior") || !strings.Contains(body.Input, "empresa pequena") {
					t.Error("classification context missing")
				}
				if tc == "server_error" {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":{"message":"unavailable"}}`)
					return
				}
				answers := []map[string]any{}
				for name, value := range map[string]string{"intent": "objection", "objection": "fit", "readiness": "considering", "progress": "open", "cause": "provided"} {
					answers = append(answers, map[string]any{"type": "choice", "name": name, "choice": value, "confidence": 0.8})
				}
				switch tc {
				case "refusal":
					answers[0]["type"] = "refusal"
				case "incomplete":
					answers = answers[:3]
				case "invalid_category":
					answers[0]["choice"] = "execute_sql"
				case "duplicate":
					answers = append(answers, answers[0])
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers, "model": "gpt-6-luna", "usage": map[string]int{"input_tokens": 100, "output_tokens": 0, "total_tokens": 100}})
			}))
			defer srv.Close()
			c := DecisionsClassifier{Client: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL+"/")), Model: "gpt-6-luna"}
			a, err := c.Assess(context.Background(), domain.State{Profile: domain.Form{Goal: "atendimento"}}, domain.Event{Text: "empresa pequena", Context: &conversation.Window{History: []conversation.Message{{Role: "assistant", Text: "pergunta anterior"}}}})
			if tc == "valid" {
				if err != nil || a.Intent != "objection" || a.Objection != "fit" || a.Source != "decisions" {
					t.Fatal(a, err)
				}
			} else if err == nil {
				t.Fatal("invalid assessment accepted", tc, a)
			}
			if calls != 1 {
				t.Fatal("classification retried without bound", calls)
			}
		})
	}
}
