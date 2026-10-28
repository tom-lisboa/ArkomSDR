package voiceworker

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/providers"
)

type Provider interface {
	Permission(context.Context, string, string) (string, error)
	Dial(context.Context, Request, string) (string, error)
	Hangup(context.Context, string) error
	MessageTime(context.Context, string, string, string) (time.Time, error)
}
type Twilio struct {
	AllowedPhone                                      string
	Account, AuthToken, Sender, ContentSID, PublicURL string
	Client                                            *http.Client
	BaseURL                                           string
	MaxSeconds                                        int
}

func (t *Twilio) request(ctx context.Context, method, path string, values url.Values, out any) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, base+"/2010-04-01/Accounts/"+url.PathEscape(t.Account)+path, body)
	if e != nil {
		return &providers.Failure{Code: "voice_invalid_request"}
	}
	req.SetBasicAuth(t.Account, t.AuthToken)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, e := t.Client.Do(req)
	if e != nil {
		return &providers.Failure{Code: "voice_transport", Ambiguous: method != "GET"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &providers.Failure{Code: "voice_provider_rejected", Ambiguous: method != "GET" && resp.StatusCode >= 500}
	}
	if out != nil && json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(out) != nil {
		return &providers.Failure{Code: "voice_invalid_response", Ambiguous: method != "GET"}
	}
	return nil
}
func (t *Twilio) Permission(ctx context.Context, to, sender string) (string, error) {
	if t.AllowedPhone != "" && to != t.AllowedPhone {
		return "", &providers.Failure{Code: "voice_recipient_not_allowed"}
	}
	var r struct {
		SID string `json:"sid"`
	}
	e := t.request(ctx, "POST", "/Messages.json", url.Values{"From": {"whatsapp:" + sender}, "To": {"whatsapp:" + to}, "ContentSid": {t.ContentSID}}, &r)
	if e == nil && !providers.ValidMessageSID(r.SID) {
		e = &providers.Failure{Code: "voice_missing_message_sid", Ambiguous: true}
	}
	return r.SID, e
}
func (t *Twilio) Dial(ctx context.Context, r Request, to string) (string, error) {
	if t.AllowedPhone != "" && to != t.AllowedPhone {
		return "", &providers.Failure{Code: "voice_recipient_not_allowed"}
	}
	var result struct {
		SID string `json:"sid"`
	}
	e := t.request(ctx, "POST", "/Calls.json", url.Values{"From": {"whatsapp:" + r.Sender}, "To": {"whatsapp:" + to}, "Url": {t.PublicURL + "/voice/twiml/" + r.ID}, "Method": {"POST"}, "StatusCallback": {t.PublicURL + "/voice/status/" + r.ID}, "StatusCallbackMethod": {"POST"}, "StatusCallbackEvent": {"initiated", "ringing", "answered", "completed"}, "Timeout": {"25"}, "TimeLimit": {strconv.Itoa(t.MaxSeconds)}}, &result)
	if e == nil && !sid(result.SID, "CA") {
		e = &providers.Failure{Code: "voice_missing_call_sid", Ambiguous: true}
	}
	return result.SID, e
}
func (t *Twilio) Hangup(ctx context.Context, call string) error {
	if !sid(call, "CA") {
		return errors.New("invalid_call_sid")
	}
	return t.request(ctx, "POST", "/Calls/"+call+".json", url.Values{"Status": {"completed"}}, nil)
}
func (t *Twilio) MessageTime(ctx context.Context, id, from, to string) (time.Time, error) {
	if !providers.ValidMessageSID(id) {
		return time.Time{}, ErrBlocked
	}
	var m struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Created string `json:"date_created"`
		Body    string `json:"body"`
	}
	if e := t.request(ctx, "GET", "/Messages/"+id+".json", nil, &m); e != nil {
		return time.Time{}, e
	}
	if m.From != from || m.To != to || m.Body != "VOICE_CALL_REQUEST" {
		return time.Time{}, ErrBlocked
	}
	at, e := time.Parse(time.RFC1123Z, m.Created)
	if e != nil {
		at, e = time.Parse(time.RFC1123, m.Created)
	}
	return at, e
}
func sid(s, prefix string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// Twilio signs the configured absolute URL plus sorted form fields. Never use
// untrusted Host or X-Forwarded-* headers to reconstruct this URL.
func Signature(token, absolute string, form url.Values) string {
	var b strings.Builder
	b.WriteString(absolute)
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		values := append([]string(nil), form[k]...)
		sort.Strings(values)
		last := ""
		for i, v := range values {
			if i == 0 || v != last {
				b.WriteString(k)
				b.WriteString(v)
			}
			last = v
		}
	}
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func ticket(secret, id, call string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(id + "\x00" + call))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func streamXML(public, secret, id, call string) string {
	escape := func(s string) string { var b strings.Builder; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return `<Response><Connect><Stream url="` + escape(strings.Replace(public, "https://", "wss://", 1)+"/voice/stream") + `"><Parameter name="request_id" value="` + escape(id) + `"/><Parameter name="ticket" value="` + escape(ticket(secret, id, call)) + `"/></Stream></Connect><Hangup/></Response>`
}
