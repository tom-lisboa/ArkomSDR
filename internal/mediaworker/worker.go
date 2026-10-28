package mediaworker

import (
	"arkom/bia/internal/conversation"
	"arkom/bia/internal/store"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"time"
)

type Processor interface {
	Process(context.Context, conversation.Attachment) (string, error)
}
type Worker struct {
	DB        *store.DB
	Processor Processor
}

func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, e := w.Once(ctx); e != nil {
				slog.Error("media worker step failed")
			}
		}
	}
}
func (w *Worker) Once(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	t, e := w.DB.ClaimTask(ctx, "media")
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var req conversation.MediaRequest
	if json.Unmarshal(t.Payload, &req) != nil {
		return true, w.DB.FailTask(ctx, t, "invalid_media_request")
	}
	r := conversation.MediaResult{MediaRequest: req}
	if w.Processor == nil {
		r.ErrorCode = "media_processor_not_configured"
	} else {
		r.Text, e = w.Processor.Process(ctx, req.Attachment)
		if e != nil {
			return true, w.DB.FailTask(parent, t, "media_processing_failed")
		}
		if r.Text == "" || len(r.Text) > 16000 {
			r.Text = ""
			r.ErrorCode = "media_unusable"
		}
	}
	return true, w.DB.CompleteMedia(parent, t, r)
}
