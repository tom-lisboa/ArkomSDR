package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

// Cal uses the self-hosted contracts observed in the exported workflow. They
// must be contract-tested against the deployed Cal version before live cutover.
type Cal struct {
	URL, Team, Slug, TimeZone string
	EventTypeID               int64
	Client                    *http.Client
}

func (c *Cal) Slots(ctx context.Context, s domain.State, now time.Time) ([]domain.Slot, error) {
	var pin []int64
	if s.Booking != nil {
		if s.Booking.Provider != "cal" || s.Booking.CloserID == 0 {
			return nil, &Failure{Code: "cal_closer_unresolved"}
		}
		pin = []int64{s.Booking.CloserID}
	}
	input := map[string]any{"json": map[string]any{"isTeamEvent": true, "usernameList": []string{c.Team}, "eventTypeSlug": c.Slug, "startTime": now.UTC().Format(time.RFC3339), "endTime": now.AddDate(0, 0, 10).UTC().Format(time.RFC3339), "timeZone": c.TimeZone, "duration": nil, "rescheduleUid": nil, "orgSlug": nil, "teamMemberEmail": nil, "routedTeamMemberIds": pin, "skipContactOwner": len(pin) > 0, "routingFormResponseId": nil, "email": nil, "embedConnectVersion": "0", "_isDryRun": false}, "meta": map[string]any{"values": map[string]any{"duration": []string{"undefined"}, "orgSlug": []string{"undefined"}, "teamMemberEmail": []string{"undefined"}, "routingFormResponseId": []string{"undefined"}}}}
	b, _ := json.Marshal(input)
	var out struct {
		Result struct {
			Data struct {
				JSON struct {
					Slots map[string][]json.RawMessage `json:"slots"`
				} `json:"json"`
			} `json:"data"`
		} `json:"result"`
	}
	e := JSON(ctx, c.Client, "GET", c.URL+"/api/trpc/slots/getSchedule?input="+url.QueryEscape(string(b)), nil, nil, &out, true)
	if e != nil {
		return nil, e
	}
	if out.Result.Data.JSON.Slots == nil {
		return nil, &Failure{Code: "cal_invalid_schedule"}
	}
	var slots []domain.Slot
	for _, day := range out.Result.Data.JSON.Slots {
		for _, raw := range day {
			var ts string
			var v struct {
				Time string `json:"time"`
			}
			if json.Unmarshal(raw, &ts) != nil {
				if json.Unmarshal(raw, &v) != nil {
					return nil, &Failure{Code: "cal_invalid_slot"}
				}
				ts = v.Time
			}
			start, e := time.Parse(time.RFC3339, ts)
			if e != nil {
				return nil, &Failure{Code: "cal_invalid_slot_time"}
			}
			var closer int64
			if len(pin) > 0 {
				closer = pin[0]
			}
			slots = append(slots, domain.Slot{ID: fmt.Sprintf("cal-%d-%d", c.EventTypeID, start.Unix()), Start: start, End: start.Add(time.Hour), Provider: "cal", CloserID: closer, EventTypeID: c.EventTypeID})
		}
	}
	return slots, nil
}

