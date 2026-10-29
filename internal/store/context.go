package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
)

type WindowPolicy struct {
	Quiet, MediaQuiet, MediaSettle, MaxWait, MediaTimeout, HeadTTL time.Duration
	MaxHistoryBytes, MaxHistoryMessages, MaxChainDepth             int
	MaxInputTokens                                                 int64
}

func DefaultWindowPolicy() WindowPolicy {
	return WindowPolicy{Quiet: 5 * time.Second, MediaQuiet: 12 * time.Second, MediaSettle: 3 * time.Second, MaxWait: 30 * time.Second, MediaTimeout: 120 * time.Second, HeadTTL: 20 * time.Hour, MaxHistoryBytes: 24000, MaxHistoryMessages: 20, MaxChainDepth: 8, MaxInputTokens: 16000}
}

type BufferHint struct {
	ID, MessageID string
	Version       int64
	Due           time.Time
}

func AssetID(message, attachment string) string {
	return "asset_" + hash([]byte(message + "\x00" + attachment))[:32]
}
func conversationID(e domain.Event, mode string) string {
	return "conv_" + hash([]byte(e.LeadID + "\x00" + mode + "\x00" + e.ConversationID))[:32]
}
func dueAt(first, last time.Time, p WindowPolicy) time.Time {
	d := last.Add(p.Quiet)
	if max := first.Add(p.MaxWait); d.After(max) {
		d = max
	}
	return d
}

