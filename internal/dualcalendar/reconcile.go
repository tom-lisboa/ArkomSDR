package dualcalendar

import (
	"context"
	"log/slog"
	"time"
)

// Reconcile recovers an accepted event after a timeout or crash. It may finish
// an existing cancellation, but only a valid scheduling job can create an event.
func (c *Service) Reconcile(ctx context.Context) error {
	rows, err := c.Pool.Query(ctx, `SELECT operation_id FROM bia_go.google_booking_ops WHERE status='pending' AND updated_at<now()-interval '30 seconds' ORDER BY updated_at LIMIT 10`)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			break
		}
		keys = append(keys, key)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		_, _ = c.resume(ctx, key, false)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// RunReconciliation runs once per scheduling process, independently of its job workers.
func (c *Service) RunReconciliation(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			op, cancel := context.WithTimeout(ctx, 40*time.Second)
			err := c.Reconcile(op)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("Google calendar reconciliation pending")
			}
		}
	}
}
