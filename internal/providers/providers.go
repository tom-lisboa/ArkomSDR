package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"arkom/bia/internal/domain"
)

type Failure struct {
	Code      string
	Retryable bool
	Ambiguous bool
}

// Deferred is a prerequisite wait before any provider mutation, not a failed
// send. The durable job keeps its original expiry and retry budget.
type Deferred struct {
	Code  string
	After time.Duration
}

func (d *Deferred) Error() string { return d.Code }

func (e *Failure) Error() string { return e.Code }
func Classify(e error) *Failure {
	var f *Failure
	if errors.As(e, &f) {
		return f
	}
	return &Failure{Code: "provider_failure", Ambiguous: true}
}

type Receipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type Messenger interface {
	Send(context.Context, string, domain.State, string, string, time.Time) (Receipt, error)
}
type Calendar interface {
	Slots(context.Context, domain.State, time.Time) ([]domain.Slot, error)
	Book(context.Context, string, domain.State, domain.Slot, *domain.Booking) (domain.Booking, error)
	Cancel(context.Context, string, domain.Booking) error
}
type CRM interface {
	Record(context.Context, string, domain.State, domain.Effect) error
}
type Transcriber interface {
	Transcribe(context.Context, string) (string, error)
}

type Disabled struct{}

func (Disabled) Send(context.Context, string, domain.State, string, string, time.Time) (Receipt, error) {
	return Receipt{}, &Failure{Code: "messenger_not_configured"}
}
func (Disabled) Slots(context.Context, domain.State, time.Time) ([]domain.Slot, error) {
	return nil, nil
}
func (Disabled) Book(context.Context, string, domain.State, domain.Slot, *domain.Booking) (domain.Booking, error) {
	return domain.Booking{}, &Failure{Code: "calendar_not_configured"}
}
func (Disabled) Cancel(context.Context, string, domain.Booking) error {
	return &Failure{Code: "calendar_not_configured"}
}
func (Disabled) Record(context.Context, string, domain.State, domain.Effect) error {
	return &Failure{Code: "crm_not_configured"}
}

func NewHTTP() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

// JSON does not expose response bodies or authorization headers in errors.
// idempotent must only be true for a provider with a verified dedupe contract.
func JSON(ctx context.Context, c *http.Client, method, url string, headers map[string]string, body, out any, idempotent bool) error {
	var reader io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, url, reader)
	if e != nil {
		return &Failure{Code: "invalid_provider_url"}
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, e := c.Do(r)
	mutation := method != "GET"
	if e != nil {
		return &Failure{Code: "provider_transport", Retryable: !mutation || idempotent, Ambiguous: mutation && !idempotent}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Failure{Code: http.StatusText(resp.StatusCode), Retryable: resp.StatusCode == 429 || ((resp.StatusCode >= 500) && (!mutation || idempotent)), Ambiguous: mutation && !idempotent && resp.StatusCode >= 500}
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return e
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(b) > 2<<20 {
		return &Failure{Code: "provider_invalid_body", Ambiguous: mutation && !idempotent, Retryable: idempotent}
	}
	if e = json.Unmarshal(b, out); e != nil {
		return &Failure{Code: "provider_invalid_json", Ambiguous: mutation && !idempotent, Retryable: idempotent}
	}
	return nil
}

// Gateway is an explicit integration contract, not an invented ClickMax API.
// It is useful when the existing channel/CRM lives in another application.
type Gateway struct {
	URL, Token string
	Client     *http.Client
}

func (g *Gateway) headers(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + g.Token, "Idempotency-Key": key}
}
func (g *Gateway) Send(ctx context.Context, key string, s domain.State, text, purpose string, now time.Time) (Receipt, error) {
	var r Receipt
	windowOpen := !s.LastInbound.IsZero() && now.Sub(s.LastInbound) < 23*time.Hour
	e := JSON(ctx, g.Client, "POST", g.URL+"/messages", g.headers(key), map[string]any{"lead_id": s.ID, "contact": s.Profile, "text": text, "purpose": purpose, "requires_template": !windowOpen}, &r, true)
	if e == nil && (r.ID == "" || (r.Status != "accepted" && r.Status != "sent")) {
		e = &Failure{Code: "gateway_unconfirmed_receipt", Retryable: true}
	}
	return r, e
}
func (g *Gateway) Record(ctx context.Context, key string, s domain.State, fx domain.Effect) error {
	var r Receipt
	e := JSON(ctx, g.Client, "POST", g.URL+"/crm/events", g.headers(key), map[string]any{"lead_id": s.ID, "contact": s.Profile, "effect": fx}, &r, true)
	if e == nil && r.ID == "" {
		e = &Failure{Code: "crm_unconfirmed_receipt", Retryable: true}
	}
	return e
}
