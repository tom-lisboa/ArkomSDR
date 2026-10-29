package voiceworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"arkom/bia/internal/domain"
	"github.com/coder/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/realtime"
)

type AudioConnection interface {
	SendRaw(context.Context, json.RawMessage) error
	RecvRaw(context.Context) (json.RawMessage, error)
	Close() error
}
type Connect func(context.Context) (AudioConnection, error)

func OpenAIConnect(key, model string) Connect {
	client := openai.NewClient(option.WithAPIKey(key), option.WithMaxRetries(0))
	return func(ctx context.Context) (AudioConnection, error) {
		return client.Realtime.Connect(ctx, realtime.RealtimeConnectionOptions{}, option.WithQuery("model", model))
	}
}

type Bridge struct {
	Repo                                    Repository
	Provider                                Provider
	Connect                                 Connect
	Tools                                   Tools
	Model, Voice, TranscriptionModel, Brand string
	MaxDuration                             time.Duration
}
type wireEvent struct {
	Event     string `json:"event"`
	StreamSID string `json:"streamSid"`
	Start     struct {
		AccountSID       string            `json:"accountSid"`
		CallSID          string            `json:"callSid"`
		StreamSID        string            `json:"streamSid"`
		CustomParameters map[string]string `json:"customParameters"`
		MediaFormat      struct {
			Encoding   string `json:"encoding"`
			SampleRate int    `json:"sampleRate"`
			Channels   int    `json:"channels"`
		} `json:"mediaFormat"`
	} `json:"start"`
	Media struct {
		Payload string `json:"payload"`
		Track   string `json:"track"`
	} `json:"media"`
	Mark struct {
		Name string `json:"name"`
	} `json:"mark"`
}
type aiEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Session    struct {
		ID string `json:"id"`
	} `json:"session"`
}
type audioItem struct {
	TotalMS, PlayedMS     int
	Text                  string
	Done, Cleared, Stored bool
}
type audioMark struct {
	Item  string
	EndMS int
}
type socketResult struct {
	Raw json.RawMessage
	Err error
}
type toolResult struct {
	ID    string
	Value any
}

