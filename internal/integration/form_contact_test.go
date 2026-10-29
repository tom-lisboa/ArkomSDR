package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/providers"
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

func TestAutomaticFormQueuesContactWithoutInbound(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	key := strings.Repeat("f", 48)
	h := (&httpapi.Server{DB: db, TestFormToken: key, VoiceSender: voiceSender, Policy: policy(), Mode: "live", FormAutoContact: true}).Handler()
	body := `{"name":"Ana","company":"Empresa Ana","phone":"+5577999749058","revenue":0,"goal":"Vender mais","consent":true}`
	post := func(id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/webhooks/test-form/"+key, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for range 2 {
		res := post("automatic-one", body)
		if res.Code != 202 || !strings.Contains(res.Body.String(), `"status":"contact_queued"`) || !strings.Contains(res.Body.String(), `"requires_whatsapp_action":false`) {
			t.Fatal(res.Code, res.Body)
		}
	}
	if post("automatic-one", strings.Replace(body, "Ana", "Bia", 1)).Code != 409 {
		t.Fatal("idempotency conflict ignored")
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE kind='event'`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	m := &countingMessenger{}
	w := runtimeWorker(db, time.Now(), m)
	drain(t, w)
	if m.Count.Load() != 0 {
		t.Fatal("opening sent before the form booking grace period")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE payload->>'purpose'='form_contact'`); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	s, err := db.Lead(ctx, domain.LeadKey("+5577999749058"))
	if err != nil || s.Profile.Company != "Empresa Ana" || s.Profile.Revenue == nil || *s.Profile.Revenue != 0 || !s.LastInbound.IsZero() || s.Owner != "go" || s.LastFormContactAt == nil || m.Count.Load() != 1 {
		t.Fatal(s, m.Count.Load(), err)
	}
	// A changed UUID is a new submission, but does not spam a recent contact.
	if post("automatic-two", body).Code != 202 {
		t.Fatal("second form failed")
	}
	drain(t, w)
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE payload->>'purpose'='form_contact'`); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if m.Count.Load() != 1 {
		t.Fatal("form cooldown ignored")
	}
	var calls int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.voice_requests`).Scan(&calls)
	if calls != 0 {
		t.Fatal("form initiated a call")
	}
}

func TestAutomaticFormPreservesControlAndExistingConversation(t *testing.T) {
	for _, block := range []string{"none", "optout", "paused", "human", "n8n", "shadow"} {
		t.Run(block, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now()
			s := testState(domain.LeadKey("+557799749058"), now)
			s.Profile.Phone = "+557799749058"
			s.Profile.ConversationID = "twilio:" + voiceSender + ":" + s.Profile.Phone
			s.LastInbound = now.Add(-time.Hour)
			switch block {
			case "optout":
				s.OptedOut = true
			case "paused":
				s.Paused = true
			case "human":
				s.Human = true
			case "n8n":
				s.Owner = "n8n"
			}
			publishEffects(t, db, "seed", s)
			f := domain.Form{Name: "Nova Ana", Company: "Atualizada", Phone: "+5577999749058", Consent: true, ConsentTextVersion: "test", Channel: "gateway"}
			if _, _, err := db.RegisterTestForm(ctx, "auto", f); err != nil {
				t.Fatal(err)
			}
			mode := "live"
			if block == "shadow" {
				mode = "shadow"
			}
			for range 2 {
				if _, err := db.ActivateTestForm(ctx, "auto", mode, voiceSender); err != nil {
					t.Fatal(err)
				}
			}
			m := &countingMessenger{}
			w := runtimeWorker(db, now, m)
			drain(t, w)
			if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE payload->>'purpose'='form_contact'`); err != nil {
				t.Fatal(err)
			}
			drain(t, w)
			current, err := db.Lead(ctx, s.ID)
			if err != nil || current.Profile.Company != "Atualizada" || current.Profile.ConversationID != s.Profile.ConversationID || !current.LastInbound.Equal(s.LastInbound) || current.Owner != s.Owner || current.OptedOut != s.OptedOut || current.Paused != s.Paused || current.Human != s.Human {
				t.Fatal(current, err)
			}
			want := int32(0)
			if block == "none" {
				want = 1
			}
			if m.Count.Load() != want {
				t.Fatal("unexpected send", m.Count.Load())
			}
			var n int
			db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.leads`).Scan(&n)
			if n != 1 {
				t.Fatal("split contact", n)
			}
		})
	}
}

