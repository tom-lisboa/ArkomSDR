package dualcalendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/googlecalendar"
	"arkom/bia/internal/providers"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

type intent struct {
	State domain.State    `json:"state"`
	Slot  domain.Slot     `json:"slot"`
	Old   *domain.Booking `json:"old,omitempty"`
}
type operation struct {
	Key, Kind, Lead, Slot, Booking, Old, User, Account, Calendar, Event, Status string
	Intent                                                                      intent
}

func digest(key string) string { v := sha256.Sum256([]byte(key)); return hex.EncodeToString(v[:]) }
func readOperation(ctx context.Context, tx pgx.Tx, key string) (operation, error) {
	var o operation
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT operation_id,kind,lead_id,slot_id,booking_id,old_booking_id,user_id,account_id,calendar_id,event_id,status,payload FROM bia_go.google_booking_ops WHERE operation_id=$1`, key).Scan(&o.Key, &o.Kind, &o.Lead, &o.Slot, &o.Booking, &o.Old, &o.User, &o.Account, &o.Calendar, &o.Event, &o.Status, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &o.Intent)
	}
	return o, err
}
func (c *Service) Book(ctx context.Context, key string, s domain.State, slot domain.Slot, old *domain.Booking) (domain.Booking, error) {
	empty := domain.Booking{}
	if slot.Provider != "local" || slot.Group {
		return empty, &providers.Failure{Code: "invalid_local_booking"}
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	if err = capacityLock(ctx, tx); err != nil {
		return empty, err
	}
	existing, err := readOperation(ctx, tx, key)
	if err == nil {
		oldID := ""
		if old != nil {
			oldID = old.ID
		}
		if existing.Kind != "book" || existing.Lead != s.ID || !existing.Intent.Slot.Start.Equal(slot.Start) || existing.Old != oldID {
			return empty, &providers.Failure{Code: "calendar_idempotency_conflict"}
		}
		if err = tx.Commit(ctx); err != nil {
			return empty, err
		}
		return c.resume(ctx, key, true)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	// One unresolved operation per lead, including a cancellation in progress.
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.google_booking_ops WHERE lead_id=$1 AND status='pending')`, s.ID).Scan(&pending); err != nil {
		return empty, err
	}
	if pending {
		return empty, &providers.Deferred{Code: "calendar_operation_pending", After: 5 * time.Second}
	}
	var closer int64
	if old != nil && old.GoogleSynced {
		closer = old.CloserID
	}
	list, err := c.candidates(ctx, tx, time.Now(), closer)
	if err != nil {
		return empty, err
	}
	var chosen *candidate
	var checkFailed bool
	for _, v := range list {
		if !v.Start.Equal(slot.Start) || !v.End.Equal(slot.End) {
			continue
		}
		blocks, e := c.busy(ctx, v, v.Start, v.End)
		if e != nil {
			checkFailed = true
			continue
		}
		if !overlaps(blocks, v.Start, v.End) {
			copy := v
			chosen = &copy
			break
		}
	}
	if chosen == nil {
		if checkFailed {
			return empty, &providers.Deferred{Code: "google_availability_unavailable", After: 5 * time.Second}
		}
		return empty, &providers.Failure{Code: "google_slot_no_longer_available"}
	}
	// Verify the current lead reservation before creating an external event.
	var active string
	err = tx.QueryRow(ctx, `SELECT id FROM bia_go.bookings WHERE lead_id=$1 AND cancelled_at IS NULL`, s.ID).Scan(&active)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if (old == nil && active != "") || (old != nil && active != old.ID) {
		return empty, &providers.Failure{Code: "lead_booking_changed"}
	}
	if old != nil && old.GoogleSynced {
		if _, err = c.bookedOperation(ctx, tx, old.ID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return empty, &providers.Failure{Code: "google_booking_binding_missing"}
			}
			return empty, err
		}
	}
	o := operation{Key: key, Kind: "book", Lead: s.ID, Slot: chosen.ID, Booking: "local_" + digest(key)[:32], User: chosen.User, Account: chosen.Account, Calendar: chosen.Calendar, Event: "bia" + digest(key), Status: "pending"}
	if old != nil {
		o.Old = old.ID
	}
	// Persist only the brief needed for the calendar event, not conversation history.
	o.Intent = intent{State: domain.State{ID: s.ID, Profile: domain.Form{Name: s.Profile.Name, Company: s.Profile.Company, Phone: s.Profile.Phone}}, Slot: chosen.Slot, Old: old}
	raw, _ := json.Marshal(o.Intent)
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.google_booking_ops(operation_id,kind,lead_id,slot_id,booking_id,old_booking_id,user_id,account_id,calendar_id,event_id,payload) VALUES($1,'book',$2,$3,$4,$5,$6,$7,$8,$9,$10)`, o.Key, o.Lead, o.Slot, o.Booking, o.Old, o.User, o.Account, o.Calendar, o.Event, raw)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	return c.resume(ctx, key, true)
}
func (c *Service) pending(ctx context.Context, key, code string) error {
	// Background context permits the diagnostic to survive the provider deadline.
	op, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = c.Pool.Exec(op, `UPDATE bia_go.google_booking_ops SET error_code=$2,updated_at=now() WHERE operation_id=$1 AND status='pending'`, key, code)
	return &providers.Deferred{Code: code, After: 5 * time.Second}
}
func (c *Service) resume(ctx context.Context, key string, allowCreate bool) (domain.Booking, error) {
	var empty domain.Booking
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "calendar-op:"+key).Scan(&locked); err != nil {
		return empty, err
	}
	if !locked {
		return empty, &providers.Deferred{Code: "calendar_operation_pending", After: 5 * time.Second}
	}
	o, err := readOperation(ctx, tx, key)
	if err != nil {
		return empty, err
	}
	if o.Status == "failed" {
		return empty, &providers.Failure{Code: "google_booking_failed"}
	}
	if o.Status == "confirmed" {
		if o.Kind == "cancel" {
			return empty, nil
		}
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT payload FROM bia_go.bookings WHERE id=$1`, o.Booking).Scan(&raw)
		if err == nil {
			err = json.Unmarshal(raw, &empty)
		}
		return empty, err
	}
	token, err := c.Google.AccountToken(ctx, c.Pool, o.User, o.Account, true)
	if err != nil {
		return empty, c.pending(ctx, key, "google_scheduling_reconnect_required")
	}
	if o.Kind == "cancel" {
		if err = c.remove(ctx, token, o); err != nil {
			return empty, c.pending(ctx, key, "google_cancellation_pending")
		}
		if _, err = tx.Exec(ctx, `UPDATE bia_go.bookings SET cancelled_at=coalesce(cancelled_at,now()) WHERE id=$1`, o.Booking); err != nil {
			return empty, err
		}
		if _, err = tx.Exec(ctx, `UPDATE bia_go.slots SET reserved_by=NULL WHERE id=$1 AND reserved_by=$2`, o.Slot, o.Booking); err != nil {
			return empty, err
		}
	} else {
		event, e := c.Google.Event(ctx, token, o.Calendar, o.Event)
		if googlecalendar.HasStatus(e, 404) {
			if !o.Intent.Slot.Start.After(time.Now().Add(30 * time.Minute)) {
				return empty, failIntent(ctx, tx, key, "google_slot_expired")
			}
			if !allowCreate {
				return empty, c.pending(ctx, key, "google_event_creation_pending")
			}
			// Durable holds protect internal capacity; recheck external calendars just
			// before insertion. No confirmation follows an incomplete free/busy reply.
			blocks, busyErr := c.busy(ctx, candidate{Slot: o.Intent.Slot, User: o.User, Account: o.Account, Calendar: o.Calendar}, o.Intent.Slot.Start, o.Intent.Slot.End)
			if busyErr != nil {
				return empty, c.pending(ctx, key, "google_availability_unavailable")
			}
			if overlaps(blocks, o.Intent.Slot.Start, o.Intent.Slot.End) {
				return empty, failIntent(ctx, tx, key, "google_slot_no_longer_available")
			}
			event = googlecalendar.Event{ID: o.Event, Summary: "Arkom · Diagnóstico com " + o.Intent.State.Profile.Name, Description: fmt.Sprintf("Empresa: %s\nContato: %s\nReserva SDR: %s", o.Intent.State.Profile.Company, o.Intent.State.Profile.Phone, o.Booking), Start: googlecalendar.EventTime{DateTime: o.Intent.Slot.Start}, End: googlecalendar.EventTime{DateTime: o.Intent.Slot.End}}
			event.ExtendedProperties.Private = map[string]string{"arkom_operation": digest(o.Key), "arkom_booking": o.Booking}
			event, e = c.Google.Insert(ctx, token, o.Calendar, event)
			if e != nil {
				if googlecalendar.HasStatus(e, 400) {
					return empty, failIntent(ctx, tx, key, "google_event_rejected")
				}
				// The provider may have accepted a request whose response was lost.
				// Retrying resolves the same deterministic ID before attempting insertion.
				return empty, c.pending(ctx, key, "google_event_creation_pending")
			}
		} else if e != nil {
			return empty, c.pending(ctx, key, "google_event_lookup_pending")
		}
		if event.ID != o.Event || event.Status == "cancelled" || event.ExtendedProperties.Private["arkom_operation"] != digest(o.Key) || !event.Start.DateTime.Equal(o.Intent.Slot.Start) || !event.End.DateTime.Equal(o.Intent.Slot.End) {
			return empty, c.pending(ctx, key, "google_event_requires_review")
		}
		if o.Old != "" {
			previous, e := c.bookedOperation(ctx, tx, o.Old)
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return empty, e
			}
			if e == nil {
				oldToken, e := c.Google.AccountToken(ctx, c.Pool, previous.User, previous.Account, true)
				if e != nil {
					return empty, c.pending(ctx, key, "google_old_calendar_reconnect_required")
				}
				if e = c.remove(ctx, oldToken, previous); e != nil {
					return empty, c.pending(ctx, key, "google_reschedule_cleanup_pending")
				}
			}
		}
		// The local record and verified Google binding are committed together. A
		// failed commit leaves the intent available for deterministic reconciliation.
		empty, err = providers.BookVerifiedLocalTx(ctx, tx, key, o.Intent.State, o.Intent.Slot, o.Intent.Old)
		if err != nil {
			return empty, c.pending(ctx, key, "google_local_commit_pending")
		}
	}
	_, err = tx.Exec(ctx, `UPDATE bia_go.google_booking_ops SET status='confirmed',error_code='',updated_at=now() WHERE operation_id=$1`, key)
	if err != nil {
		return empty, err
	}
	event := domain.Event{ID: "google-booking/" + digest(key), LeadID: o.Lead, Kind: "booking.created", At: time.Now().UTC(), Booking: &empty}
	if o.Old != "" {
		event.Kind = "booking.rescheduled"
	}
	if o.Kind == "cancel" {
		event.Kind = "booking.cancelled"
		event.Booking = nil
		event.BookingID = o.Booking
	}
	raw, _ := json.Marshal(event)
	if _, err = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event','live',$3,$4,now()) ON CONFLICT DO NOTHING`, event.ID, event.LeadID, raw, digest(string(raw))); err != nil {
		return empty, err
	}
	if o.Kind == "book" && (strings.HasPrefix(key, "form-booking:") || strings.HasPrefix(key, "site-booking:")) {
		if _, err = tx.Exec(ctx, `UPDATE bia_go.jobs SET status='skipped',error_code='web_booking_created',completed_at=now() WHERE lead_id=$1 AND kind='effect' AND status='pending' AND payload->>'kind'='send' AND payload->>'purpose' IN ('reply','offer','availability','followup')`, o.Lead); err != nil {
			return empty, err
		}
	}
	return empty, tx.Commit(ctx)
}

