package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWhatsAppSignedInboundDedupAndOptOutPreserved(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	sender := "+5565999999901"
	phone := "+5565999999900"
	account := "AC" + strings.Repeat("1", 32)
	sid := "SM" + strings.Repeat("2", 32)
	at := time.Now().UTC().Truncate(time.Second)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(providers.TwilioMessage{SID: sid, From: "whatsapp:" + phone, To: "whatsapp:" + sender, Body: "Oi", Status: "received", Created: at.Format(time.RFC1123Z)})
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: account, AuthToken: "secret", Sender: sender, AllowedPhone: phone, Client: mock.Client(), BaseURL: mock.URL}
	srv := &voiceworker.Server{Repo: voiceworker.Repository{DB: db}, Account: account, AuthToken: "secret", Sender: sender, PublicURL: "https://voice.example", Mode: "live", TestPhone: phone, TimeZone: "America/Cuiaba", Messages: tw}
	f := url.Values{"AccountSid": {account}, "MessageSid": {sid}, "From": {"whatsapp:" + phone}, "To": {"whatsapp:" + sender}, "Body": {"Oi"}, "ProfileName": {"Pessoa de teste"}}
	post := func(signed bool) int {
		r := httptest.NewRequest("POST", "/voice/messages", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if signed {
			r.Header.Set("X-Twilio-Signature", voiceworker.Signature("secret", srv.PublicURL+"/voice/messages", f))
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := post(false); code != 403 {
		t.Fatal(code)
	}
	if code := post(true); code != 200 {
		t.Fatal(code)
	}
	st, e := db.Lead(ctx, domain.LeadKey(phone))
	if e != nil || !st.Profile.Consent || st.Owner != "go" {
		t.Fatal(st, e)
	}
	// Existing state must survive a replay, including operator control and opt-out.
	_, e = db.Pool.Exec(ctx, `UPDATE bia_go.leads SET state=jsonb_set(jsonb_set(state,'{opted_out}','true'),'{owner}','"n8n"') WHERE id=$1`, st.ID)
	if e != nil {
		t.Fatal(e)
	}
	if code := post(true); code != 200 {
		t.Fatal(code)
	}
	st, e = db.Lead(ctx, st.ID)
	if e != nil || !st.OptedOut || st.Owner != "n8n" {
		t.Fatal(st, e)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.worker_tasks WHERE kind='inbound'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	f.Set("From", "whatsapp:+5565999999902")
	if code := post(true); code != 200 {
		t.Fatal(code)
	}
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.leads`).Scan(&n)
	if n != 1 {
		t.Fatal("unauthorized test contact accepted", n)
	}
}
func TestTwilioSendLedgerAndRecipientFence(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	posts := 0
	sid := "SM" + strings.Repeat("2", 32)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			fmt.Fprintf(w, `{"sid":%q,"status":"queued"}`, sid)
		} else {
			fmt.Fprintf(w, `{"sid":%q,"status":"delivered"}`, sid)
		}
	}))
	defer mock.Close()
	phone := "+5565999999900"
	now := time.Now().UTC()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("1", 32), AuthToken: "secret", Sender: "+5565999999901", AllowedPhone: phone, Client: mock.Client(), BaseURL: mock.URL, PollInterval: time.Millisecond}
	st := domain.State{Profile: domain.Form{Phone: phone}, LastInbound: now.Add(-time.Minute)}
	for i := 0; i < 2; i++ {
		r, e := tw.Send(ctx, "send-one", st, "Teste", "reply", now)
		if e != nil || r.Status != "sent" {
			t.Fatal(r, e)
		}
	}
	if posts != 1 {
		t.Fatal("duplicated POST", posts)
	}
	if _, e := tw.Send(ctx, "send-one", st, "Changed", "reply", now); e == nil {
		t.Fatal("conflict accepted")
	}
	// A crash after reservation but before a response must never re-POST.
	_, e := db.Pool.Exec(ctx, `UPDATE bia_go.twilio_messages SET sid=NULL WHERE id='send-one'`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := tw.Send(ctx, "send-one", st, "Teste", "reply", now); e == nil || !providers.Classify(e).Ambiguous {
		t.Fatal(e)
	}
	st.Profile.Phone = "+5565999999902"
	if _, e := tw.Send(ctx, "other", st, "Teste", "reply", now); e == nil {
		t.Fatal("recipient fence bypass")
	}
	st.Profile.Phone = phone
	st.LastInbound = now.Add(-24 * time.Hour)
	if _, e := tw.Send(ctx, "expired", st, "Teste", "reply", now); e == nil {
		t.Fatal("window bypass")
	}
	if posts != 1 {
		t.Fatal(posts)
	}
}

func TestWhatsAppAttachmentEntersMediaQueueOnce(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	account := "AC" + strings.Repeat("1", 32)
	sid := "MM" + strings.Repeat("2", 32)
	asset := "ME" + strings.Repeat("3", 32)
	phone := "+5565999999900"
	sender := "+5565999999901"
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Media.json") {
			fmt.Fprintf(w, `{"media_list":[{"sid":%q,"parent_sid":%q,"account_sid":%q,"content_type":"audio/ogg"}]}`, asset, sid, account)
			return
		}
		json.NewEncoder(w).Encode(providers.TwilioMessage{SID: sid, From: "whatsapp:" + phone, To: "whatsapp:" + sender, Status: "receiving", Created: now.Format(time.RFC1123Z), NumMedia: "0"})
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: account, AuthToken: "secret", Sender: sender, Client: mock.Client(), BaseURL: mock.URL}
	srv := &voiceworker.Server{Repo: voiceworker.Repository{DB: db}, Account: account, AuthToken: "secret", Sender: sender, PublicURL: "https://voice.example", Mode: "live", TestPhone: phone, TimeZone: "America/Cuiaba", Messages: tw}
	// A signed canonical attachment is durable even before REST metadata catches up.
	f := url.Values{"AccountSid": {account}, "MessageSid": {sid}, "From": {"whatsapp:" + phone}, "To": {"whatsapp:" + sender}, "Body": {""}, "NumMedia": {"1"}, "MediaUrl0": {"https://api.twilio.com/2010-04-01/Accounts/" + account + "/Messages/" + sid + "/Media/" + asset}, "MediaContentType0": {"audio/ogg"}}
	for range 2 {
		r := httptest.NewRequest("POST", "/voice/messages", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-Twilio-Signature", voiceworker.Signature("secret", srv.PublicURL+"/voice/messages", f))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	ingest(t, db, store.DefaultWindowPolicy(), now)
	var count int
	var source string
	if e := db.Pool.QueryRow(ctx, `SELECT count(*),min(source_url) FROM bia_go.media_assets`).Scan(&count, &source); e != nil || count != 1 || !strings.HasPrefix(source, "https://api.twilio.com/") {
		t.Fatal(count, source, e)
	}
	task, e := db.ClaimTask(ctx, "media")
	if e != nil || task.Kind != "media.request" {
		t.Fatal(task, e)
	}
}