func TestTemplateApprovalDefersThenSendsWithoutBody(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now()
	f := domain.Form{Name: "Ana", Phone: "+5565999999900", Consent: true, ConsentTextVersion: "test", Channel: "gateway"}
	db.RegisterTestForm(ctx, "template", f)
	if _, err := db.ActivateTestForm(ctx, "template", "live", voiceSender); err != nil {
		t.Fatal(err)
	}
	approved := false
	posts := 0
	sid := "HX" + strings.Repeat("2", 32)
	messageSID := "SM" + strings.Repeat("3", 32)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ApprovalRequests") {
			status := "pending"
			if approved {
				status = "approved"
			}
			fmt.Fprintf(w, `{"whatsapp":{"status":%q}}`, status)
			return
		}
		if r.Method == "POST" {
			posts++
			r.ParseForm()
			if r.Form.Get("ContentSid") != sid || r.Form.Has("Body") || r.Form.Has("MediaUrl") {
				t.Error("invalid template send", r.Form)
			}
		}
		fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, messageSID)
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: "AC" + strings.Repeat("1", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, FormContentSID: sid}
	w := runtimeWorker(db, now, &countingMessenger{})
	w.Messenger = tw
	step(t, w, true)
	drain(t, w)
	jobID := "fx/form-contact-template:0"
	j, err := db.Job(ctx, jobID)
	if err != nil || j.Status != "pending" || j.Attempts != 0 || posts != 0 {
		t.Fatal(j, posts, err)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.twilio_messages`).Scan(&n)
	if n != 0 {
		t.Fatal("reserved send before approval")
	}
	st, _ := db.Lead(ctx, domain.LeadKey(f.Phone))
	if st.Human {
		t.Fatal("pending approval triggered takeover")
	}
	approved = true
	db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now() WHERE id=$1`, jobID)
	drain(t, w)
	assertJob(t, db, jobID, "execution", "done")
	if posts != 1 {
		t.Fatal(posts)
	}
	if _, err := tw.Send(ctx, jobID, st, domain.ContactOpening(st, policy(), now).Text, "form_contact", now); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatal("duplicate template send")
	}
	if _, err := tw.Send(ctx, "bad", st, "Outra mensagem", "form_contact", now); err == nil {
		t.Fatal("template mismatch")
	}
	if _, err := tw.Send(ctx, "reply", st, "Resposta", "reply", now); err == nil {
		t.Fatal("free-form window bypass")
	}
}

func TestSignedInboundUsesFormContactWithAlternateBrazilianPhone(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	f := domain.Form{Name: "Ana Form", Company: "Empresa Form", Phone: "+5577999749058", Consent: true, ConsentTextVersion: "test", Channel: "gateway"}
	db.RegisterTestForm(ctx, "pair-auto", f)
	if _, err := db.ActivateTestForm(ctx, "pair-auto", "live", voiceSender); err != nil {
		t.Fatal(err)
	}
	drain(t, runtimeWorker(db, time.Now(), &countingMessenger{}))
	phone := "+557799749058"
	account := "AC" + strings.Repeat("1", 32)
	sid := "SM" + strings.Repeat("2", 32)
	at := time.Now().UTC().Truncate(time.Second)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(providers.TwilioMessage{SID: sid, From: "whatsapp:" + phone, To: "whatsapp:" + voiceSender, Body: "Vamos conversar", Status: "received", Created: at.Format(time.RFC1123Z)})
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Account: account, AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL}
	srv := &voiceworker.Server{Repo: voiceworker.Repository{DB: db}, Account: account, AuthToken: "test", Sender: voiceSender, PublicURL: "https://voice.example", Mode: "live", TimeZone: "America/Cuiaba", Messages: tw}
	values := url.Values{"AccountSid": {account}, "MessageSid": {sid}, "From": {"whatsapp:" + phone}, "To": {"whatsapp:" + voiceSender}, "Body": {"Vamos conversar"}}
	r := httptest.NewRequest("POST", "/voice/messages", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Twilio-Signature", voiceworker.Signature("test", srv.PublicURL+"/voice/messages", values))
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, r)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
	var n int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.leads`).Scan(&n)
	if n != 1 {
		t.Fatal("split conversation", n)
	}
	var lead string
	db.Pool.QueryRow(ctx, `SELECT lead_id FROM bia_go.worker_tasks WHERE kind='inbound'`).Scan(&lead)
	if lead != domain.LeadKey(f.Phone) {
		t.Fatal("lost form profile")
	}
}