func (b *Bridge) Run(parent context.Context, tw *websocket.Conn, r Request, stream string) (err error) {
	duration := b.MaxDuration
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	// No call recording is enabled. Only received transcripts and acknowledged
	// assistant playback are retained through the context worker.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err != nil {
			_ = b.Repo.MarkError(cleanup, r.ID, "voice_bridge_failed")
		}
		if e := b.Provider.Hangup(cleanup, r.CallSID); e != nil {
			_ = b.Repo.MarkError(cleanup, r.ID, "voice_hangup_uncertain")
		}
	}()
	_, e := b.Repo.Active(ctx, r)
	if e != nil {
		return e
	}
	if b.Tools.Agenda == nil {
		return errSession
	}
	meeting, e := b.Tools.Agenda.Lookup(ctx, r.ID)
	if e != nil || meeting.Status != "available" || meeting.Booking == nil || meeting.Customer.MeetingDate == nil {
		return errSession
	}
	opening, stop := context.WithTimeout(ctx, 10*time.Second)
	ai, e := b.Connect(opening)
	stop()
	if e != nil {
		return errSession
	}
	defer ai.Close()
	aiSend := func(v any) error {
		data, e := json.Marshal(v)
		if e != nil {
			return e
		}
		c, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return ai.SendRaw(c, data)
	}
	twSend := func(v any) error {
		data, e := json.Marshal(v)
		if e != nil {
			return e
		}
		c, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return tw.Write(c, websocket.MessageText, data)
	}
	// Bounded context snapshot is fetched from the canonical history. Response IDs
	// from text Runs never become a Realtime session ID.
	history, e := b.history(ctx, r)
	if e != nil {
		return e
	}
	instructions := reminderInstructions(meeting, history)
	config := map[string]any{"type": "realtime", "model": b.Model, "instructions": instructions, "output_modalities": []string{"audio"}, "max_output_tokens": 1000, "tools": toolDefinitions(), "tool_choice": "auto", "audio": map[string]any{"input": map[string]any{"format": map[string]any{"type": "audio/pcmu"}, "transcription": map[string]any{"model": b.TranscriptionModel, "language": "pt"}, "turn_detection": map[string]any{"type": "server_vad", "threshold": 0.5, "prefix_padding_ms": 300, "silence_duration_ms": 600, "create_response": true, "interrupt_response": true}}, "output": map[string]any{"format": map[string]any{"type": "audio/pcmu"}, "voice": b.Voice}}}
	if e = aiSend(map[string]any{"type": "session.update", "session": config}); e != nil {
		return e
	}
	aiEvents := make(chan socketResult, 64)
	twEvents := make(chan socketResult, 64)
	go func() {
		for {
			raw, e := ai.RecvRaw(ctx)
			select {
			case aiEvents <- socketResult{raw, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	go func() {
		for {
			_, raw, e := tw.Read(ctx)
			select {
			case twEvents <- socketResult{raw, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	items := map[string]*audioItem{}
	marks := map[string]audioMark{}
	sequence := 0
	ready := false
	var earlyAudio []string
	earlyBytes := 0
	responseActive := false
	toolBusy := false
	toolCount := 0
	results := make(chan toolResult, 1)
	var pending *toolResult
	seenTools := map[string]bool{}
	closing := false
	awaitingClosingResponse := false
	voicemailPending := false
	var closeDeadline time.Time

	persist := func(id string) error {
		v := items[id]
		if v != nil && v.Done && !v.Cleared && !v.Stored && v.Text != "" && v.TotalMS > 0 && v.PlayedMS >= v.TotalMS {
			if e := b.Repo.Transcript(ctx, r, id, "assistant", v.Text); e != nil {
				return e
			}
			v.Stored = true
		}
		return nil
	}
	deliverTool := func(v toolResult) error {
		data, _ := json.Marshal(v.Value)
		if e := aiSend(map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "function_call_output", "call_id": v.ID, "output": string(data)}}); e != nil {
			return e
		}
		return aiSend(map[string]any{"type": "response.create"})
	}
	for {
		if closing && !awaitingClosingResponse && !responseActive && len(marks) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if closing && time.Now().After(closeDeadline) {
				return nil
			}
			if _, e = b.Repo.Active(ctx, r); e != nil {
				if errors.Is(e, ErrBlocked) {
					return nil
				}
				return e
			}
		case result := <-results:
			toolBusy = false
			if responseActive {
				pending = &result
			} else {
				if e = deliverTool(result); e != nil {
					return e
				}
			}
		case v := <-twEvents:
			if v.Err != nil {
				return nil
			}
			var event wireEvent
			if json.Unmarshal(v.Raw, &event) != nil {
				return errSession
			}
			if event.StreamSID != "" && event.StreamSID != stream {
				return errSession
			}
			switch event.Event {
			case "media":
				if closing || voicemailPending {
					continue
				}
				if event.Media.Track != "" && event.Media.Track != "inbound" {
					continue
				}
				decoded, e := base64.StdEncoding.DecodeString(event.Media.Payload)
				if e != nil || len(decoded) == 0 || len(decoded) > 64<<10 {
					return errSession
				}
				if !ready {
					earlyBytes += len(decoded)
					if earlyBytes > 80000 {
						return errSession
					}
					earlyAudio = append(earlyAudio, event.Media.Payload)
					continue
				}
				if e = aiSend(map[string]any{"type": "input_audio_buffer.append", "audio": event.Media.Payload}); e != nil {
					return e
				}
			case "mark":
				if m, ok := marks[event.Mark.Name]; ok {
					delete(marks, event.Mark.Name)
					if it := items[m.Item]; it != nil && !it.Cleared {
						it.PlayedMS = max(it.PlayedMS, m.EndMS)
						if e = persist(m.Item); e != nil {
							return e
						}
					}
				}
			case "stop":
				return nil
			}
		case v := <-aiEvents:
			if v.Err != nil {
				return errSession
			}
			var event aiEvent
			if json.Unmarshal(v.Raw, &event) != nil {
				return errSession
			}
			switch event.Type {
			case "session.created":
				if e = b.Repo.SetSession(ctx, r.ID, event.Session.ID); e != nil {
					return e
				}
			case "session.updated":
				if !ready {
					ready = true
					for _, audio := range earlyAudio {
						if e = aiSend(map[string]any{"type": "input_audio_buffer.append", "audio": audio}); e != nil {
							return e
						}
					}
					earlyAudio = nil
					if e = aiSend(map[string]any{"type": "response.create"}); e != nil {
						return e
					}
				}
			case "response.created":
				awaitingClosingResponse = false
				responseActive = true
			case "response.done":
				responseActive = false
				if voicemailPending {
					voicemailPending = false
					closing = true
					awaitingClosingResponse = true
					closeDeadline = time.Now().Add(30 * time.Second)
					if e = aiSend(map[string]any{"type": "response.create", "response": map[string]any{"instructions": voicemailInstructions(meeting), "tool_choice": "none"}}); e != nil {
						return e
					}
					continue
				}
				if pending != nil && !closing {
					if e = deliverTool(*pending); e != nil {
						return e
					}
					pending = nil
				}
			case "error":
				return errSession
			case "input_audio_buffer.speech_started":
				if closing || voicemailPending {
					continue
				}
				// Discard pending mark acknowledgements BEFORE clear: Twilio also echoes
				// marks for audio that clear removed, which does not prove it was heard.
				for id, it := range items {
					if !it.Stored && !it.Cleared && !(it.Done && it.PlayedMS >= it.TotalMS) {
						it.Cleared = true
						if it.TotalMS > 0 {
							if e = aiSend(map[string]any{"type": "conversation.item.truncate", "item_id": id, "content_index": 0, "audio_end_ms": min(it.PlayedMS, it.TotalMS)}); e != nil {
								return e
							}
						}
					}
				}
				clear(marks)
				if e = twSend(map[string]any{"event": "clear", "streamSid": stream}); e != nil {
					return e
				}
			case "response.output_audio.delta":
				it := items[event.ItemID]
				if it == nil {
					it = &audioItem{}
					items[event.ItemID] = it
				}
				if it.Cleared {
					continue
				}
				raw, e := base64.StdEncoding.DecodeString(event.Delta)
				if e != nil || len(raw) > 1<<20 {
					return errSession
				}
				it.TotalMS += len(raw) / 8
				if e = twSend(map[string]any{"event": "media", "streamSid": stream, "media": map[string]string{"payload": event.Delta}}); e != nil {
					return e
				}
				sequence++
				name := strconv.Itoa(sequence)
				marks[name] = audioMark{event.ItemID, it.TotalMS}
				if e = twSend(map[string]any{"event": "mark", "streamSid": stream, "mark": map[string]string{"name": name}}); e != nil {
					return e
				}
				if len(marks) > 1000 || len(items) > 300 {
					return errSession
				}
			case "response.output_audio.done":
				if it := items[event.ItemID]; it != nil {
					it.Done = true
					if e = persist(event.ItemID); e != nil {
						return e
					}
				}
			case "response.output_audio_transcript.done":
				it := items[event.ItemID]
				if it == nil {
					it = &audioItem{}
					items[event.ItemID] = it
				}
				it.Text = event.Transcript
				if e = persist(event.ItemID); e != nil {
					return e
				}
			case "conversation.item.input_audio_transcription.completed":
				if e = b.Repo.Transcript(ctx, r, event.ItemID, "user", event.Transcript); e != nil {
					return e
				}
				if domain.IsOptOut(event.Transcript) {
					_, e = b.Repo.DB.Submit(ctx, domain.Event{ID: "voice-stop-" + r.ID, LeadID: r.LeadID, Kind: "contact.opt_out", At: time.Now().UTC()}, r.Mode)
					if e != nil {
						return e
					}
					return nil
				}
			case "response.function_call_arguments.done":
				if seenTools[event.CallID] {
					continue
				}
				seenTools[event.CallID] = true
				if closing || voicemailPending {
					continue
				}
				if event.Name == "end_call" || event.Name == "voicemail_detection" {
					if toolBusy || pending != nil {
						return errSession
					}
					if _, e = b.Repo.Active(ctx, r); e != nil {
						return e
					}
					raw, _ := json.Marshal(map[string]string{"status": "ending"})
					if e = aiSend(map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "function_call_output", "call_id": event.CallID, "output": string(raw)}}); e != nil {
						return e
					}
					closeDeadline = time.Now().Add(30 * time.Second)
					if event.Name == "end_call" {
						closing = true
					} else {
						voicemailPending = true
					}
					continue
				}
				toolCount++
				if toolCount > 12 {
					return errSession
				}
				if toolBusy {
					return errSession
				}
				toolBusy = true
				go func(ev aiEvent) {
					result := b.Tools.Execute(ctx, r, ev.Name, ev.CallID, ev.Arguments)
					select {
					case results <- toolResult{ev.CallID, result}:
					case <-ctx.Done():
					}
				}(event)
			}
		}
	}
}
func (b *Bridge) history(ctx context.Context, r Request) ([]map[string]string, error) {
	rows, e := b.Repo.DB.Pool.Query(ctx, `SELECT m.role,m.body FROM bia_go.conversation_messages m JOIN bia_go.conversations c ON c.id=m.conversation_id WHERE c.lead_id=$1 AND c.mode=$2 ORDER BY m.seq DESC LIMIT 12`, r.LeadID, r.Mode)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []map[string]string
	size := 0
	for rows.Next() {
		var role, text string
		if e = rows.Scan(&role, &text); e != nil {
			return nil, e
		}
		if size+len(text) > 16000 {
			break
		}
		size += len(text)
		out = append(out, map[string]string{"role": role, "text": text})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