func failIntent(ctx context.Context, tx pgx.Tx, key, code string) error {
	_, err := tx.Exec(ctx, `UPDATE bia_go.google_booking_ops SET status='failed',error_code=$2,updated_at=now() WHERE operation_id=$1`, key, code)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return &providers.Failure{Code: code}
}
func (c *Service) bookedOperation(ctx context.Context, tx pgx.Tx, booking string) (operation, error) {
	var key string
	err := tx.QueryRow(ctx, `SELECT operation_id FROM bia_go.google_booking_ops WHERE booking_id=$1 AND kind='book' AND status='confirmed'`, booking).Scan(&key)
	if err != nil {
		return operation{}, err
	}
	return readOperation(ctx, tx, key)
}
func (c *Service) remove(ctx context.Context, token *oauth2.Token, o operation) error {
	event, err := c.Google.Event(ctx, token, o.Calendar, o.Event)
	if googlecalendar.HasStatus(err, 404) || googlecalendar.HasStatus(err, 410) {
		cal, err := c.Google.Calendar(ctx, token, o.Calendar)
		if err != nil {
			return err
		}
		if cal.AccessRole != "owner" && cal.AccessRole != "writer" {
			return errors.New("google_calendar_write_required")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if event.Status == "cancelled" {
		return nil
	}
	if event.ID != o.Event || event.ExtendedProperties.Private["arkom_booking"] != o.Booking {
		return errors.New("google_event_identity_mismatch")
	}
	return c.Google.Delete(ctx, token, o.Calendar, o.Event)
}
func (c *Service) Cancel(ctx context.Context, key string, b domain.Booking) error {
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = capacityLock(ctx, tx); err != nil {
		return err
	}
	o, err := readOperation(ctx, tx, key)
	if err == nil {
		if o.Kind != "cancel" || o.Booking != b.ID {
			return &providers.Failure{Code: "calendar_idempotency_conflict"}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		_, err = c.resume(ctx, key, false)
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	previous, err := c.bookedOperation(ctx, tx, b.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if b.GoogleSynced {
			return &providers.Failure{Code: "google_booking_binding_missing"}
		}
		tx.Rollback(ctx)
		return (&providers.LocalCalendar{Pool: c.Pool}).Cancel(ctx, key, b)
	}
	if err != nil {
		return err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.google_booking_ops WHERE lead_id=$1 AND status='pending')`, previous.Lead).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return &providers.Deferred{Code: "calendar_operation_pending", After: 5 * time.Second}
	}
	raw, _ := json.Marshal(previous.Intent)
	_, err = tx.Exec(ctx, `INSERT INTO bia_go.google_booking_ops(operation_id,kind,lead_id,slot_id,booking_id,user_id,account_id,calendar_id,event_id,payload) VALUES($1,'cancel',$2,$3,$4,$5,$6,$7,$8,$9)`, key, previous.Lead, previous.Slot, b.ID, previous.User, previous.Account, previous.Calendar, previous.Event, raw)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_, err = c.resume(ctx, key, false)
	return err
}
