// Package lumi implements the existing, instance-scoped Lumi WhatsApp API.
package lumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	URL, UserKey string
	HTTP         *http.Client
}
type Instance struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"`
	Device string `json:"whatsapp_device_jid"`
}
type Session struct {
	Status      string     `json:"status"`
	QRAvailable bool       `json:"qr_available"`
	QRExpiresAt *time.Time `json:"qr_expires_at,omitempty"`
}
type Receipt struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	InstanceID string `json:"instance_id"`
}

// Failure deliberately omits upstream bodies, URLs and credentials.
type Failure struct {
	Status int
	Code   string
}

func (e *Failure) Error() string { return e.Code }

func New(base, userKey string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("invalid Lumi URL")
	}
	return &Client{URL: strings.TrimRight(base, "/"), UserKey: userKey, HTTP: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, method, path, key string, body any, out any) ([]byte, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, &Failure{Code: "lumi_request_failed"}
	}
	req.Header.Set("X-API-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &Failure{Code: "lumi_transport_uncertain"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &Failure{Status: response.StatusCode, Code: fmt.Sprintf("lumi_http_%d", response.StatusCode)}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, &Failure{Code: "lumi_response_uncertain"}
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return nil, &Failure{Code: "lumi_response_uncertain"}
	}
	return raw, nil
}
func (c *Client) Create(ctx context.Context, name string) (Instance, error) {
	var out Instance
	_, err := c.call(ctx, "POST", "/api/instances", c.UserKey, map[string]string{"name": name}, &out)
	if err == nil && (out.ID == "" || out.APIKey == "") {
		err = &Failure{Code: "lumi_response_uncertain"}
	}
	return out, err
}
func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out Instance
	_, err := c.call(ctx, "GET", "/api/instances/"+url.PathEscape(id), c.UserKey, nil, &out)
	return out, err
}
func (c *Client) Session(ctx context.Context, id string) (Session, error) {
	var out Session
	_, err := c.call(ctx, "GET", "/api/instances/"+url.PathEscape(id)+"/session", c.UserKey, nil, &out)
	return out, err
}
func (c *Client) Command(ctx context.Context, id, command string) error {
	if command != "qr" && command != "logout" {
		return errors.New("unsupported Lumi command")
	}
	_, err := c.call(ctx, "POST", "/api/instances/"+url.PathEscape(id)+"/"+command, c.UserKey, map[string]any{}, &Receipt{})
	return err
}
func (c *Client) QR(ctx context.Context, id string) ([]byte, error) {
	return c.call(ctx, "GET", "/api/instances/"+url.PathEscape(id)+"/qr.png", c.UserKey, nil, nil)
}
func (c *Client) Send(ctx context.Context, key, phone, text string) (Receipt, error) {
	var out Receipt
	_, err := c.call(ctx, "POST", "/message/sendText", key, map[string]string{"number": strings.TrimPrefix(phone, "+"), "text": text}, &out)
	if err == nil && out.ID == "" {
		err = &Failure{Code: "lumi_response_uncertain"}
	}
	return out, err
}
func (c *Client) Delivery(ctx context.Context, key, id string) (Receipt, error) {
	var out Receipt
	_, err := c.call(ctx, "GET", "/v1/outbound-messages/"+url.PathEscape(id), key, nil, &out)
	return out, err
}
