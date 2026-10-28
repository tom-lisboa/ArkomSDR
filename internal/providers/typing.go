package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Typing is best effort and also marks the inbound message as read at Twilio.
// It must only receive a verified incoming SID belonging to the allowed lead.
func (t *TwilioMessaging) Typing(ctx context.Context, phone, sid string) error {
	if !ValidMessageSID(sid) || (t.AllowedPhone != "" && phone != t.AllowedPhone) {
		return errors.New("typing_not_allowed")
	}
	endpoint := "https://messaging.twilio.com/v3/Indicators/Typing.json"
	if t.BaseURL != "" {
		endpoint = t.BaseURL + "/v3/Indicators/Typing.json"
	}
	b, _ := json.Marshal(map[string]string{"channel": "WHATSAPP", "messageId": sid})
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(b)))
	if e != nil {
		return errors.New("typing_invalid_request")
	}
	req.SetBasicAuth(t.Account, t.AuthToken)
	req.Header.Set("Content-Type", "application/json")
	resp, e := t.Client.Do(req)
	if e != nil {
		return errors.New("typing_unavailable")
	}
	defer resp.Body.Close()
	var result struct {
		Success bool `json:"success"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil || !result.Success {
		return errors.New("typing_unavailable")
	}
	return nil
}
