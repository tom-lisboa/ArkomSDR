package store

import (
	"context"
	"time"
)

// DeferJob reschedules a policy delay without spending the provider retry budget.
// The same job ID is retained; no duplicate delivery is created.
func (d *DB) DeferJob(ctx context.Context, j Job, due time.Time, reason string) error {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	ct, e := tx.Exec(ctx, `UPDATE bia_go.leads SET lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now()`, j.LeadID, j.Token)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	ct, e = tx.Exec(ctx, `UPDATE bia_go.jobs SET status='pending',due_at=$3,error_code=$4,lease_token=NULL,attempts=GREATEST(attempts-1,0) WHERE id=$1 AND lease_token=$2`, j.ID, j.Token, due, reason)
	if e != nil {
		return e
	}
	if ct.RowsAffected() != 1 {
		return ErrLease
	}
	return tx.Commit(ctx)
}
