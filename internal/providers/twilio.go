package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TwilioMessaging records an attempt before making a non-idempotent POST.
// An interrupted attempt without a SID requires review; it is never resent.
type TwilioMessaging struct {
	FormContentSID, ContentBaseURL                    string
	Pool                                              *pgxpool.Pool
	Account, AuthToken, Sender, AllowedPhone, BaseURL string
	Client                                            *http.Client
	PollInterval                                      time.Duration
}
type TwilioMessage struct {
	SID       string `json:"sid"`
	From      string `json:"from"`
	To        string `json:"to"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	Created   string `json:"date_created"`
	NumMedia  string `json:"num_media"`
	ErrorCode *int   `json:"error_code"`
}

func (t *TwilioMessaging) request(ctx context.Context, method, path string, form url.Values) (TwilioMessage, error) {
	var m TwilioMessage
	base := t.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, base+"/2010-04-01/Accounts/"+url.PathEscape(t.Account)+path, body)
	if e != nil {
		return m, &Failure{Code: "twilio_invalid_request"}
	}
	req.SetBasicAuth(t.Account, t.AuthToken)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, e := t.Client.Do(req)
	if e != nil {
		return m, &Failure{Code: "twilio_transport", Ambiguous: method != "GET"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var out struct {
			Code int `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out)
		return m, &Failure{Code: "twilio_" + strconv.Itoa(out.Code), Ambiguous: method != "GET" && resp.StatusCode >= 500}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&m) != nil {
		return m, &Failure{Code: "twilio_invalid_response", Ambiguous: method != "GET"}
	}
	return m, nil
}
func ValidMessageSID(id string) bool {
	if len(id) != 34 || (!strings.HasPrefix(id, "SM") && !strings.HasPrefix(id, "MM")) {
		return false
	}
	_, e := hex.DecodeString(id[2:])
	return e == nil
}
func (t *TwilioMessaging) Fetch(ctx context.Context, id string) (TwilioMessage, error) {
	if !ValidMessageSID(id) {
		return TwilioMessage{}, &Failure{Code: "twilio_invalid_sid"}
	}
	return t.request(ctx, "GET", "/Messages/"+id+".json", nil)
}
func MessageCreated(m TwilioMessage) (time.Time, error) {
	at, e := time.Parse(time.RFC1123Z, m.Created)
	if e != nil {
		at, e = time.Parse(time.RFC1123, m.Created)
	}
	return at, e
}
func (t *TwilioMessaging) Send(ctx context.Context, key string, s domain.State, text, purpose string, now time.Time) (Receipt, error) {
	if len([]rune(text)) > 1000 || strings.TrimSpace(text) == "" {
		return Receipt{}, &Failure{Code: "twilio_invalid_text"}
	}
	if purpose == "form_contact" && !messageWindowOpen(s, now) {
		if text != domain.FormContactText {
			return Receipt{}, &Failure{Code: "form_template_text_mismatch"}
		}
		if err := t.formTemplateReady(ctx); err != nil {
			return Receipt{}, err
		}
		return t.send(ctx, key, s, text, url.Values{"ContentSid": {t.FormContentSID}}, now)
	}
	return t.send(ctx, key, s, text, url.Values{"Body": {text}}, now)
}

func messageWindowOpen(s domain.State, now time.Time) bool {
	return !s.LastInbound.IsZero() && now.Sub(s.LastInbound) < 23*time.Hour && !s.LastInbound.After(now.Add(time.Minute))
}

// WhatsApp audio has no caption. Keep the same write-before-POST delivery ledger.
func (t *TwilioMessaging) SendAudio(ctx context.Context, key string, s domain.State, mediaURL string, now time.Time) (Receipt, error) {
	if !domain.ValidHTTPS(mediaURL) {
		return Receipt{}, &Failure{Code: "twilio_invalid_media_url"}
	}
	return t.send(ctx, key, s, "audio\x00"+mediaURL, url.Values{"MediaUrl": {mediaURL}}, now)
}

