package domain

import (
	"regexp"
	"strings"
	"time"
)

// DialogueAssessment describes observable conversational signals, not a sales
// probability. Booking and contact permissions always come from State.
type DialogueAssessment struct {
	Model       string             `json:"model,omitempty"`
	InputTokens int64              `json:"input_tokens,omitempty"`
	LatencyMS   int64              `json:"latency_ms,omitempty"`
	Cause       string             `json:"cause"`
	Adjusted    []string           `json:"adjusted,omitempty"`
	Intent      string             `json:"intent"`
	Objection   string             `json:"objection"`
	Readiness   string             `json:"readiness"`
	Progress    string             `json:"progress"`
	Confidence  map[string]float64 `json:"confidence,omitempty"`
	Source      string             `json:"source"`
}
type DialogueAttempt struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}
type DialogueObjection struct {
	Clarification string            `json:"clarification,omitempty"`
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	Evidence      string            `json:"evidence"`
	Attempts      []DialogueAttempt `json:"attempts,omitempty"`
}
type DialogueState struct {
	BookingCondition    string              `json:"booking_condition,omitempty"`
	Agent               string              `json:"agent"`
	PromptVersion       string              `json:"prompt_version"`
	EventID             string              `json:"event_id"`
	ConversationVersion int64               `json:"conversation_version,omitempty"`
	Assessment          DialogueAssessment  `json:"assessment"`
	Objection           *DialogueObjection  `json:"objection,omitempty"`
	PreviousObjections  []DialogueObjection `json:"previous_objections,omitempty"`
	LastQuestion        string              `json:"last_question,omitempty"`
	LastResponse        string              `json:"last_response,omitempty"`
	LastResponseTurn    string              `json:"last_response_turn,omitempty"`
	At                  time.Time           `json:"at"`
}
type DialogueDelivery struct {
	Agent       string `json:"agent"`
	TurnID      string `json:"turn_id"`
	ObjectionID string `json:"objection_id,omitempty"`
}

func dialogueText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func copyDialogue(s *DialogueState) *DialogueState {
	if s == nil {
		return &DialogueState{}
	}
	n := *s
	n.PreviousObjections = append([]DialogueObjection(nil), s.PreviousObjections...)
	if s.Objection != nil {
		o := *s.Objection
		o.Attempts = append([]DialogueAttempt(nil), o.Attempts...)
		n.Objection = &o
	}
	return &n
}

// Assessments do not consume persuasion attempts. Only confirmed delivery does.
func AdvanceDialogue(s State, e Event, a DialogueAssessment, id, version string, now time.Time) *DialogueState {
	n := copyDialogue(s.Dialogue)
	n.Agent, n.PromptVersion, n.EventID, n.Assessment, n.At = id, version, e.ID, a, now
	if e.Context != nil {
		n.ConversationVersion = e.Context.Version
	}
	if (a.Objection == "price" || a.Objection == "time") && (a.Intent == "clarify" || a.Intent == "objection") && ConditionalAcceptance(e.Text) {
		n.BookingCondition = dialogueText(e.Text, 1000)
	}
	if WithdrawsCondition(e.Text) {
		n.BookingCondition = ""
	}
	kind := a.Objection
	if kind == "unknown" && n.Objection != nil && a.Intent == "objection" {
		kind = n.Objection.Kind
	}
	if kind != "" && kind != "none" && (kind != "unknown" || a.Intent == "objection") {
		if n.Objection == nil || n.Objection.Kind != kind || n.Objection.Status == "resolved" || n.Objection.Status == "declined" {
			if n.Objection != nil {
				n.PreviousObjections = append(n.PreviousObjections, *n.Objection)
			}
			n.Objection = &DialogueObjection{ID: e.ID, Kind: kind, Status: "open", Evidence: dialogueText(e.Text, 1000)}
		}
	}
	if n.Objection != nil {
		if n.BookingCondition != "" {
			n.Objection.Clarification = n.BookingCondition
		} else if a.Progress == "clarifying" || a.Cause == "provided" {
			if n.Objection.Clarification == "" {
				n.Objection.Clarification = dialogueText(e.Text, 1000)
			} else if !strings.Contains(n.Objection.Clarification, e.Text) {
				n.Objection.Clarification = dialogueText(n.Objection.Clarification+"\n"+e.Text, 1000)
			}
		}
		switch a.Progress {
		case "resolved", "deferred", "declined", "clarifying", "open":
			n.Objection.Status = a.Progress
		}
	}
	if len(n.PreviousObjections) > 4 {
		n.PreviousObjections = n.PreviousObjections[len(n.PreviousObjections)-4:]
	}
	return n
}

var conditionalAcceptanceRE = regexp.MustCompile(`(?i)\b(se (for|fosse|tiver|custar|durar)|desde que|com a condição|com a condicao)\b`)
var withdrawConditionRE = regexp.MustCompile(`(?i)(independentemente|mesmo assim|n[ãa]o precisa ser|sem essa condi[çc][ãa]o|sem essa exig[êe]ncia|pode ser pag[oa])`)

func ConditionalAcceptance(text string) bool { return conditionalAcceptanceRE.MatchString(text) }
func WithdrawsCondition(text string) bool    { return withdrawConditionRE.MatchString(text) }

func RecordDialogueDelivery(s *State, fx Effect, text string, at time.Time) {
	if s.Dialogue == nil || fx.Dialogue == nil {
		return
	}
	n := copyDialogue(s.Dialogue)
	s.Dialogue = n
	m := fx.Dialogue
	if n.LastResponseTurn == m.TurnID {
		n.LastResponse = dialogueText(n.LastResponse+"\n\n"+text, 1000)
	} else {
		n.LastResponse = text
	}
	n.LastResponseTurn = m.TurnID
	if strings.Contains(text, "?") {
		n.LastQuestion = dialogueText(n.LastResponse, 1000)
	}
	if m.Agent != "objections" || n.Objection == nil || n.Objection.ID != m.ObjectionID {
		return
	}
	o := n.Objection
	for i := range o.Attempts {
		if o.Attempts[i].ID == m.TurnID {
			o.Attempts[i].Text = dialogueText(o.Attempts[i].Text+"\n\n"+text, 1000)
			return
		}
	}
	o.Attempts = append(o.Attempts, DialogueAttempt{ID: m.TurnID, Text: dialogueText(text, 1000), At: at})
	if len(o.Attempts) > 4 {
		o.Attempts = o.Attempts[len(o.Attempts)-4:]
	}
}
