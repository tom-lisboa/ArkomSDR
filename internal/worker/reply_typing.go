package worker

import (
	"arkom/bia/internal/domain"
	"context"
	"log/slog"
	"strings"
	"time"
)

func (w *Worker) replyTyping(parent context.Context, s domain.State, conversation string) {
	sender, ok := w.Messenger.(interface {
		Typing(context.Context, string, string) error
	})
	if !ok || conversation == "" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var id string
	err := w.DB.Pool.QueryRow(ctx, `SELECT m.id FROM bia_go.conversation_messages m JOIN bia_go.conversations c ON c.id=m.conversation_id WHERE c.id=$1 AND c.lead_id=$2 AND m.role='user' AND m.id LIKE 'twilio-%' AND m.occurred_at>now()-interval '23 hours' ORDER BY m.occurred_at DESC,m.seq DESC LIMIT 1`, conversation, s.ID).Scan(&id)
	if err == nil {
		if sender.Typing(ctx, s.Profile.Phone, strings.TrimPrefix(id, "twilio-")) != nil {
			slog.Debug("reply typing unavailable")
		}
	}
}