func (t *TwilioMessaging) send(ctx context.Context, key string, s domain.State, content string, form url.Values, now time.Time) (Receipt, error) {
	if t.AllowedPhone != "" && s.Profile.Phone != t.AllowedPhone {
		return Receipt{}, &Failure{Code: "twilio_recipient_not_allowed"}
	}
	if !messageWindowOpen(s, now) && form.Get("ContentSid") == "" {
		return Receipt{}, &Failure{Code: "twilio_requires_approved_template"}
	}
	sum := sha256.Sum256([]byte(s.Profile.Phone + "\x00" + content))
	fp := hex.EncodeToString(sum[:])
	ct, e := t.Pool.Exec(ctx, `INSERT INTO bia_go.twilio_messages(id,phone,fingerprint) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, key, s.Profile.Phone, fp)
	if e != nil {
		return Receipt{}, e
	}
	var m TwilioMessage
	if ct.RowsAffected() == 1 {
		form.Set("From", "whatsapp:"+t.Sender)
		form.Set("To", "whatsapp:"+s.Profile.Phone)
		m, e = t.request(ctx, "POST", "/Messages.json", form)
		if e != nil {
			_, _ = t.Pool.Exec(ctx, `UPDATE bia_go.twilio_messages SET error_code=$2,updated_at=now() WHERE id=$1`, key, Classify(e).Code)
			return Receipt{}, e
		}
		if !ValidMessageSID(m.SID) {
			return Receipt{}, &Failure{Code: "twilio_missing_sid", Ambiguous: true}
		}
		if _, e = t.Pool.Exec(ctx, `UPDATE bia_go.twilio_messages SET sid=$2,status=$3,updated_at=now() WHERE id=$1`, key, m.SID, m.Status); e != nil {
			return Receipt{}, &Failure{Code: "twilio_receipt_storage", Ambiguous: true}
		}
	} else {
		var old string
		if e = t.Pool.QueryRow(ctx, `SELECT COALESCE(sid,''),fingerprint FROM bia_go.twilio_messages WHERE id=$1`, key).Scan(&m.SID, &old); e != nil {
			return Receipt{}, e
		}
		if old != fp {
			return Receipt{}, &Failure{Code: "twilio_idempotency_conflict", Ambiguous: true}
		}
		if m.SID == "" {
			return Receipt{}, &Failure{Code: "twilio_interrupted_send", Ambiguous: true}
		}
	}
	return t.awaitSent(ctx, key, m)
}

func (t *TwilioMessaging) formTemplateReady(ctx context.Context) error {
	wait := func(code string) error { return &Deferred{Code: code, After: time.Minute} }
	if t.FormContentSID == "" {
		return wait("form_template_not_configured")
	}
	base := t.ContentBaseURL
	if base == "" {
		base = "https://content.twilio.com"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/Content/"+url.PathEscape(t.FormContentSID)+"/ApprovalRequests", nil)
	if err != nil {
		return &Failure{Code: "invalid_template_request"}
	}
	req.SetBasicAuth(t.Account, t.AuthToken)
	resp, err := t.Client.Do(req)
	if err != nil {
		return wait("form_template_status_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return wait("form_template_status_unavailable")
	}
	var out struct {
		WhatsApp struct {
			Status string `json:"status"`
		} `json:"whatsapp"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out) != nil {
		return wait("form_template_status_unavailable")
	}
	if out.WhatsApp.Status != "approved" {
		return wait("form_template_" + out.WhatsApp.Status)
	}
	return nil
}
func (t *TwilioMessaging) awaitSent(parent context.Context, key string, m TwilioMessage) (Receipt, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	interval := t.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		switch m.Status {
		case "sent", "delivered", "read":
			return Receipt{ID: m.SID, Status: "sent"}, nil
		case "failed", "undelivered", "canceled":
			code := "twilio_delivery_failed"
			if m.ErrorCode != nil {
				code = "twilio_" + strconv.Itoa(*m.ErrorCode)
			}
			return Receipt{}, &Failure{Code: code}
		}
		select {
		case <-ctx.Done():
			return Receipt{}, &Failure{Code: "twilio_delivery_unconfirmed", Ambiguous: true}
		case <-time.After(interval):
		}
		next, e := t.Fetch(ctx, m.SID)
		if e != nil {
			return Receipt{}, &Failure{Code: "twilio_receipt_unavailable", Ambiguous: true}
		}
		if next.SID != m.SID {
			return Receipt{}, &Failure{Code: "twilio_receipt_mismatch", Ambiguous: true}
		}
		m = next
		if t.Pool != nil {
			if _, e = t.Pool.Exec(ctx, `UPDATE bia_go.twilio_messages SET status=$2,updated_at=now() WHERE id=$1`, key, m.Status); e != nil {
				return Receipt{}, &Failure{Code: "twilio_receipt_storage", Ambiguous: true}
			}
		}
	}
}
