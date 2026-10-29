package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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
	Specialists   bool
	Classifier    Classifier
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
	if !a.Specialists {
		s.Dialogue = nil
	}
	assessment := domain.DialogueAssessment{}
	if a.Specialists {
		assessment = domain.DialogueAssessment{Intent: "other", Objection: "unknown", Readiness: "unknown", Progress: "none", Source: "decisions_unavailable"}
		if a.Classifier != nil {
			if v, err := a.Classifier.Assess(ctx, s, e); err == nil {
				assessment = v
			}
		}
		assessment = normalizeAssessment(s, e, assessment)
		agentID = SelectAssessment(s, e, assessment, time.Now().UTC())
		if assessment.Source != "decisions" && agentID == "objections" {
			assessment.Intent = "objection"
		}
		out.Dialogue = domain.AdvanceDialogue(s, e, assessment, agentID, SpecialistPromptVersion, time.Now().UTC())
		s.Dialogue = out.Dialogue
		out.PromptVersion = SpecialistPromptVersion
	}
	out.Agent = agentID
	var head conversation.Head
	if e.Context != nil {
		head = e.Context.Heads[agentID]
	}
	if (a.Specialists && head.PromptVersion != SpecialistPromptVersion) || (!a.Specialists && head.PromptVersion != "") {
		head = conversation.Head{}
	}
	// Do not give the model credentials, arbitrary workflow code or database access.
	input, err := json.Marshal(textContext(s, e, slots, a.SchedulingURL, time.Now().UTC()))
	if err != nil {
		return out, err
	}
	prompt := textInstructions(a.Brand, agentID)
	schema := DecisionSchema()
	if a.Specialists {
		prompt = specialistInstructions(a.Brand, agentID)
		schema = decisionSchema(specialistActions(agentID, assessment))
	}
	params := responses.ResponseNewParams{
		Model: a.Model, Instructions: openai.String(prompt), Store: openai.Bool(e.Context != nil), MaxOutputTokens: openai.Int(1500),
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(string(input))},
		Text:  responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "sdr_decision", Strict: openai.Bool(true), Schema: schema}}},
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
	if a.Specialists {
		*d = normalizeTextDecision(s, e, *d)
		if feedback := specialistQuality(s, *d); feedback != "" {
			params.Instructions = openai.String(prompt + "\n\nREVISÃO OBRIGATÓRIA DESTA RESPOSTA: " + feedback)
			fixed, repairErr := a.Client.Responses.New(ctx, params, option.WithMaxRetries(0))
			if repairErr == nil && fixed.Status == "completed" && strings.TrimSpace(fixed.OutputText()) != "" {
				candidate := domain.Decision{}
				if json.Unmarshal([]byte(fixed.OutputText()), &candidate) == nil {
					candidate = normalizeTextDecision(s, e, candidate)
					if specialistQuality(s, candidate) == "" {
						*d = candidate
						res = fixed
					} else {
						*d = specialistQualityFallback(s)
					}
				} else {
					*d = specialistQualityFallback(s)
				}
			} else {
				*d = specialistQualityFallback(s)
			}
		}
		if d.Reason == "specialist_quality_fallback" {
			res.ID = ""
		}
	}
	// Diagnostic prose is not an operational field. Bound it without dropping a
	// valid customer response or stopping the conversation for an oversized reason.
	d.Reason = boundedMetadata(d.Reason, 500)
	d.Evidence = boundedMetadata(d.Evidence, 2000)
	if len([]rune(d.Message)) > 1000 || len(d.OfferSlotIDs) > 2 {
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
	if a.Specialists {
		*d = constrainSpecialist(s, e, *d, assessment, time.Now().UTC())
		if len(slots) == 0 && (assessment.Intent == "schedule" || assessment.Intent == "reschedule") {
			*d = domain.Decision{Action: "offer_slots", Reason: "calendar_has_no_available_slots"}
		}
	}
	out.ResponseID = res.ID
	out.InputTokens = res.Usage.InputTokens
	out.UsedPrevious = params.PreviousResponseID.Value != ""
	return out, nil
}

func boundedMetadata(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for len(text) > limit {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

func DecisionSchema() map[string]any {
	return decisionSchema([]string{"reply", "offer_slots", "book", "reschedule", "cancel", "confirm", "pause", "opt_out", "handoff"})
}
func decisionSchema(actions []string) map[string]any {
	props := map[string]any{}
	for _, k := range []string{"action", "message", "slot_id", "evidence", "reason"} {
		props[k] = map[string]any{"type": "string"}
	}
	props["offer_slot_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 2}
	props["action"] = map[string]any{"type": "string", "enum": actions}
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
