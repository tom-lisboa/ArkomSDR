package providers

import (
	"testing"
	"time"
)

func TestDailyAvailabilityFitsWindowsAndIncludesWeekend(t *testing.T) {
	a := DailyAvailability{Enabled: true, TimeZone: "America/Cuiaba", Windows: "08:00-12:00,14:00-20:00", DurationMinutes: 45, Days: 2, CloserID: 1}
	zone, _ := time.LoadLocation(a.TimeZone)
	now := time.Date(2026, 10, 31, 6, 0, 0, 0, zone) // Saturday, followed by Sunday.
	slots, err := a.Slots(now)
	if err != nil || len(slots) != 26 {
		t.Fatal(len(slots), err)
	}
	for _, s := range slots {
		start, end := s.Start.In(zone), s.End.In(zone)
		if end.Sub(start) != 45*time.Minute || start.Hour() < 8 || end.Hour() > 20 || (start.Hour() < 14 && end.Hour() >= 12) {
			t.Fatal(s)
		}
		if start.Weekday() != time.Saturday && start.Weekday() != time.Sunday {
			t.Fatal(s)
		}
		if s.MeetingURL != "" {
			t.Fatal("invented meeting URL")
		}
	}
	if slots[4].End.In(zone).Format("15:04") != "11:45" || slots[12].End.In(zone).Format("15:04") != "20:00" {
		t.Fatal("meetings cross window boundary")
	}
	later, err := a.Slots(time.Date(2026, 10, 31, 11, 30, 0, 0, zone))
	if err != nil || len(later) != 21 {
		t.Fatal(len(later), err)
	}
	for _, s := range later {
		if !s.Start.After(time.Date(2026, 10, 31, 12, 0, 0, 0, zone)) {
			t.Fatal("slot inside minimum notice")
		}
	}
}
