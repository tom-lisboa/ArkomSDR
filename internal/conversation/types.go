// Package conversation defines the context, execution and media contracts.
// It has no dependency on transports, the SDK, or application business rules.
package conversation

import "time"

type Message struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}
type Head struct {
	PromptVersion string    `json:"prompt_version,omitempty"`
	ResponseID    string    `json:"response_id"`
	At            time.Time `json:"at"`
	Depth         int       `json:"depth"`
	InputTokens   int64     `json:"input_tokens"`
}
type Window struct {
	ReceivedAt     time.Time       `json:"received_at,omitempty"`
	ConversationID string          `json:"conversation_id"`
	RunID          string          `json:"run_id"`
	Version        int64           `json:"version"`
	MessageIDs     []string        `json:"message_ids"`
	History        []Message       `json:"history"`
	Heads          map[string]Head `json:"heads"`
	MediaFailed    bool            `json:"media_failed"`
}
type RunResult struct {
	PromptVersion  string `json:"prompt_version,omitempty"`
	ConversationID string `json:"conversation_id"`
	RunID          string `json:"run_id"`
	Version        int64  `json:"version"`
	Agent          string `json:"agent"`
	ResponseID     string `json:"response_id"`
	InputTokens    int64  `json:"input_tokens"`
	UsedPrevious   bool   `json:"used_previous"`
	Status         string `json:"status"`
}
type Attachment struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
	MIME string `json:"mime,omitempty"`
}
type MediaRequest struct {
	ConversationID string     `json:"conversation_id"`
	MessageID      string     `json:"message_id"`
	Attachment     Attachment `json:"attachment"`
}
type MediaResult struct {
	MediaRequest
	Text      string `json:"text"`
	ErrorCode string `json:"error_code,omitempty"`
}
type Delivery struct {
	OperatorID             string    `json:"operator_id,omitempty"`
	ProviderConversationID string    `json:"provider_conversation_id,omitempty"`
	ConversationID         string    `json:"conversation_id"`
	ID                     string    `json:"id"`
	Text                   string    `json:"text"`
	At                     time.Time `json:"at"`
}
