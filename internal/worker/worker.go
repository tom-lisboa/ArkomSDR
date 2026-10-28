package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"arkom/bia/internal/agent"
	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"github.com/jackc/pgx/v5"
)

// CalendarReader keeps mutations out of the conversational executor.
type CalendarReader interface {
	Slots(context.Context, domain.State, time.Time) ([]domain.Slot, error)
}
type EffectHandler func(context.Context, store.Job, domain.State, domain.Effect, time.Time) (domain.State, []domain.Effect, any, error)
type EffectSchedule func(domain.State, domain.Effect, time.Time) (time.Time, string)

// SkipEffect records an expected policy outcome without claiming delivery or
// handing the entire conversation over to a human.
type SkipEffect struct{ Reason string }

func (s *SkipEffect) Error() string { return s.Reason }

type Worker struct {
	AfterSend   func(domain.State, domain.Effect, time.Time) []domain.Effect
	AudioSender interface {
		SendAudio(context.Context, string, domain.State, string, time.Time) (providers.Receipt, error)
	}
	VoiceAutoRequest bool
	VoiceSender      string
	Queue            string
	HandleEffect     EffectHandler
	Schedule         EffectSchedule
	DB               *store.DB
	Planner          agent.Planner
	Messenger        providers.Messenger
	Calendar         CalendarReader
	CRM              providers.CRM
	Policy           domain.Policy
	Live             bool
	PerLeadDaily     int
	GlobalHourly     int
	Now              func() time.Time
}

