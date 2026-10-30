package store

import (
	"context"
	"encoding/json"
	"time"

	"arkom/bia/internal/domain"
)

func (d *DB) ReconcileProactive(ctx context.Context, now time.Time, live bool, p domain.Policy) error {
	if p.MeetingCheckins {
		if err := d.reconcileReminders(ctx, now, live, func(s domain.State, t time.Time) []domain.Effect { return domain.MeetingCheckinSequence(s, p, t) }, 15*time.Minute); err != nil {
			return err
		}
	}
	if !p.NoReplyEnabled || p.NoReplySince.IsZero() {
		return nil
	}
	after := ""
	mode := "shadow"
	if live {
		mode = "live"
	}
	for {
		rows, err := d.Pool.Query(ctx, `SELECT id,state FROM bia_go.leads WHERE id>$1 AND COALESCE((state->>'last_form_contact_at')::timestamptz,(state->>'first_contact_at')::timestamptz)>=$2 AND COALESCE(state->'booking','null'::jsonb)='null'::jsonb ORDER BY id LIMIT 100`, after, p.NoReplySince)
		if err != nil {
			return err
		}
		states := []domain.State{}
		for rows.Next() {
			var raw []byte
			var s domain.State
			if err = rows.Scan(&after, &raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &s); err != nil {
				rows.Close()
				return err
			}
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
			for _, fx := range domain.NoReplySequence(s, p, now) {
				raw, _ := json.Marshal(fx)
				if _, err = d.Pool.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'effect',$3,$4,$5,$6) ON CONFLICT DO NOTHING`, fx.ID, s.ID, mode, raw, hash(raw), fx.Due); err != nil {
					return err
				}
			}
		}
		if len(states) < 100 {
			return nil
		}
	}
}
