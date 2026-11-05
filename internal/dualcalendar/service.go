// Package dualcalendar schedules the internal SDR in PostgreSQL and Google.
// Google I/O occurs only through the scheduling worker's Calendar adapter.
package dualcalendar

import (
	"context"
	"errors"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/googlecalendar"
	"arkom/bia/internal/providers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	Pool         *pgxpool.Pool
	Google       *googlecalendar.Client
	Availability providers.DailyAvailability
}
type Selection struct {
	AccountID  string `json:"account_id"`
	CalendarID string `json:"calendar_id"`
}
type Profile struct {
	Enabled      bool        `json:"enabled"`
	Destination  Selection   `json:"destination"`
	Blocks       []Selection `json:"blocking_calendars"`
	CalendarName string      `json:"calendar_name"`
	CloserID     int64       `json:"closer_id"`
	Ready        bool        `json:"ready"`
	Distribution string      `json:"distribution"`
}

func capacityLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('internal-calendar-capacity',0))`)
	return err
}
func (c *Service) Profile(ctx context.Context, user string) (Profile, error) {
	p := Profile{Blocks: []Selection{}, Distribution: "fewest_future_bookings"}
	err := c.Pool.QueryRow(ctx, `SELECT enabled,coalesce(account_id,''),calendar_id,calendar_name,closer_id FROM bia_go.seller_calendars WHERE user_id=$1`, user).Scan(&p.Enabled, &p.Destination.AccountID, &p.Destination.CalendarID, &p.CalendarName, &p.CloserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	rows, err := c.Pool.Query(ctx, `SELECT account_id,calendar_id FROM bia_go.seller_calendar_blocks WHERE user_id=$1 ORDER BY account_id,calendar_id`, user)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var b Selection
		if err = rows.Scan(&b.AccountID, &b.CalendarID); err != nil {
			rows.Close()
			return p, err
		}
		p.Blocks = append(p.Blocks, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	err = c.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.seller_calendars p JOIN bia_go.management_users u ON u.id=p.user_id JOIN bia_go.google_accounts a ON a.id=p.account_id AND a.user_id=p.user_id WHERE p.user_id=$1 AND p.enabled AND u.enabled AND u.role IN ('admin','operator') AND a.client_id=$2 AND a.scopes @> $3::text[] AND a.google_subject IS NOT NULL AND NOT EXISTS (SELECT 1 FROM bia_go.seller_calendar_blocks b JOIN bia_go.google_accounts ba ON ba.id=b.account_id WHERE b.user_id=p.user_id AND (ba.client_id<>$2 OR NOT ba.scopes @> $3::text[] OR ba.google_subject IS NULL)))`, user, c.Google.Config.ClientID, []string{googlecalendar.EventsScope, googlecalendar.FreeBusyScope}).Scan(&p.Ready)
	return p, err
}
func (c *Service) Save(ctx context.Context, user string, p Profile) error {
	if len(p.Blocks) > 49 {
		return errors.New("too_many_blocking_calendars")
	}
	if p.Enabled {
		if p.Destination.AccountID == "" || p.Destination.CalendarID == "" {
			return errors.New("google_destination_required")
		}
		all := append([]Selection{p.Destination}, p.Blocks...)
		seen := map[Selection]bool{}
		p.Blocks = nil
		for i, v := range all {
			if len(v.AccountID) > 80 || len(v.CalendarID) > 1024 || v.AccountID == "" || v.CalendarID == "" {
				return errors.New("invalid_calendar_selection")
			}
			if seen[v] {
				continue
			}
			seen[v] = true
			token, err := c.Google.AccountToken(ctx, c.Pool, user, v.AccountID, true)
			if err != nil {
				return err
			}
			cal, err := c.Google.Calendar(ctx, token, v.CalendarID)
			if err != nil {
				return errors.New("google_calendar_unavailable")
			}
			if i == 0 {
				if cal.AccessRole != "owner" && cal.AccessRole != "writer" {
					return errors.New("google_calendar_write_required")
				}
				p.CalendarName = cal.Summary
			}
			p.Blocks = append(p.Blocks, v)
		}
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = capacityLock(ctx, tx); err != nil {
		return err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bia_go.google_booking_ops WHERE user_id=$1 AND status='pending')`, user).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return errors.New("google_scheduling_operation_pending")
	}
	if !p.Enabled {
		_, err = tx.Exec(ctx, `UPDATE bia_go.seller_calendars SET enabled=false,updated_at=now() WHERE user_id=$1`, user)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO bia_go.seller_calendars(user_id,enabled,account_id,calendar_id,calendar_name) VALUES($1,true,$2,$3,$4) ON CONFLICT(user_id) DO UPDATE SET enabled=true,account_id=EXCLUDED.account_id,calendar_id=EXCLUDED.calendar_id,calendar_name=EXCLUDED.calendar_name,updated_at=now()`, user, p.Destination.AccountID, p.Destination.CalendarID, p.CalendarName)
		if err == nil {
			_, err = tx.Exec(ctx, `DELETE FROM bia_go.seller_calendar_blocks WHERE user_id=$1`, user)
		}
		for _, v := range p.Blocks {
			if err != nil {
				break
			}
			var inserted pgconn.CommandTag
			inserted, err = tx.Exec(ctx, `INSERT INTO bia_go.seller_calendar_blocks(user_id,account_id,calendar_id) SELECT $1,a.id,$3 FROM bia_go.google_accounts a WHERE a.id=$2 AND a.user_id=$1`, user, v.AccountID, v.CalendarID)
			if err == nil && inserted.RowsAffected() != 1 {
				err = errors.New("google_account_not_found")
			}
		}
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return c.EnsureAvailability(ctx, c.Availability, time.Now())
}
func (c *Service) EnsureAvailability(ctx context.Context, a providers.DailyAvailability, now time.Time) error {
	rows, err := c.Pool.Query(ctx, `SELECT closer_id FROM bia_go.seller_calendars p JOIN bia_go.management_users u ON u.id=p.user_id WHERE p.enabled AND u.enabled AND u.role IN ('operator','admin')`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	local := providers.LocalCalendar{Pool: c.Pool}
	for _, id := range ids {
		a.CloserID = id
		if err = local.EnsureAvailability(ctx, a, now); err != nil {
			return err
		}
	}
	return nil
}

type candidate struct {
	domain.Slot
	User, Account, Calendar string
	Load                    int
}

func (c *Service) candidates(ctx context.Context, tx pgx.Tx, now time.Time, closer int64) ([]candidate, error) {
	q := `SELECT s.id,s.starts_at,s.ends_at,s.closer_id,p.user_id,p.account_id,p.calendar_id,
 (SELECT count(*) FROM bia_go.bookings b JOIN bia_go.slots bs ON bs.id=b.slot_id WHERE b.cancelled_at IS NULL AND bs.closer_id=s.closer_id AND bs.starts_at>now()) +
 (SELECT count(*) FROM bia_go.google_booking_ops o JOIN bia_go.slots os ON os.id=o.slot_id WHERE o.status='pending' AND o.kind='book' AND os.closer_id=s.closer_id)
 FROM bia_go.slots s JOIN bia_go.seller_calendars p ON p.closer_id=s.closer_id
 JOIN bia_go.management_users u ON u.id=p.user_id
 JOIN bia_go.google_accounts a ON a.id=p.account_id AND a.user_id=p.user_id
 WHERE s.reserved_by IS NULL AND s.starts_at>$1 AND ($2::bigint=0 OR s.closer_id=$2)
 AND p.enabled AND u.enabled AND u.role IN ('operator','admin') AND a.client_id=$3 AND a.google_subject IS NOT NULL AND a.scopes @> $4::text[]
 AND NOT EXISTS(SELECT 1 FROM bia_go.slots os WHERE os.closer_id=s.closer_id AND os.reserved_by IS NOT NULL AND os.starts_at<s.ends_at AND os.ends_at>s.starts_at)
 AND NOT EXISTS(SELECT 1 FROM bia_go.google_booking_ops o JOIN bia_go.slots os ON os.id=o.slot_id
 LEFT JOIN bia_go.bookings ob ON ob.id=o.booking_id
 WHERE (o.status='pending' OR (o.status='confirmed' AND o.kind='book' AND ob.id IS NOT NULL AND ob.cancelled_at IS NULL))
 AND os.starts_at<s.ends_at AND os.ends_at>s.starts_at AND (os.closer_id=s.closer_id OR o.calendar_id=p.calendar_id
 OR EXISTS(SELECT 1 FROM bia_go.seller_calendar_blocks cb WHERE cb.user_id=p.user_id AND cb.calendar_id=o.calendar_id)
 OR EXISTS(SELECT 1 FROM bia_go.seller_calendar_blocks ob WHERE ob.user_id=o.user_id AND ob.calendar_id=p.calendar_id)))
 ORDER BY s.starts_at,8,p.user_id LIMIT 5000`
	args := []any{now.Add(30 * time.Minute), closer, c.Google.Config.ClientID, []string{googlecalendar.EventsScope, googlecalendar.FreeBusyScope}}
	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, q, args...)
	} else {
		rows, err = c.Pool.Query(ctx, q, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		v := candidate{Slot: domain.Slot{Provider: "local", GoogleCalendar: true}}
		if err = rows.Scan(&v.ID, &v.Start, &v.End, &v.CloserID, &v.User, &v.Account, &v.Calendar, &v.Load); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (c *Service) busy(ctx context.Context, v candidate, start, end time.Time) ([]googlecalendar.Interval, error) {
	p, err := c.Profile(ctx, v.User)
	if err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	for _, b := range append(p.Blocks, Selection{v.Account, v.Calendar}) {
		found := false
		for _, id := range groups[b.AccountID] {
			if id == b.CalendarID {
				found = true
			}
		}
		if !found {
			groups[b.AccountID] = append(groups[b.AccountID], b.CalendarID)
		}
	}
	var out []googlecalendar.Interval
	for account, ids := range groups {
		t, err := c.Google.AccountToken(ctx, c.Pool, v.User, account, true)
		if err != nil {
			return nil, err
		}
		items, err := c.Google.Busy(ctx, t, ids, start, end)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}
func overlaps(busy []googlecalendar.Interval, start, end time.Time) bool {
	for _, b := range busy {
		if b.Start.Before(end) && b.End.After(start) {
			return true
		}
	}
	return false
}
func (c *Service) Slots(ctx context.Context, s domain.State, now time.Time) ([]domain.Slot, error) {
	var closer int64
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.GoogleSynced {
		closer = s.Booking.CloserID
	}
	candidates, err := c.candidates(ctx, nil, now, closer)
	if err != nil {
		return nil, err
	}
	out := []domain.Slot{}
	if len(candidates) == 0 {
		return out, nil
	}
	busy := map[string][]googlecalendar.Interval{}
	unavailable := map[string]bool{}
	seen := map[string]bool{}
	end := candidates[len(candidates)-1].End
	for _, v := range candidates {
		key := v.Start.Format(time.RFC3339Nano) + "/" + v.End.Format(time.RFC3339Nano)
		if seen[key] || unavailable[v.User] {
			continue
		}
		blocks, ok := busy[v.User]
		if !ok {
			blocks, err = c.busy(ctx, v, candidates[0].Start, end)
			if err != nil {
				unavailable[v.User] = true
				continue
			}
			busy[v.User] = blocks
		}
		if !overlaps(blocks, v.Start, v.End) {
			seen[key] = true
			out = append(out, v.Slot)
		}
	}
	if len(out) == 0 && len(unavailable) > 0 {
		return nil, &providers.Failure{Code: "google_availability_unavailable", Retryable: true}
	}
	return out, nil
}