func (c *Cal) Book(ctx context.Context, key string, s domain.State, slot domain.Slot, old *domain.Booking) (domain.Booking, error) {
	var b domain.Booking
	if slot.Provider != "cal" || slot.Group {
		return b, &Failure{Code: "cal_unsupported_booking"}
	}
	if s.Profile.Email == "" {
		return b, &Failure{Code: "booking_email_required"}
	}
	if old != nil && (old.Provider != "cal" || old.Group || old.CloserID == 0 || old.CloserID != slot.CloserID) {
		return b, &Failure{Code: "cal_reschedule_invariant"}
	}
	// Revalidate availability immediately before the mutation.
	slots, e := c.Slots(ctx, s, time.Now().UTC())
	if e != nil {
		return b, e
	}
	found := false
	for _, v := range slots {
		if v.ID == slot.ID {
			found = true
			break
		}
	}
	if !found {
		return b, &Failure{Code: "slot_no_longer_available"}
	}
	body := map[string]any{"responses": map[string]any{"name": s.Profile.Name, "email": s.Profile.Email, "attendeePhoneNumber": s.Profile.Phone, "title": "Diagnóstico de IA", "guests": []string{}, "location": map[string]any{"value": "integrations:google:meet", "optionValue": ""}}, "start": slot.Start.UTC().Format(time.RFC3339), "end": slot.End.UTC().Format(time.RFC3339), "eventTypeId": c.EventTypeID, "eventTypeSlug": c.Slug, "timeZone": c.TimeZone, "language": "pt-BR", "metadata": map[string]string{"origem": "bia-go", "bia_go_operation": key, "bia_go_lead_id": s.ID}, "hasHashedBookingLink": false, "user": c.Team, "skipContactOwner": false, "_isDryRun": false}
	if slot.CloserID > 0 {
		body["routedTeamMemberIds"] = []int64{slot.CloserID}
		body["routingFormResponseId"] = 0
		body["skipContactOwner"] = true
	}
	if old != nil {
		body["rescheduleUid"] = old.ID
	}
	var out struct {
		UID          string    `json:"uid"`
		StartTime    time.Time `json:"startTime"`
		EndTime      time.Time `json:"endTime"`
		UserID       int64     `json:"userId"`
		VideoCallURL string    `json:"videoCallUrl"`
		References   []struct {
			MeetingURL string `json:"meetingUrl"`
			Type       string `json:"type"`
		} `json:"references"`
	}
	e = JSON(ctx, c.Client, "POST", c.URL+"/api/book/event", nil, body, &out, false)
	if e != nil {
		return b, e
	}
	if out.UID == "" || out.UserID == 0 || !out.StartTime.Equal(slot.Start) || (slot.CloserID > 0 && out.UserID != slot.CloserID) {
		return b, &Failure{Code: "cal_booking_requires_reconciliation", Ambiguous: true}
	}
	meet := ""
	for _, r := range out.References {
		if r.Type == "google_calendar" && domain.ValidHTTPS(r.MeetingURL) {
			meet = r.MeetingURL
			break
		}
	}
	if meet == "" && domain.ValidHTTPS(out.VideoCallURL) {
		meet = out.VideoCallURL
	}
	if meet == "" {
		return b, &Failure{Code: "cal_meeting_link_missing", Ambiguous: true}
	}
	return domain.Booking{ID: out.UID, Provider: "cal", Start: out.StartTime, End: slot.End, MeetingURL: meet, CloserID: out.UserID}, nil
}

func (c *Cal) Cancel(ctx context.Context, key string, b domain.Booking) error {
	if b.Provider != "cal" || b.Group || !b.Start.After(time.Now()) || !domain.ValidID(b.ID) {
		return &Failure{Code: "cal_cancel_requires_review"}
	}
	// A cancellation is intentionally not reverse-engineered from the CSRF UI.
	// The workflow additionally verifies event type from HTML; without the
	// deployed API/booking lookup contract we cannot prove ownership safely.
	return &Failure{Code: "cal_cancel_contract_not_verified"}
}

type GatewayCalendar struct{ Gateway }

func (g *GatewayCalendar) Slots(ctx context.Context, s domain.State, now time.Time) ([]domain.Slot, error) {
	var out struct {
		Slots []domain.Slot `json:"slots"`
	}
	e := JSON(ctx, g.Client, "POST", g.URL+"/calendar/slots", g.headers(""), map[string]any{"lead_id": s.ID, "booking": s.Booking, "from": now, "timezone": s.Profile.TimeZone}, &out, true)
	if e == nil {
		for _, slot := range out.Slots {
			if slot.ID == "" || slot.Provider == "" || !slot.End.After(slot.Start) {
				return nil, &Failure{Code: "gateway_invalid_slots"}
			}
		}
	}
	return out.Slots, e
}
func (g *GatewayCalendar) Book(ctx context.Context, key string, s domain.State, slot domain.Slot, old *domain.Booking) (domain.Booking, error) {
	var b domain.Booking
	e := JSON(ctx, g.Client, "POST", g.URL+"/calendar/book", g.headers(key), map[string]any{"lead_id": s.ID, "contact": s.Profile, "slot": slot, "previous_booking": old}, &b, true)
	if e == nil && (b.ID == "" || b.Provider != slot.Provider || !b.Start.Equal(slot.Start) || !b.End.After(b.Start) || !domain.ValidHTTPS(b.MeetingURL) || (slot.CloserID > 0 && slot.CloserID != b.CloserID)) {
		e = &Failure{Code: "gateway_booking_requires_reconciliation", Ambiguous: true}
	}
	return b, e
}
func (g *GatewayCalendar) Cancel(ctx context.Context, key string, b domain.Booking) error {
	var out struct {
		Cancelled bool   `json:"cancelled"`
		BookingID string `json:"booking_id"`
		SeatOnly  bool   `json:"seat_only"`
	}
	e := JSON(ctx, g.Client, "POST", g.URL+"/calendar/cancel", g.headers(key), b, &out, true)
	if e == nil && (!out.Cancelled || out.BookingID != b.ID || (b.Group && !out.SeatOnly)) {
		e = &Failure{Code: "gateway_cancel_unconfirmed", Ambiguous: true}
	}
	return e
}

func TrimBase(s string) string { return strings.TrimRight(s, "/") }
