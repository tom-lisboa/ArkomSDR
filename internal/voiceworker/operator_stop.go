package voiceworker

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Stop requests are durable and independent of permission/dial leases. A lost
// provider response is visible as uncertain; never blindly repeat a mutation.
func (w *Worker) stopRequested(ctx context.Context) (bool, error) {
	if !w.Live || w.Provider == nil {
		return false, nil
	}
	if _, err := w.Repo.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET stop_state='uncertain',error_code='operator_stop_uncertain' WHERE stop_state='running' AND stop_lease_until<now()`); err != nil {
		return false, err
	}
	var id, call string
	err := w.Repo.DB.Pool.QueryRow(ctx, `UPDATE bia_go.voice_requests SET stop_state='running',stop_lease_until=now()+interval '45 seconds' WHERE id=(SELECT id FROM bia_go.voice_requests WHERE mode='live' AND stop_state='pending' AND call_sid IS NOT NULL ORDER BY updated_at,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,call_sid`).Scan(&id, &call)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state := "done"
	code := "operator_interrupted"
	if err = w.Provider.Hangup(ctx, call); err != nil {
		state = "uncertain"
		code = "operator_stop_uncertain"
	}
	_, err = w.Repo.DB.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET stop_state=$2,stop_lease_until=NULL,error_code=$3,updated_at=now() WHERE id=$1 AND stop_state='running'`, id, state, code)
	return true, err
}
