package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LocalCalendar is a Go-owned calendar. Its slots and reservations are isolated
// from n8n, Cal and ClickMax. No Google invitation is sent by this adapter.
type LocalCalendar struct{ Pool *pgxpool.Pool }
type LocalSlot struct {
	domain.Slot
	MeetingURL string `json:"meeting_url"`
}

func (c *LocalCalendar) Add(ctx context.Context, slot LocalSlot) error {
	if !domain.ValidID(slot.ID) || !slot.Start.After(time.Now()) || !slot.End.After(slot.Start) || slot.CloserID <= 0 || (slot.MeetingURL != "" && !domain.ValidHTTPS(slot.MeetingURL)) {
		return &Failure{Code: "invalid_local_slot"}
	}
	tx, e := c.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Per-host advisory lock prevents overlapping slots even across instances.
	_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, slot.CloserID)
	if e != nil {
		return e
	}
	var overlaps bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.slots WHERE closer_id=$1 AND starts_at<$3 AND ends_at>$2)`, slot.CloserID, slot.Start, slot.End).Scan(&overlaps)
	if e != nil {
		return e
	}
	if overlaps {
		return &Failure{Code: "slot_overlaps"}
	}
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.slots(id,starts_at,ends_at,closer_id,meeting_url) VALUES($1,$2,$3,$4,$5)`, slot.ID, slot.Start, slot.End, slot.CloserID, slot.MeetingURL)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (c *LocalCalendar) Slots(ctx context.Context, s domain.State, now time.Time) ([]domain.Slot, error) {
	var closer int64
	if s.Booking != nil && s.Booking.Outcome == "" {
		if s.Booking.Provider != "local" || s.Booking.CloserID == 0 {
			return nil, &Failure{Code: "local_booking_provider_mismatch"}
		}
		closer = s.Booking.CloserID
	}
	rows, e := c.Pool.Query(ctx, `SELECT id,starts_at,ends_at,closer_id FROM bia_go.slots WHERE reserved_by IS NULL AND starts_at>$1 AND ($2::bigint=0 OR closer_id=$2) ORDER BY starts_at LIMIT 100`, now.Add(30*time.Minute), closer)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Slot{}
	for rows.Next() {
		v := domain.Slot{Provider: "local"}
		if e = rows.Scan(&v.ID, &v.Start, &v.End, &v.CloserID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (c *LocalCalendar) Book(ctx context.Context, key string, s domain.State, slot domain.Slot, old *domain.Booking) (domain.Booking, error) {
	var b domain.Booking
	tx, e := c.Pool.Begin(ctx)
	if e != nil {
		return b, e
	}
	defer tx.Rollback(ctx)
	b, e = BookLocalTx(ctx, tx, key, s, slot, old)
	if e != nil {
		return b, e
	}
	return b, tx.Commit(ctx)
}

// BookLocalTx lets the form atomically reserve and enqueue the booking event.
// The caller owns commit/rollback; all calendar locking rules remain shared.
func BookLocalTx(ctx context.Context, tx pgx.Tx, key string, s domain.State, slot domain.Slot, old *domain.Booking) (domain.Booking, error) {
	var b domain.Booking
	var e error
	// Serialize all bookings for this identity, including different slots.
	_, e = tx.Exec(ctx, `SELECT id FROM bia_go.leads WHERE id=$1 FOR UPDATE`, s.ID)
	if e != nil {
		return b, e
	}
	var raw []byte
	e = tx.QueryRow(ctx, `SELECT payload FROM bia_go.bookings WHERE operation_id=$1`, key).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &b)
		return b, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return b, e
	}
	if slot.Provider != "local" || slot.Group {
		return b, &Failure{Code: "invalid_local_booking"}
	}
	var reserved *string
	var meet string
	var start, end time.Time
	var closer int64
	e = tx.QueryRow(ctx, `SELECT starts_at,ends_at,closer_id,meeting_url,reserved_by FROM bia_go.slots WHERE id=$1 FOR UPDATE`, slot.ID).Scan(&start, &end, &closer, &meet, &reserved)
	if errors.Is(e, pgx.ErrNoRows) {
		return b, &Failure{Code: "slot_not_found"}
	}
	if e != nil {
		return b, e
	}
	if reserved != nil || !start.After(time.Now().Add(30*time.Minute)) || !slot.Start.Equal(start) {
		return b, &Failure{Code: "slot_no_longer_available"}
	}
	if old != nil {
		if old.Provider != "local" || old.CloserID != closer || old.Group {
			return b, &Failure{Code: "local_reschedule_invariant"}
		}
		var previous string
		e = tx.QueryRow(ctx, `UPDATE bia_go.bookings SET cancelled_at=now() WHERE id=$1 AND lead_id=$2 AND cancelled_at IS NULL RETURNING slot_id`, old.ID, s.ID).Scan(&previous)
		if e != nil {
			return b, &Failure{Code: "old_booking_not_active"}
		}
		_, e = tx.Exec(ctx, `UPDATE bia_go.slots SET reserved_by=NULL WHERE id=$1 AND reserved_by=$2`, previous, old.ID)
		if e != nil {
			return b, e
		}
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.bookings WHERE lead_id=$1 AND cancelled_at IS NULL)`, s.ID).Scan(&exists)
	if e != nil {
		return b, e
	}
	if exists {
		return b, &Failure{Code: "lead_already_booked"}
	}
	h := sha256.Sum256([]byte(key))
	id := "local_" + hex.EncodeToString(h[:16])
	b = domain.Booking{ID: id, Provider: "local", Start: start, End: end, MeetingURL: meet, CloserID: closer}
	raw, _ = json.Marshal(b)
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.bookings(id,operation_id,lead_id,slot_id,payload) VALUES($1,$2,$3,$4,$5)`, id, key, s.ID, slot.ID, raw)
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.slots SET reserved_by=$2 WHERE id=$1`, slot.ID, id)
	if e != nil {
		return b, e
	}
	return b, nil
}
func (c *LocalCalendar) Cancel(ctx context.Context, key string, b domain.Booking) error {
	if b.Provider != "local" || b.Group {
		return &Failure{Code: "local_cancel_provider_mismatch"}
	}
	tx, e := c.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var slot string
	var cancelled *time.Time
	e = tx.QueryRow(ctx, `SELECT slot_id,cancelled_at FROM bia_go.bookings WHERE id=$1 FOR UPDATE`, b.ID).Scan(&slot, &cancelled)
	if e != nil {
		return e
	}
	if cancelled != nil {
		return nil
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.bookings SET cancelled_at=now() WHERE id=$1`, b.ID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.slots SET reserved_by=NULL WHERE id=$1 AND reserved_by=$2`, slot, b.ID)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type LocalCRM struct{ Pool *pgxpool.Pool }

func (c LocalCRM) Record(ctx context.Context, key string, s domain.State, fx domain.Effect) error {
	_, e := c.Pool.Exec(ctx, `INSERT INTO bia_go.crm_notes(operation_id,lead_id,purpose,body) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, key, s.ID, fx.Purpose, fx.Text)
	if e != nil {
		return &Failure{Code: "local_crm_unavailable", Retryable: true}
	}
	return nil
}
