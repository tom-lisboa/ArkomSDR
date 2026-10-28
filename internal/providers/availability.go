package providers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

// DailyAvailability is the explicitly configured schedule for our own calendar.
// It creates capacity only; it never reserves a slot or sends a message.
type DailyAvailability struct {
	Enabled         bool
	TimeZone        string
	Windows         string
	DurationMinutes int
	Days            int
	CloserID        int64
	MeetingURL      string
}

func (a DailyAvailability) intervals() ([][2]int, error) {
	var out [][2]int
	for _, window := range strings.Split(a.Windows, ",") {
		ends := strings.Split(strings.TrimSpace(window), "-")
		if len(ends) != 2 {
			return nil, errors.New("invalid calendar windows")
		}
		var pair [2]int
		for i, raw := range ends {
			t, err := time.Parse("15:04", raw)
			if err != nil {
				return nil, errors.New("invalid calendar window time")
			}
			pair[i] = t.Hour()*60 + t.Minute()
		}
		if pair[1]-pair[0] < a.DurationMinutes {
			return nil, errors.New("calendar window shorter than meeting")
		}
		for _, existing := range out {
			if pair[0] < existing[1] && pair[1] > existing[0] {
				return nil, errors.New("overlapping calendar windows")
			}
		}
		out = append(out, pair)
	}
	return out, nil
}

func (a DailyAvailability) Validate() error {
	if !a.Enabled {
		return nil
	}
	if a.DurationMinutes < 15 || a.DurationMinutes > 240 || a.Days < 1 || a.Days > 60 || a.CloserID < 1 {
		return errors.New("invalid local calendar settings")
	}
	if _, err := time.LoadLocation(a.TimeZone); err != nil {
		return errors.New("invalid calendar timezone")
	}
	if a.MeetingURL != "" && !domain.ValidHTTPS(a.MeetingURL) {
		return errors.New("invalid calendar meeting URL")
	}
	_, err := a.intervals()
	return err
}

func (a DailyAvailability) Slots(now time.Time) ([]LocalSlot, error) {
	if !a.Enabled {
		return nil, nil
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	zone, _ := time.LoadLocation(a.TimeZone)
	windows, _ := a.intervals()
	local := now.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)
	var slots []LocalSlot
	for d := 0; d < a.Days; d++ {
		date := day.AddDate(0, 0, d)
		for _, window := range windows {
			for minute := window[0]; minute+a.DurationMinutes <= window[1]; minute += a.DurationMinutes {
				start := time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, zone)
				end := start.Add(time.Duration(a.DurationMinutes) * time.Minute)
				// Never materialize elapsed/too-close slots, or spill across a DST/window edge.
				if !start.After(now.Add(30*time.Minute)) || end.In(zone).Day() != date.Day() || end.In(zone).Hour()*60+end.In(zone).Minute() > window[1] {
					continue
				}
				id := fmt.Sprintf("daily-%d-%s", a.CloserID, strconv.FormatInt(start.Unix(), 10))
				slots = append(slots, LocalSlot{Slot: domain.Slot{ID: id, Provider: "local", CloserID: a.CloserID, Start: start.UTC(), End: end.UTC()}, MeetingURL: a.MeetingURL})
			}
		}
	}
	return slots, nil
}

func (c *LocalCalendar) EnsureAvailability(ctx context.Context, a DailyAvailability, now time.Time) error {
	slots, err := a.Slots(now)
	if err != nil || len(slots) == 0 {
		return err
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, a.CloserID); err != nil {
		return err
	}
	for _, slot := range slots {
		// Preserve reservations and manually configured capacity. Concurrent
		// scheduling workers use the same host lock as LocalCalendar.Add.
		_, err = tx.Exec(ctx, `INSERT INTO bia_go.slots(id,starts_at,ends_at,closer_id,meeting_url)
		 SELECT $1,$2,$3,$4,$5 WHERE NOT EXISTS(SELECT 1 FROM bia_go.slots WHERE closer_id=$4 AND starts_at<$3 AND ends_at>$2)
		 ON CONFLICT(id) DO NOTHING`, slot.ID, slot.Start, slot.End, slot.CloserID, slot.MeetingURL)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
