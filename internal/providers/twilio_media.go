package providers

import (
	"arkom/bia/internal/conversation"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Attachments reads Twilio's canonical inventory, never arbitrary webhook URLs.
// Only metadata is fetched here. The media worker downloads the actual bytes.
func (t *TwilioMessaging) Attachments(ctx context.Context, m TwilioMessage) ([]conversation.Attachment, error) {
	if m.NumMedia == "" || m.NumMedia == "0" {
		return nil, nil
	}
	n, e := strconv.Atoi(m.NumMedia)
	if e != nil || n < 1 || n > 5 || !ValidMessageSID(m.SID) {
		return nil, errors.New("twilio_invalid_media_count")
	}
	base := t.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	path := "/2010-04-01/Accounts/" + t.Account + "/Messages/" + m.SID + "/Media"
	req, e := http.NewRequestWithContext(ctx, "GET", base+path+".json?PageSize=5", nil)
	if e != nil {
		return nil, e
	}
	req.SetBasicAuth(t.Account, t.AuthToken)
	resp, e := t.Client.Do(req)
	if e != nil {
		return nil, errors.New("twilio_media_inventory_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("twilio_media_inventory_failed")
	}
	var data struct {
		Media []struct {
			SID     string `json:"sid"`
			Account string `json:"account_sid"`
			Parent  string `json:"parent_sid"`
			MIME    string `json:"content_type"`
		} `json:"media_list"`
		Next *string `json:"next_page_uri"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&data) != nil || len(data.Media) != n || (data.Next != nil && *data.Next != "") {
		return nil, errors.New("twilio_media_inventory_incomplete")
	}
	var out []conversation.Attachment
	seen := map[string]bool{}
	for _, a := range data.Media {
		if !validMediaSID(a.SID) || a.Account != t.Account || a.Parent != m.SID || seen[a.SID] {
			return nil, errors.New("twilio_media_mismatch")
		}
		seen[a.SID] = true
		kind := "document"
		if strings.HasPrefix(a.MIME, "audio/") || a.MIME == "application/ogg" {
			kind = "audio"
		}
		if strings.HasPrefix(a.MIME, "image/") {
			kind = "image"
		}
		out = append(out, conversation.Attachment{ID: a.SID, Kind: kind, MIME: a.MIME, URL: "https://api.twilio.com" + path + "/" + a.SID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// WebhookAttachments reads the signed delivery, which may be ahead of the REST
// message's num_media while status=receiving. Each URL must identify a media
// resource of this exact account and parent message; CDN URLs are never accepted.
func (t *TwilioMessaging) WebhookAttachments(ctx context.Context, m TwilioMessage, form url.Values) ([]conversation.Attachment, error) {
	raw := form.Get("NumMedia")
	if raw == "" {
		return t.Attachments(ctx, m)
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 || n > 5 || !ValidMessageSID(m.SID) {
		return nil, errors.New("twilio_invalid_media_count")
	}
	if n == 0 {
		if m.NumMedia != "" && m.NumMedia != "0" {
			return nil, errors.New("twilio_media_count_mismatch")
		}
		return nil, nil
	}
	var out []conversation.Attachment
	seen := map[string]bool{}
	validator := Downloader{Account: t.Account, AuthToken: t.AuthToken}
	for i := 0; i < n; i++ {
		u, e := url.Parse(form.Get("MediaUrl" + strconv.Itoa(i)))
		if e != nil || !safeMediaURL(u) || !validator.twilioURL(u) {
			return nil, errors.New("twilio_untrusted_attachment")
		}
		parts := strings.Split(u.Path, "/")
		id := parts[7]
		if parts[5] != m.SID || seen[id] {
			return nil, errors.New("twilio_media_parent_mismatch")
		}
		seen[id] = true
		ct, _, e := mime.ParseMediaType(form.Get("MediaContentType" + strconv.Itoa(i)))
		if e != nil {
			return nil, errors.New("twilio_invalid_media_type")
		}
		kind := "document"
		if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
			kind = "audio"
		}
		if strings.HasPrefix(ct, "image/") {
			kind = "image"
		}
		out = append(out, conversation.Attachment{ID: id, Kind: kind, MIME: ct, URL: u.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
