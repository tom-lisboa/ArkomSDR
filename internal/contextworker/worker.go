package contextworker

import (
	"arkom/bia/internal/store"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"strconv"
	"time"
)

func formatMillis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

type Worker struct {
	Typing    TypingSender
	TestPhone string
	DB        *store.DB
	Buffer    *Buffer
	Policy    store.WindowPolicy
	Now       func() time.Time
}

func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for i := 0; i < 100; i++ {
				worked, e := w.Once(ctx)
				if e != nil {
					slog.Error("context worker step failed")
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}
func (w *Worker) cache(ctx context.Context, h store.BufferHint) {
	if w.Buffer == nil || h.ID == "" {
		return
	}
	c, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	var e error
	if h.Due.IsZero() {
		e = w.Buffer.Clear(c, h)
	} else {
		e = w.Buffer.Touch(c, h)
	}
	if e != nil {
		slog.Debug("redis unavailable; durable context queue remains active")
	}
}
func (w *Worker) Once(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	t, e := w.DB.ClaimTask(ctx, "context")
	if e == nil {
		h, err := w.DB.ApplyContext(ctx, t, w.Policy, now)
		if err != nil {
			return true, w.DB.FailTask(ctx, t, "context_processing_failed")
		}
		w.cache(ctx, h)
		// Also flush during sustained inbound traffic, so MaxWait is enforced.
		h, err = w.DB.FlushWindow(ctx, "", w.Policy, now)
		if err == nil {
			w.cache(ctx, h)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return true, err
		}
		return true, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return false, e
	}
	candidate := ""
	if w.Buffer != nil {
		c, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		candidate, _ = w.Buffer.Ready(c, now)
		stop()
	}
	h, e := w.DB.FlushWindow(ctx, candidate, w.Policy, now)
	if errors.Is(e, pgx.ErrNoRows) && candidate != "" {
		h, e = w.DB.FlushWindow(ctx, "", w.Policy, now)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	w.cache(ctx, h)
	return true, nil
}
