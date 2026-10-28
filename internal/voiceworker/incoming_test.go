package voiceworker

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestIncomingEndsCallWithValidSignatureWhileOutboundDisabled(t *testing.T) {
	s := &Server{Account: "AC" + strings.Repeat("1", 32), AuthToken: "test-only", Sender: "+5565999999901", PublicURL: "https://voice.example"}
	for _, scenario := range []struct {
		name, account, to string
		tamper            bool
		want              int
	}{
		{"valid", s.Account, "whatsapp:" + s.Sender, false, 200},
		{"tampered", s.Account, "whatsapp:" + s.Sender, true, 403},
		{"wrong-account", "AC" + strings.Repeat("2", 32), "whatsapp:" + s.Sender, false, 403},
		{"wrong-recipient", s.Account, "whatsapp:+5565999999902", false, 403},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := url.Values{"AccountSid": {scenario.account}, "To": {scenario.to}}
			sig := Signature(s.AuthToken, s.PublicURL+"/voice/incoming", f)
			if scenario.tamper {
				f.Set("To", "whatsapp:+5565999999902")
			}
			r := httptest.NewRequest(http.MethodPost, "/voice/incoming", strings.NewReader(f.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-Twilio-Signature", sig)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != scenario.want {
				t.Fatalf("got %d, want %d", w.Code, scenario.want)
			}
			if w.Code == 200 && w.Body.String() != "<Response><Hangup/></Response>" {
				t.Fatal(w.Body.String())
			}
		})
	}
	// Deployment must not accidentally enable dialing or permission handling.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/voice/permission", nil))
	if w.Code != 503 {
		t.Fatalf("permission enabled unexpectedly: %d", w.Code)
	}
}
