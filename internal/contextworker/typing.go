package contextworker

import (
	"arkom/bia/internal/domain"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"strings"
	"time"
)

type TypingSender interface {
	Typing(context.Context, string, string) error
}

// A separate loop keeps media downloads and model latency from blocking the
// context inbox. Cosmetic failures never retry inbound messages or model runs.
func (w *Worker) RunTyping(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if w.Typing == nil {
				continue
			}
			for range 10 {
				c, cancel := context.WithTimeout(ctx, 2*time.Second)
				target, e := w.DB.ClaimTyping(c, time.Now().UTC(), w.TestPhone)
				if e != nil {
					cancel()
					if !errors.Is(e, pgx.ErrNoRows) {
						slog.Warn("typing target unavailable")
					}
					break
				}
				active, err := w.DB.VoiceActive(c, target.LeadID)
				if err == nil && !active && !domain.IsOptOut(target.Body) {
					if err = w.Typing.Typing(c, target.Phone, strings.TrimPrefix(target.MessageID, "twilio-")); err != nil {
						slog.Warn("typing indicator unavailable")
					} else {
						slog.Info("typing indicator accepted")
					}
				}
				cancel()
			}
		}
	}
}