func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for i := 0; i < 20; i++ {
				worked, err := w.Once(ctx)
				if err != nil {
					slog.Error("worker step failed", "queue", w.Queue, "error_type", errorType(err))
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}

func errorType(e error) string {
	if errors.Is(e, store.ErrLease) {
		return "lease_lost"
	}
	return "storage_or_processing_error"
}

func (w *Worker) Once(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	defer cancel()
	opCtx, stopOperations := context.WithTimeout(ctx, 40*time.Second)
	defer stopOperations()
	queue := w.Queue
	if queue == "" {
		queue = "execution"
	}
	j, s, err := w.DB.ClaimQueue(ctx, queue)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	if j.Kind == "event" {
		var e domain.Event
		if err = json.Unmarshal(j.Payload, &e); err != nil {
			return true, w.DB.Fail(ctx, j, "invalid_event", false)
		}
		if e.Kind == "message.received" && !domain.IsOptOut(e.Text) {
			active, err := w.DB.VoiceActive(ctx, s.ID)
			if err != nil {
				return true, err
			}
			if active {
				return true, w.DB.DeferJob(ctx, j, now.Add(5*time.Second), "voice_session_active")
			}
		}
		if e.Context != nil {
			j.Run = &conversation.RunResult{ConversationID: e.Context.ConversationID, RunID: e.Context.RunID, Version: e.Context.Version}
		}
		var d domain.Decision
		var slots []domain.Slot
		if e.Kind == "message.received" && !s.OptedOut && !s.Human && !domain.AutomaticReply(e.Text) && (e.Context == nil || !e.Context.MediaFailed) {
			var calendarErr error
			slots, calendarErr = w.Calendar.Slots(opCtx, s, now)
			e.CalendarUnavailable = calendarErr != nil
			if domain.NeedsPlan(s, e, now) {
				id := agent.Select(s, e)
				var out agent.Outcome
				out, err = agent.Execute(opCtx, w.Planner, s, e, slots, id)
				d = out.Decision
				if j.Run != nil {
					j.Run.Agent = id
					j.Run.ResponseID = out.ResponseID
					j.Run.InputTokens = out.InputTokens
					j.Run.UsedPrevious = out.UsedPrevious
				}
			}
			if err != nil {
				d = domain.Decision{Action: "handoff", Reason: "model_unavailable"}
			}
			if calendarErr != nil && (d.Action == "offer_slots" || d.Action == "book" || d.Action == "reschedule") {
				d = domain.Decision{Action: "offer_slots", Reason: "calendar_unavailable"}
				slots = nil
			}
		}
		r := domain.Reduce(s, e, d, slots, w.Policy, now)
		return true, w.DB.Finish(ctx, j, r.State, r.Effects, "done", map[string]any{"decision": r.Decision, "effects": len(r.Effects)})
	}
	var fx domain.Effect
	if err = json.Unmarshal(j.Payload, &fx); err != nil {
		return true, w.DB.Fail(ctx, j, "invalid_effect", false)
	}
	if fx.Kind != "crm" && fx.VoiceRequestID == "" {
		active, e := w.DB.VoiceActive(ctx, s.ID)
		if e != nil {
			return true, e
		}
		if active {
			return true, w.DB.DeferJob(ctx, j, now.Add(5*time.Second), "voice_session_active")
		}
	}
	if fx.VoiceRequestID != "" {
		var allowed bool
		e := w.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.voice_requests WHERE id=$1 AND lead_id=$2 AND mode=$3 AND revoked_at IS NULL AND permission_expires_at>now() AND state IN ('dialing','queued','initiated','ringing','in-progress'))`, fx.VoiceRequestID, s.ID, j.Mode).Scan(&allowed)
		if e != nil {
			return true, e
		}
		if !allowed {
			return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": "voice_authorization_ended"})
		}
	}
	if ok, why := domain.Deliverable(s, fx, now); !ok {
		return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": why})
	}
	if fx.Kind != "crm" {
		conv, version := fx.ConversationID, fx.ConversationVersion
		if domain.IsMeetingReminder(fx) {
			conv, version = "", 0
		}
		pending, e := w.DB.HasNewInput(ctx, s.ID, conv, version)
		if e != nil {
			return true, e
		}
		if pending {
			if domain.IsMeetingReminder(fx) {
				return true, w.DB.DeferJob(ctx, j, now.Add(5*time.Second), "new_input_pending")
			}
			return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": "new_input_superseded_run"})
		}
	}
	if w.Schedule != nil {
		due, why := w.Schedule(s, fx, now)
		if why != "" {
			return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": why})
		}
		if due.After(now) {
			return true, w.DB.DeferJob(ctx, j, due, "outside_contact_hours")
		}
	}
	// An effect created in shadow can never become live after a restart/cutover.
	if j.Mode == "shadow" {
		return true, w.DB.Finish(ctx, j, s, nil, "simulated", map[string]any{"effect": fx, "text": domain.Render(s, fx, w.Policy, now), "external_call": false})
	}
	if !w.Live || s.Owner != "go" {
		return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": "go_does_not_own_contact"})
	}
	if fx.Kind != "crm" {
		pending, e := w.DB.PendingEvent(ctx, s.ID)
		if e != nil {
			return true, e
		}
		if pending {
			return true, w.DB.Fail(ctx, j, "new_event_pending", true)
		}
	}
	var result any
	var follow []domain.Effect
	switch fx.Kind {
	case "send", "send_audio":
		lead, global, e := w.DB.Counts(ctx, s.ID)
		if e != nil {
			return true, e
		}
		if lead > w.PerLeadDaily || global > w.GlobalHourly {
			return true, w.DB.Fail(ctx, j, "send_rate_limit", true)
		}
		text := domain.Render(s, fx, w.Policy, now)
		if fx.Purpose == "reply" && fx.VoiceRequestID == "" && fx.ReplyPart == 0 {
			parts := domain.SplitReply(text)
			if len(parts) > 1 {
				if lead+len(parts)-1 > w.PerLeadDaily || global+len(parts)-1 > w.GlobalHourly {
					return true, w.DB.Fail(ctx, j, "send_rate_limit", true)
				}
				first := fx
				first.ReplyRoot = j.ID
				first.ReplyPart = 1
				first.RemainingParts = parts[1:]
				first.ID = j.ID + ":part:1"
				first.Text = parts[0]
				first.Due = now
				return true, w.DB.Finish(ctx, j, s, []domain.Effect{first}, "done", map[string]any{"chunked": true, "parts": len(parts)})
			}
		}
		var receipt providers.Receipt
		if fx.Kind == "send_audio" {
			text = "[Áudio de lembrete da reunião enviado]"
			if w.AudioSender == nil {
				err = &providers.Failure{Code: "audio_sender_not_configured"}
			} else {
				receipt, err = w.AudioSender.SendAudio(opCtx, j.ID, s, fx.MediaURL, now)
			}
		} else {
			receipt, err = w.Messenger.Send(opCtx, j.ID, s, text, fx.Purpose, now)
		}
		if err == nil {
			result = receipt
			sentAt := time.Now().UTC()
			if w.Now != nil {
				sentAt = w.Now()
			}
			if w.AfterSend != nil {
				follow = append(follow, w.AfterSend(s, fx, sentAt)...)
			}
			if len(fx.RemainingParts) > 0 {
				next := fx
				next.ReplyPart++
				next.ID = fmt.Sprintf("%s:part:%d", fx.ReplyRoot, next.ReplyPart)
				next.Text = fx.RemainingParts[0]
				next.RemainingParts = fx.RemainingParts[1:]
				next.Due = sentAt.Add(domain.ReplyPause(next.Text))
				follow = append(follow, next)
				w.replyTyping(opCtx, s, fx.ConversationID)
			}
			now = sentAt
			j.Delivery = &conversation.Delivery{ConversationID: fx.ConversationID, ProviderConversationID: s.Profile.ConversationID, ID: "outbound/" + j.ID, Text: text, At: now}
			domain.AppendTurn(&s, "assistant", text, now)
			if s.FirstContactAt == nil {
				s.FirstContactAt = &now
			}
			if fx.Purpose == "welcome" {
				s.Stage = "contacted"
			}
			if fx.Purpose == "followup" {
				s.LastFollowup = &now
			}
			if fx.Purpose == "form_contact" {
				s.LastFormContactAt = &now
			}
		}
	case "crm":
		err = w.CRM.Record(opCtx, j.ID, s, fx)
		result = map[string]bool{"recorded": err == nil}
	default:
		if w.HandleEffect == nil {
			return true, w.DB.Fail(ctx, j, "unsupported_effect", false)
		}
		s, follow, result, err = w.HandleEffect(opCtx, j, s, fx, now)
	}
	if err != nil {
		var deferred *providers.Deferred
		if errors.As(err, &deferred) {
			return true, w.DB.DeferJob(ctx, j, now.Add(deferred.After), deferred.Code)
		}
		var skip *SkipEffect
		if errors.As(err, &skip) {
			return true, w.DB.Finish(ctx, j, s, nil, "skipped", map[string]string{"reason": skip.Reason})
		}
		f := providers.Classify(err)
		if f.Retryable && !f.Ambiguous {
			return true, w.DB.Fail(ctx, j, f.Code, true)
		}
		status := "dead"
		if f.Ambiguous {
			status = "uncertain"
		}
		// Never fabricate a success message after a timeout or unknown response.
		if fx.Kind != "crm" {
			s.Human = true
			s.Reasons = append(s.Reasons, f.Code)
			follow = []domain.Effect{{ID: j.ID + ":review", LeadID: s.ID, Kind: "crm", Purpose: "handoff", Text: "Revisar operação " + j.ID + ": " + f.Code, Due: now, Expires: now.Add(7 * 24 * time.Hour)}}
		}
		return true, w.DB.Finish(ctx, j, s, follow, status, map[string]string{"error_code": f.Code})
	}
	for i := range follow {
		follow[i].ConversationID = fx.ConversationID
		follow[i].ConversationVersion = fx.ConversationVersion
	}
	return true, w.DB.Finish(ctx, j, s, follow, "done", result)
}
