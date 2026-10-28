package providers

import (
	"arkom/bia/internal/domain"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMutationTimeoutIsAmbiguousWithoutIdempotency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	e := JSON(context.Background(), srv.Client(), "POST", srv.URL, nil, map[string]string{"text": "hello"}, nil, false)
	f := Classify(e)
	if !f.Ambiguous || f.Retryable {
		t.Fatalf("unsafe retry: %+v", f)
	}
	e = JSON(context.Background(), srv.Client(), "POST", srv.URL, nil, nil, nil, true)
	f = Classify(e)
	if f.Ambiguous || !f.Retryable {
		t.Fatal("idempotent retries disabled")
	}
}
func TestClickMaxDedupeAndWindow(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resultado":"negado","motivo":"ja_processada"}`))
	}))
	defer srv.Close()
	c := ClickMax{URL: srv.URL, Client: srv.Client(), ChannelID: "channel", Token: "not-real"}
	s := domain.State{Profile: domain.Form{Phone: "+5565999999999", ProviderLeadID: "provider-id"}, LastInbound: time.Now()}
	if _, e := c.Send(context.Background(), "key", s, "Olá", "reply", time.Now()); e != nil {
		t.Fatal(e)
	}
	s.LastInbound = time.Time{}
	if _, e := c.Send(context.Background(), "key2", s, "Olá", "welcome", time.Now()); e == nil {
		t.Fatal("free text sent without session")
	}
	if calls != 1 {
		t.Fatal("unexpected external call")
	}
}
func TestAudioRejectsUntrustedHostsBeforeDownload(t *testing.T) {
	a := Audio{Hosts: map[string]bool{"cdn.example": true}, HTTP: MediaHTTP()}
	for _, u := range []string{"http://cdn.example/a.ogg", "https://localhost/a.ogg", "https://cdn.example:444/a.ogg", "https://user:password@cdn.example/a.ogg"} {
		if _, e := a.Transcribe(context.Background(), u); e == nil || !strings.Contains(e.Error(), "untrusted") {
			t.Fatalf("accepted %s", u)
		}
	}
}
