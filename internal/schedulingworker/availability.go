package schedulingworker

import (
	"context"
	"log/slog"
	"time"

	"arkom/bia/internal/providers"
)

// RunAvailability maintains the rolling calendar independently of voice calls,
// conversation turns and booking mutations. Only the scheduling role starts it.
func (w *Worker) RunAvailability(ctx context.Context, settings providers.DailyAvailability) {
	if !settings.Enabled {
		return
	}
	calendar, ok := w.calendar.(*providers.LocalCalendar)
	if !ok {
		return
	}
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for {
		op, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := calendar.EnsureAvailability(op, settings, time.Now().UTC())
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("local calendar availability refresh failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
