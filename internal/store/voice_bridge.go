package store

import (
	"arkom/bia/internal/domain"
	"context"
)

func (d *DB) VoiceActive(ctx context.Context, lead string) (bool, error) {
	var active bool
	e := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.voice_requests WHERE lead_id=$1 AND mode='live' AND state IN ('dialing','queued','initiated','ringing','in-progress','uncertain'))`, lead).Scan(&active)
	return active, e
}

// VoiceBooking is retained as a fail-closed guard for older internal callers.
// Reminder calls cannot create or alter reservations.
func (d *DB) VoiceBooking(ctx context.Context, requestID, callID, slotID, evidence string, p domain.Policy) (string, error) {
	return "", ErrConflict
}
