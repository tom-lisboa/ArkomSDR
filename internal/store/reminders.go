package store

import (
	"arkom/bia/internal/domain"
	"context"
	"encoding/json"
	"time"
)

// ReconcileMeetingReminders backfills existing bookings and recovers from
// downtime. Stable IDs and ON CONFLICT prevent replay of completed/skipped jobs.
// It never changes ownership or promotes jobs originally created in shadow.
func (d *DB) ReconcileMeetingReminders(ctx context.Context, now time.Time, live bool) error {
	return d.reconcileReminders(ctx, now, live, domain.MeetingSequence)
}

func (d *DB) ReconcileSMSReminders(ctx context.Context, now time.Time, live bool) error {
	return d.reconcileReminders(ctx, now, live, domain.SMSSequence)
}

func (d *DB) reconcileReminders(ctx context.Context, now time.Time, live bool, sequence func(domain.State, time.Time) []domain.Effect) error {
	after := ""
	for {
		rows, err := d.Pool.Query(ctx, `SELECT id,state FROM bia_go.leads WHERE id>$1 AND state->'booking'->>'start' IS NOT NULL AND (state->'booking'->>'start')::timestamptz>$2 ORDER BY id LIMIT 100`, after, now)
		if err != nil {
			return err
		}
		var states []domain.State
		for rows.Next() {
			var id string
			var raw []byte
			var s domain.State
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &s); err != nil {
				rows.Close()
				return err
			}
			after = id
			states = append(states, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, s := range states {
			if s.Owner != "go" || s.OptedOut || s.Human || s.Paused || !s.Profile.Consent {
				continue
			}
			mode := "shadow"
			if live {
				mode = "live"
			}
			for _, fx := range sequence(s, now) {
				raw, _ := json.Marshal(fx)
				_, err = d.Pool.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'effect',$3,$4,$5,$6) ON CONFLICT DO NOTHING`, fx.ID, s.ID, mode, raw, hash(raw), fx.Due)
				if err != nil {
					return err
				}
			}
		}
		if len(states) < 100 {
			return nil
		}
	}
}