// ApplyContext is the sole writer of canonical conversation history and heads.
func (d *DB) ApplyContext(ctx context.Context, t Task, p WindowPolicy, now time.Time) (BufferHint, error) {
	var hint BufferHint
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return hint, e
	}
	defer tx.Rollback(ctx)
	if e = ackTask(ctx, tx, t); e != nil {
		return hint, e
	}
	switch t.Kind {
	case "inbound":
		var ev domain.Event
		if e = json.Unmarshal(t.Payload, &ev); e != nil {
			return hint, e
		}
		id := conversationID(ev, t.Mode)
		hint.ID = id
		hint.MessageID = ev.ID
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversations(id,lead_id,mode) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, t.LeadID, t.Mode)
		if e != nil {
			return hint, e
		}
		var first, last *time.Time
		e = tx.QueryRow(ctx, `SELECT version,first_pending_at,last_pending_at FROM bia_go.conversations WHERE id=$1 FOR UPDATE`, id).Scan(&hint.Version, &first, &last)
		if e != nil {
			return hint, e
		}
		ct, e := tx.Exec(ctx, `INSERT INTO bia_go.conversation_messages(id,conversation_id,role,body,event,occurred_at) VALUES($1,$2,'user',$3,$4,$5) ON CONFLICT DO NOTHING`, ev.ID, id, ev.Text, t.Payload, ev.At)
		if e != nil {
			return hint, e
		}
		if ct.RowsAffected() == 0 {
			return hint, tx.Commit(ctx)
		}
		if first == nil {
			first = &now
		}
		hint.Version++
		hint.Due = dueAt(*first, now, p)
		if ev.AudioURL != "" {
			ev.Attachments = append(ev.Attachments, conversation.Attachment{ID: "legacy-audio", Kind: "audio", URL: ev.AudioURL})
		}
		for _, a := range ev.Attachments {
			asset := AssetID(ev.ID, a.ID)
			_, e = tx.Exec(ctx, `INSERT INTO bia_go.media_assets(id,conversation_id,message_id,kind,source_url) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, asset, id, ev.ID, a.Kind, a.URL)
			if e != nil {
				return hint, e
			}
			if e = enqueueTask(ctx, tx, "media/"+asset, "media", "media.request", t.LeadID, t.Mode, conversation.MediaRequest{ConversationID: id, MessageID: ev.ID, Attachment: a}); e != nil {
				return hint, e
			}
		}
		var hasMedia bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.media_assets a JOIN bia_go.conversation_messages m ON m.id=a.message_id WHERE a.conversation_id=$1 AND m.run_id IS NULL)`, id).Scan(&hasMedia)
		if e != nil {
			return hint, e
		}
		batchPolicy := p
		if hasMedia && p.MediaQuiet > p.Quiet {
			batchPolicy.Quiet = p.MediaQuiet
		}
		hint.Due = dueAt(*first, now, batchPolicy)
		_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET version=$2,first_pending_at=$3,last_pending_at=$4,flush_at=$5,updated_at=now() WHERE id=$1`, id, hint.Version, first, now, hint.Due)
		if e != nil {
			return hint, e
		}
	case "media.completed":
		var r conversation.MediaResult
		if e = json.Unmarshal(t.Payload, &r); e != nil {
			return hint, e
		}
		hint.ID = r.ConversationID
		var first, last *time.Time
		e = tx.QueryRow(ctx, `SELECT version,first_pending_at,last_pending_at FROM bia_go.conversations WHERE id=$1 FOR UPDATE`, hint.ID).Scan(&hint.Version, &first, &last)
		if e != nil {
			return hint, e
		}
		status := "ready"
		if r.ErrorCode != "" {
			status = "failed"
		}
		_, e = tx.Exec(ctx, `UPDATE bia_go.media_assets SET status=$2,body=$3,error_code=$4 WHERE id=$1 AND status='pending'`, AssetID(r.MessageID, r.Attachment.ID), status, r.Text, r.ErrorCode)
		if e != nil {
			return hint, e
		}
		if first != nil && last != nil {
			batchPolicy := p
			if p.MediaQuiet > p.Quiet {
				batchPolicy.Quiet = p.MediaQuiet
			}
			hint.Due = dueAt(*first, *last, batchPolicy)
			// Completion of the final transcript still leaves a short grace
			// period for another fragment already on its way from WhatsApp.
			if settle := now.Add(p.MediaSettle); settle.After(hint.Due) {
				hint.Due = settle
			}
			_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET flush_at=$2 WHERE id=$1`, hint.ID, hint.Due)
			if e != nil {
				return hint, e
			}
		}
	case "run.completed":
		var r conversation.RunResult
		if e = json.Unmarshal(t.Payload, &r); e != nil {
			return hint, e
		}
		var active *string
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT active_run,heads FROM bia_go.conversations WHERE id=$1 FOR UPDATE`, r.ConversationID).Scan(&active, &raw)
		if e != nil {
			return hint, e
		}
		if active == nil || *active != r.RunID {
			return hint, tx.Commit(ctx)
		}
		var version int64
		e = tx.QueryRow(ctx, `SELECT version FROM bia_go.conversation_runs WHERE id=$1`, r.RunID).Scan(&version)
		if e != nil {
			return hint, e
		}
		if version != r.Version {
			return hint, fmt.Errorf("run_version_mismatch")
		}
		heads := map[string]conversation.Head{}
		if e = json.Unmarshal(raw, &heads); e != nil {
			return hint, e
		}
		if r.Agent != "" {
			if r.Status == "done" && r.ResponseID != "" {
				depth := 1
				if r.UsedPrevious {
					depth = heads[r.Agent].Depth + 1
				}
				heads[r.Agent] = conversation.Head{PromptVersion: r.PromptVersion, ResponseID: r.ResponseID, At: now, Depth: depth, InputTokens: r.InputTokens}
			} else {
				delete(heads, r.Agent)
			}
		}
		raw, _ = json.Marshal(heads)
		_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET active_run=NULL,heads=$2 WHERE id=$1`, r.ConversationID, raw)
		if e != nil {
			return hint, e
		}
		_, e = tx.Exec(ctx, `UPDATE bia_go.conversation_runs SET status=$2,agent=$3,response_id=$4,input_tokens=$5,completed_at=now() WHERE id=$1`, r.RunID, r.Status, r.Agent, r.ResponseID, r.InputTokens)
		if e != nil {
			return hint, e
		}
	case "voice.transcript":
		var v struct {
			ID        string    `json:"id"`
			Role      string    `json:"role"`
			Text      string    `json:"text"`
			RequestID string    `json:"request_id"`
			At        time.Time `json:"at"`
		}
		if e = json.Unmarshal(t.Payload, &v); e != nil {
			return hint, e
		}
		if v.Role != "user" && v.Role != "assistant" {
			return hint, fmt.Errorf("invalid_voice_role")
		}
		var providerConversation string
		e = tx.QueryRow(ctx, `SELECT COALESCE(state->'profile'->>'conversation_id','') FROM bia_go.leads WHERE id=$1`, t.LeadID).Scan(&providerConversation)
		if e != nil {
			return hint, e
		}
		id := conversationID(domain.Event{LeadID: t.LeadID, ConversationID: providerConversation}, t.Mode)
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversations(id,lead_id,mode) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, t.LeadID, t.Mode)
		if e != nil {
			return hint, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversation_messages(id,conversation_id,role,body,occurred_at,run_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, v.ID, id, v.Role, v.Text, v.At, "voice/"+v.RequestID)
		if e != nil {
			return hint, e
		}

	case "delivery.completed":
		var r conversation.Delivery
		if e = json.Unmarshal(t.Payload, &r); e != nil {
			return hint, e
		}
		if r.ConversationID == "" {
			r.ConversationID = conversationID(domain.Event{LeadID: t.LeadID, ConversationID: r.ProviderConversationID}, t.Mode)
		}
		_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversations(id,lead_id,mode) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.ConversationID, t.LeadID, t.Mode)
		if e != nil {
			return hint, e
		}

		_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversation_messages(id,conversation_id,role,body,occurred_at,run_id) VALUES($1,$2,'assistant',$3,$4,'delivered') ON CONFLICT DO NOTHING`, r.ID, r.ConversationID, r.Text, r.At)
		if e != nil {
			return hint, e
		}
	default:
		return hint, fmt.Errorf("unknown_context_task")
	}
	return hint, tx.Commit(ctx)
}

// FlushWindow atomically freezes pending messages and publishes ONE execution
// job. New fragments are left for the next run, and cannot overwrite this one.
func (d *DB) FlushWindow(ctx context.Context, candidate string, p WindowPolicy, now time.Time) (BufferHint, error) {
	var h BufferHint
	var lead, mode string
	var headsRaw []byte
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return h, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT id,lead_id,mode,version,heads FROM bia_go.conversations
 WHERE active_run IS NULL AND flush_at<=$1 AND ($2='' OR id=$2) AND NOT EXISTS(SELECT 1 FROM bia_go.worker_tasks t WHERE t.lead_id=conversations.lead_id AND t.mode=conversations.mode AND t.kind='inbound' AND t.status IN ('pending','running')) ORDER BY flush_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now, candidate).Scan(&h.ID, &lead, &mode, &h.Version, &headsRaw)
	if e != nil {
		return h, e
	}
	// A stuck media job becomes an explicit failed input, never a silent drop.
	_, e = tx.Exec(ctx, `UPDATE bia_go.media_assets SET status='failed',error_code='media_timeout' WHERE conversation_id=$1 AND status='pending' AND created_at<=$2`, h.ID, now.Add(-p.MediaTimeout))
	if e != nil {
		return h, e
	}
	var pending int
	var earliest *time.Time
	e = tx.QueryRow(ctx, `SELECT count(*),min(a.created_at) FROM bia_go.media_assets a JOIN bia_go.conversation_messages m ON m.id=a.message_id WHERE a.conversation_id=$1 AND a.status='pending' AND m.run_id IS NULL`, h.ID).Scan(&pending, &earliest)
	if e != nil {
		return h, e
	}
	if pending > 0 {
		h.Due = earliest.Add(p.MediaTimeout)
		_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET flush_at=$2 WHERE id=$1`, h.ID, h.Due)
		if e != nil {
			return h, e
		}
		return h, tx.Commit(ctx)
	}
	rows, e := tx.Query(ctx, `SELECT m.id,m.body,m.event,m.occurred_at,
 COALESCE((SELECT string_agg('['||a.kind||'] '||a.body,E'\n' ORDER BY a.id) FROM bia_go.media_assets a WHERE a.message_id=m.id AND a.status='ready'),''),
 EXISTS(SELECT 1 FROM bia_go.media_assets a WHERE a.message_id=m.id AND a.status='failed')
 FROM bia_go.conversation_messages m WHERE conversation_id=$1 AND role='user' AND run_id IS NULL ORDER BY occurred_at,seq LIMIT 32`, h.ID)
	if e != nil {
		return h, e
	}
	var buttonCount int
	var buttonPayload string
	var ev domain.Event
	var ids []string
	var body strings.Builder
	failed := false
	for rows.Next() {
		var id, text, media string
		var raw []byte
		var at time.Time
		var mf bool
		if e = rows.Scan(&id, &text, &raw, &at, &media, &mf); e != nil {
			rows.Close()
			return h, e
		}
		part := strings.TrimSpace(text + "\n" + media)
		if len(ids) > 0 && body.Len()+len(part) > 16000 {
			break
		}
		ev = domain.Event{}
		if e = json.Unmarshal(raw, &ev); e != nil {
			rows.Close()
			return h, e
		}
		if ev.ButtonPayload != "" {
			buttonCount++
			buttonPayload = ev.ButtonPayload
		}
		if len(part) > 16000 {
			part = "[conteúdo acima do limite; revisão necessária]"
			mf = true
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(part)
		ids = append(ids, id)
		failed = failed || mf
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return h, e
	}
	if len(ids) == 0 {
		_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET flush_at=NULL,first_pending_at=NULL,last_pending_at=NULL WHERE id=$1`, h.ID)
		if e != nil {
			return h, e
		}
		return h, tx.Commit(ctx)
	}
	runID := "run/" + hash([]byte(strings.Join(ids, "\x00")))[:32]
	window := conversation.Window{ConversationID: h.ID, RunID: runID, Version: h.Version, MessageIDs: ids, MediaFailed: failed, Heads: map[string]conversation.Head{}}
	if e = json.Unmarshal(headsRaw, &window.Heads); e != nil {
		return h, e
	}
	for k, v := range window.Heads {
		if now.Sub(v.At) > p.HeadTTL || v.Depth >= p.MaxChainDepth || v.InputTokens >= p.MaxInputTokens {
			delete(window.Heads, k)
		}
	}
	history, e := tx.Query(ctx, `SELECT role,body,occurred_at FROM bia_go.conversation_messages WHERE conversation_id=$1 AND run_id IS NOT NULL ORDER BY seq DESC LIMIT $2`, h.ID, p.MaxHistoryMessages)
	if e != nil {
		return h, e
	}
	size := 0
	for history.Next() {
		var m conversation.Message
		if e = history.Scan(&m.Role, &m.Text, &m.At); e != nil {
			history.Close()
			return h, e
		}
		if size+len(m.Text) > p.MaxHistoryBytes {
			break
		}
		size += len(m.Text)
		window.History = append(window.History, m)
	}
	history.Close()
	if e = history.Err(); e != nil {
		return h, e
	}
	for i, j := 0, len(window.History)-1; i < j; i, j = i+1, j-1 {
		window.History[i], window.History[j] = window.History[j], window.History[i]
	}
	// Multiple messages must not execute a positional/confirmation shortcut.
	if buttonCount > 0 && len(ids) > 1 {
		ev.ButtonPayload = "mixed"
	} else {
		ev.ButtonPayload = buttonPayload
	}
	ev.ID = runID
	ev.Text = body.String()
	ev.Context = &window
	ev.AudioURL = ""
	ev.Attachments = nil
	if failed {
		ev.SelectedSlotID = ""
	}
	raw, _ := json.Marshal(ev)
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,$6) ON CONFLICT DO NOTHING`, runID, lead, mode, raw, hash(raw), now)
	if e != nil {
		return h, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.conversation_runs(id,conversation_id,version) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, runID, h.ID, h.Version)
	if e != nil {
		return h, e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.conversation_messages SET run_id=$2 WHERE id=ANY($1)`, ids, runID)
	if e != nil {
		return h, e
	}
	// Keep the enriched media text in canonical history after freezing the turn.
	_, e = tx.Exec(ctx, `UPDATE bia_go.conversation_messages m SET body=m.body||CASE WHEN x.body IS NULL THEN '' ELSE E'\n'||x.body END FROM (SELECT message_id,string_agg('['||kind||'] '||body,E'\n' ORDER BY id) body FROM bia_go.media_assets WHERE message_id=ANY($1) AND status='ready' GROUP BY message_id) x WHERE m.id=x.message_id`, ids)
	if e != nil {
		return h, e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.conversations SET active_run=$2,first_pending_at=(SELECT min(received_at) FROM bia_go.conversation_messages WHERE conversation_id=$1 AND role='user' AND run_id IS NULL),last_pending_at=(SELECT max(received_at) FROM bia_go.conversation_messages WHERE conversation_id=$1 AND role='user' AND run_id IS NULL),flush_at=CASE WHEN EXISTS(SELECT 1 FROM bia_go.conversation_messages WHERE conversation_id=$1 AND role='user' AND run_id IS NULL) THEN $3::timestamptz ELSE NULL END WHERE id=$1`, h.ID, runID, now)
	if e != nil {
		return h, e
	}
	h.MessageID = runID
	h.Due = time.Time{}
	return h, tx.Commit(ctx)
}
