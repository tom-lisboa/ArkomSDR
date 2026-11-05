package googlecalendar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"
)

type APIError struct{ Status int }

func (e *APIError) Error() string { return "google_calendar_request_failed" }
func HasStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

func (c *Client) request(ctx context.Context, token *oauth2.Token, method, path string, body, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://www.googleapis.com/calendar/v3/"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	token.SetAuthHeader(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &APIError{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, out) != nil {
		return &APIError{}
	}
	return nil
}

type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (c *Client) Busy(ctx context.Context, token *oauth2.Token, ids []string, start, end time.Time) ([]Interval, error) {
	if len(ids) == 0 || len(ids) > 50 || !end.After(start) {
		return nil, errors.New("invalid_freebusy_query")
	}
	items := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, map[string]string{"id": id})
	}
	var out struct {
		Calendars map[string]struct {
			Busy   []Interval        `json:"busy"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"calendars"`
	}
	err := c.request(ctx, token, "POST", "freeBusy", map[string]any{"timeMin": start, "timeMax": end, "items": items}, &out)
	if err != nil {
		return nil, err
	}
	var busy []Interval
	for _, id := range ids {
		cal, ok := out.Calendars[id]
		if !ok || len(cal.Errors) > 0 {
			return nil, errors.New("google_freebusy_incomplete")
		}
		for _, v := range cal.Busy {
			if v.Start.IsZero() || !v.End.After(v.Start) {
				return nil, errors.New("google_freebusy_invalid")
			}
			busy = append(busy, v)
		}
	}
	return busy, nil
}
func (c *Client) Calendar(ctx context.Context, token *oauth2.Token, id string) (Calendar, error) {
	var out Calendar
	err := c.request(ctx, token, "GET", "users/me/calendarList/"+url.PathEscape(id), nil, &out)
	if err == nil && (out.ID == "" || out.ID != id) {
		err = errors.New("google_calendar_identity_mismatch")
	}
	return out, err
}

type EventTime struct {
	DateTime time.Time `json:"dateTime"`
}
type Event struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status,omitempty"`
	Summary            string    `json:"summary,omitempty"`
	Description        string    `json:"description,omitempty"`
	Start              EventTime `json:"start"`
	End                EventTime `json:"end"`
	ExtendedProperties struct {
		Private map[string]string `json:"private"`
	} `json:"extendedProperties"`
}

func eventPath(calendar, id string) string {
	return "calendars/" + url.PathEscape(calendar) + "/events/" + url.PathEscape(id)
}
func (c *Client) Event(ctx context.Context, token *oauth2.Token, calendar, id string) (Event, error) {
	var out Event
	err := c.request(ctx, token, "GET", eventPath(calendar, id), nil, &out)
	return out, err
}
func (c *Client) Insert(ctx context.Context, token *oauth2.Token, calendar string, event Event) (Event, error) {
	var out Event
	err := c.request(ctx, token, "POST", "calendars/"+url.PathEscape(calendar)+"/events?sendUpdates=none", event, &out)
	return out, err
}
func (c *Client) Delete(ctx context.Context, token *oauth2.Token, calendar, id string) error {
	err := c.request(ctx, token, "DELETE", eventPath(calendar, id)+"?sendUpdates=none", nil, nil)
	if HasStatus(err, 404) || HasStatus(err, 410) {
		return nil
	}
	return err
}
