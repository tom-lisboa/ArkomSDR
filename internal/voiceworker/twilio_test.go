package voiceworker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/providers"
)

func TestTwilioSignatureKnownVector(t *testing.T) {
	form := url.Values{"CallSid": {"CA1234567890ABCDE"}, "Caller": {"+14158675310"}, "Digits": {"1234"}, "From": {"+14158675310"}, "To": {"+18005551212"}}
	got := Signature("12345", "https://example.com/myapp.php?foo=1&bar=2", form)
	if got != "L/OH5YylLD5NRKLltdqwSvS0BnU=" {
		t.Fatal(got)
	}
}
func TestTwilioRequestsAndAmbiguousFailure(t *testing.T) {
	account := "AC" + strings.Repeat("1", 32)
	call := "CA" + strings.Repeat("2", 32)
	message := "SM" + strings.Repeat("3", 32)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		u, p, ok := r.BasicAuth()
		if !ok || u != account || p != "test" {
			t.Error("bad authentication")
		}
		if e := r.ParseForm(); e != nil {
			t.Error(e)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/Calls.json"):
			if r.Form.Get("From") != "whatsapp:+5565999999901" || r.Form.Get("To") != "whatsapp:+5565999999900" || r.Form.Get("TimeLimit") != "300" || len(r.Form["StatusCallbackEvent"]) != 4 {
				t.Error(r.Form)
			}
			w.WriteHeader(503)
		case strings.HasSuffix(r.URL.Path, "/Messages.json"):
			if r.Form.Get("ContentSid") == "" {
				t.Error("native permission content missing")
			}
			fmt.Fprintf(w, `{"sid":%q}`, message)
		default:
			fmt.Fprintf(w, `{"from":"whatsapp:+5565999999900","to":"whatsapp:+5565999999901","body":"VOICE_CALL_REQUEST","date_created":"Tue, 27 Oct 2026 12:00:00 +0000"}`)
		}
	}))
	defer server.Close()
	tw := &Twilio{Account: account, AuthToken: "test", ContentSID: "HX" + strings.Repeat("4", 32), PublicURL: "https://voice.example", Client: server.Client(), BaseURL: server.URL, MaxSeconds: 300}
	if id, e := tw.Permission(context.Background(), "+5565999999900", "+5565999999901"); e != nil || id != message {
		t.Fatal(id, e)
	}
	_, e := tw.Dial(context.Background(), Request{ID: "one", Sender: "+5565999999901", CallSID: call}, "+5565999999900")
	if e == nil || !providers.Classify(e).Ambiguous || requests != 2 {
		t.Fatal("unsafe retry or classification", requests, e)
	}
	at, e := tw.MessageTime(context.Background(), message, "whatsapp:+5565999999900", "whatsapp:+5565999999901")
	if e != nil || !at.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
		t.Fatal(at, e)
	}
	if _, e = tw.MessageTime(context.Background(), message, "whatsapp:+5565999999999", "whatsapp:+5565999999901"); e == nil {
		t.Fatal("wrong sender accepted")
	}
}

func TestVoiceTestRecipientFence(t *testing.T) {
	tw := &Twilio{AllowedPhone: "+5565999999900"}
	if _, e := tw.Permission(context.Background(), "+5565999999902", "+5565999999901"); e == nil {
		t.Fatal("permission outside test scope")
	}
	if _, e := tw.Dial(context.Background(), Request{Sender: "+5565999999901"}, "+5565999999902"); e == nil {
		t.Fatal("call outside test scope")
	}
}

func TestNativePermissionAcceptsMediaSID(t *testing.T) {
	message := "MM" + strings.Repeat("4", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"sid":%q}`, message) }))
	defer server.Close()
	tw := &Twilio{Account: "AC" + strings.Repeat("1", 32), AuthToken: "test", ContentSID: "HX" + strings.Repeat("2", 32), BaseURL: server.URL, Client: server.Client()}
	id, e := tw.Permission(context.Background(), "+5565999999900", "+5565999999901")
	if e != nil || id != message {
		t.Fatal(id, e)
	}
	for _, bad := range []string{"CA" + strings.Repeat("4", 32), "MM" + strings.Repeat("z", 32), "MM123"} {
		if providers.ValidMessageSID(bad) {
			t.Fatal("invalid message ID accepted")
		}
	}
}
