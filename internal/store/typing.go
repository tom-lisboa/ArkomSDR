package store

import (
	"context"
	"time"
)

type TypingTarget struct{ LeadID, Phone, MessageID, Body string }

// ClaimTyping uses one durable throttle across all context processes. It never
// keeps an indicator alive indefinitely for a stuck job or an old webhook replay.
func (d *DB) ClaimTyping(ctx context.Context, now time.Time, allowedPhone string) (TypingTarget, error) {
	var out TypingTarget
	e := d.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT c.id, c.lead_id, l.state->'profile'->>'phone' phone, m.id message_id, m.body
 FROM bia_go.conversations c JOIN bia_go.leads l ON l.id=c.lead_id
 CROSS JOIN LATERAL (SELECT id,body,occurred_at FROM bia_go.conversation_messages WHERE conversation_id=c.id AND role='user' AND id LIKE 'twilio-%' ORDER BY occurred_at DESC,seq DESC LIMIT 1) m
 WHERE c.mode='live' AND (c.first_pending_at IS NOT NULL OR c.active_run IS NOT NULL)
 AND (c.typing_at IS NULL OR c.typing_at<=$1::timestamptz-interval '15 seconds')
 AND m.occurred_at>$1::timestamptz-interval '3 minutes' AND m.occurred_at<=$1
 AND l.state->>'owner'='go' AND l.state->'profile'->>'consent'='true'
 AND COALESCE(l.state->>'opted_out','false')='false' AND COALESCE(l.state->>'human','false')='false' AND COALESCE(l.state->>'paused','false')='false'
 AND ($2='' OR l.state->'profile'->>'phone'=$2)
 ORDER BY c.typing_at NULLS FIRST,c.id LIMIT 1 FOR UPDATE OF c SKIP LOCKED
 ) UPDATE bia_go.conversations c SET typing_at=$1 FROM candidate x WHERE c.id=x.id RETURNING x.lead_id,x.phone,x.message_id,x.body`, now, allowedPhone).Scan(&out.LeadID, &out.Phone, &out.MessageID, &out.Body)
	return out, e
}
