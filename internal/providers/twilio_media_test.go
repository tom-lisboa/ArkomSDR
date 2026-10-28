package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type mediaRoundTrip func(*http.Request) (*http.Response, error)

func (f mediaRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTwilioDownloadScopesCredentialsAndRedirects(t *testing.T) {
	account := "AC" + strings.Repeat("1", 32)
	sid := "MM" + strings.Repeat("2", 32)
	asset := "ME" + strings.Repeat("3", 32)
	raw := "https://api.twilio.com/2010-04-01/Accounts/" + account + "/Messages/" + sid + "/Media/" + asset
	calls := 0
	d := Downloader{Account: account, AuthToken: "secret", HTTP: &http.Client{Transport: mediaRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			user, pass, ok := r.BasicAuth()
			if !ok || user != account || pass != "secret" {
				t.Fatal("missing scoped auth")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://cdn.example/signed?token=private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		if r.Header.Get("Authorization") != "" || r.URL.Host != "cdn.example" {
			t.Fatal("credentials escaped account endpoint")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/ogg; codecs=opus"}}, Body: io.NopCloser(strings.NewReader("audio bytes")), Request: r}, nil
	})}}
	b, ct, e := d.Download(context.Background(), raw)
	if e != nil || ct != "audio/ogg" || string(b) != "audio bytes" || calls != 2 {
		t.Fatal(ct, e, calls)
	}
	for _, bad := range []string{strings.Replace(raw, account, "AC"+strings.Repeat("4", 32), 1), raw + ".json", raw + "?foo=bar", strings.Replace(raw, "api.twilio.com", "api.twilio.com.evil.test", 1), strings.Replace(raw, "https:", "http:", 1)} {
		if _, _, e := d.Download(context.Background(), bad); e == nil {
			t.Fatal("untrusted accepted")
		}
	}
	if calls != 2 {
		t.Fatal("untrusted URL caused request")
	}
	// Production transport must refuse private redirects before making a connection.
	safe := MediaHTTP()
	req, _ := http.NewRequest("GET", "https://127.0.0.1/private", nil)
	if _, e := safe.Do(req); e == nil {
		t.Fatal("private address accepted")
	}
}
func TestTwilioInventoryAndTyping(t *testing.T) {
	account := "AC" + strings.Repeat("1", 32)
	sid := "MM" + strings.Repeat("2", 32)
	asset := "ME" + strings.Repeat("3", 32)
	foreign := false
	typed := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if u != account || p != "secret" {
			t.Fatal("missing auth")
		}
		if strings.Contains(r.URL.Path, "Indicators") {
			typed++
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["messageId"] != sid || b["channel"] != "WHATSAPP" || r.Method != "POST" {
				t.Fatal(b)
			}
			fmt.Fprint(w, `{"success":true}`)
			return
		}
		owner := account
		if foreign {
			owner = "another-account"
		}
		fmt.Fprintf(w, `{"media_list":[{"sid":%q,"account_sid":%q,"parent_sid":%q,"content_type":"audio/ogg"}]}`, asset, owner, sid)
	}))
	defer server.Close()
	tw := TwilioMessaging{Account: account, AuthToken: "secret", Client: server.Client(), BaseURL: server.URL, AllowedPhone: "+5565999999900"}
	attachments, e := tw.Attachments(context.Background(), TwilioMessage{SID: sid, NumMedia: "1"})
	if e != nil || len(attachments) != 1 || attachments[0].Kind != "audio" || !strings.HasPrefix(attachments[0].URL, "https://api.twilio.com/") {
		t.Fatal(attachments, e)
	}
	foreign = true
	if _, e := tw.Attachments(context.Background(), TwilioMessage{SID: sid, NumMedia: "1"}); e == nil {
		t.Fatal("foreign account accepted")
	}
	if e := tw.Typing(context.Background(), tw.AllowedPhone, sid); e != nil {
		t.Fatal(e)
	}
	if e := tw.Typing(context.Background(), "+5565999999901", sid); e == nil {
		t.Fatal("typing recipient fence bypassed")
	}
	if typed != 1 {
		t.Fatal(typed)
	}
}

func TestSignedMediaPrecedesRESTInventory(t *testing.T) {
	account := "AC" + strings.Repeat("1", 32)
	sid := "MM" + strings.Repeat("2", 32)
	asset := "ME" + strings.Repeat("3", 32)
	tw := TwilioMessaging{Account: account, AuthToken: "secret"}
	canonical := "https://api.twilio.com/2010-04-01/Accounts/" + account + "/Messages/" + sid + "/Media/" + asset
	form := url.Values{"NumMedia": {"1"}, "MediaUrl0": {canonical}, "MediaContentType0": {"image/jpeg"}}
	m := TwilioMessage{SID: sid, Status: "receiving", NumMedia: "0"}
	a, e := tw.WebhookAttachments(context.Background(), m, form)
	if e != nil || len(a) != 1 || a[0].Kind != "image" {
		t.Fatal(a, e)
	}
	for _, bad := range []string{"https://untrusted.example/file", strings.Replace(canonical, sid, "MM"+strings.Repeat("4", 32), 1), strings.Replace(canonical, account, "AC"+strings.Repeat("4", 32), 1)} {
		form.Set("MediaUrl0", bad)
		if _, e := tw.WebhookAttachments(context.Background(), m, form); e == nil {
			t.Fatal("untrusted attachment accepted")
		}
	}
}
