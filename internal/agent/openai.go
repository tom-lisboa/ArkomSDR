package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

type Planner interface {
	Plan(context.Context, domain.State, domain.Event, []domain.Slot) (domain.Decision, error)
}

type OpenAI struct {
	Client        openai.Client
	Model         string
	Brand         string
	SchedulingURL string
}

func New(key, model, brand, schedulingURL string) *OpenAI {
	return &OpenAI{Client: openai.NewClient(option.WithAPIKey(key), option.WithMaxRetries(1)), Model: model, Brand: brand, SchedulingURL: schedulingURL}
}

func (a *OpenAI) Plan(ctx context.Context, s domain.State, e domain.Event, slots []domain.Slot) (domain.Decision, error) {
	o, err := a.Run(ctx, s, e, slots, "sdr")
	return o.Decision, err
}

func (a *OpenAI) Run(ctx context.Context, s domain.State, e domain.Event, slots []domain.Slot, agentID string) (Outcome, error) {
	var out Outcome
	d := &out.Decision
	var head conversation.Head
	if e.Context != nil {
		head = e.Context.Heads[agentID]
	}
	// Do not give the model credentials, arbitrary workflow code or database access.
	input, err := json.Marshal(textContext(s, e, slots, a.SchedulingURL, time.Now().UTC()))
	if err != nil {
		return out, err
	}
	prompt := textInstructions(a.Brand, agentID)
	params := responses.ResponseNewParams{
		Model: a.Model, Instructions: openai.String(prompt), Store: openai.Bool(e.Context != nil), MaxOutputTokens: openai.Int(1500),
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(string(input))},
		Text:  responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "sdr_decision", Strict: openai.Bool(true), Schema: DecisionSchema()}}},
	}
	if head.ResponseID != "" {
		params.PreviousResponseID = openai.String(head.ResponseID)
	}
	res, err := a.Client.Responses.New(ctx, params)
	if err != nil && head.ResponseID != "" {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) && (apiErr.StatusCode == 404 || apiErr.StatusCode == 400) && (apiErr.Param == "previous_response_id" || strings.Contains(apiErr.Code, "previous_response") || strings.Contains(strings.ToLower(apiErr.Message), "previous response") || strings.Contains(apiErr.Message, "previous_response")) {
			params.PreviousResponseID = param.Opt[string]{}
			res, err = a.Client.Responses.New(ctx, params)
		}
	}
	if err != nil {
		return out, errors.New("openai_request_failed")
	}
	if res.Status != "completed" || strings.TrimSpace(res.OutputText()) == "" {
		return out, errors.New("openai_incomplete_or_refused")
	}
	if err = json.Unmarshal([]byte(res.OutputText()), d); err != nil {
		return out, errors.New("openai_invalid_json")
	}
	if len([]rune(d.Message)) > 1000 || len(d.Reason) > 500 || len(d.Evidence) > 2000 || len(d.OfferSlotIDs) > 2 {
		return out, errors.New("openai_invalid_decision")
	}
	switch d.Action {
	case "reply", "offer_slots", "book", "reschedule", "cancel", "confirm", "pause", "opt_out", "handoff":
	default:
		return out, errors.New("openai_invalid_action")
	}
	// A valid JSON shape can still contain an incomplete operational proposal.
	// Ask for the selection instead of passing an empty booking ID downstream.
	if (d.Action == "book" || d.Action == "reschedule") && d.SlotID == "" {
		d.Action = "reply"
		d.Message = "Qual dos horários oferecidos você escolhe?"
		if len(s.Offered) == 0 {
			d.Action = "offer_slots"
			d.Message = ""
		}
	}
	*d = normalizeTextDecision(s, e, *d)
	out.ResponseID = res.ID
	out.InputTokens = res.Usage.InputTokens
	out.UsedPrevious = params.PreviousResponseID.Value != ""
	return out, nil
}

func DecisionSchema() map[string]any {
	props := map[string]any{}
	for _, k := range []string{"action", "message", "slot_id", "evidence", "reason"} {
		props[k] = map[string]any{"type": "string"}
	}
	props["offer_slot_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 2}
	props["action"] = map[string]any{"type": "string", "enum": []string{"reply", "offer_slots", "book", "reschedule", "cancel", "confirm", "pause", "opt_out", "handoff"}}
	return map[string]any{"type": "object", "properties": props, "required": []string{"action", "message", "slot_id", "offer_slot_ids", "evidence", "reason"}, "additionalProperties": false}
}

// Offline is deliberately limited: it supports deterministic replay without an
// API key and never pretends to provide a full AI conversation.
type Offline struct{}

func (Offline) Plan(_ context.Context, _ domain.State, e domain.Event, _ []domain.Slot) (domain.Decision, error) {
	t := strings.ToLower(e.Text)
	if strings.Contains(t, "horário") || strings.Contains(t, "horario") || strings.Contains(t, "agendar") {
		return domain.Decision{Action: "offer_slots", Reason: "offline_scheduling"}, nil
	}
	return domain.Decision{Action: "handoff", Reason: "planner_offline"}, nil
}
