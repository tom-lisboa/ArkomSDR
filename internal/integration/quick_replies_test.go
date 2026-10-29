package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/quickreply"
	"arkom/bia/internal/voiceworker"
)

func TestQuickReplyTemplatesInAndOutsideSession(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	signer := quickreply.Signer{Key: strings.Repeat("test", 8)}
	cfg := providers.QuickReplyConfig{Enabled: true, Session1: "session1", Session2: "session2", Session3: "session3", Form: "form", Context: "context", ContextQuestion: "context-question", Preparation: "preparation", MeetingStart: "meeting-start"}
	var gotSID string
	var vars map[string]string
	posts := 0
	approved := true
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
			_ = r.ParseForm()
			gotSID = r.Form.Get("ContentSid")
			if r.Form.Has("Body") {
				t.Error("buttons degraded to plain body")
			}
			vars = nil
			_ = json.Unmarshal([]byte(r.Form.Get("ContentVariables")), &vars)
		}
		sid := fmt.Sprintf("SM%032x", posts)
		if r.Method == "GET" {
			sid = strings.TrimSuffix(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], ".json")
		}
		fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, sid)
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Pool: db.Pool, Buttons: signer, QuickReplies: cfg, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, Account: "AC" + strings.Repeat("a", 32), AuthToken: "test", Sender: voiceSender, TimeZone: policy().TimeZone}
	for _, session := range []bool{false, true} {
		for _, purpose := range []string{"form_contact", "form_context", "meeting_preparation", "meeting_start"} {
			s := testState("buttons", now)
			s.Profile.Goal = "aumentar vendas"
			if session {
				s.LastInbound = now.Add(-time.Minute)
			}
			var opening domain.Opening
			switch purpose {
			case "form_contact":
				opening = domain.ContactOpening(s, policy(), now)
			default:
				s.Booking = &domain.Booking{ID: "booking", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
				switch purpose {
				case "form_context":
					opening = domain.ContactContextOpening(s, policy(), now)
				case "meeting_preparation":
					opening = domain.PreparationOpening(s)
				case "meeting_start":
					opening = domain.MeetingStartOpening(s, policy())
				}
			}
			key := fmt.Sprint("buttons/", purpose, "/", session)
			for range 2 {
				if _, err := tw.Send(ctx, key, s, opening.Text, purpose, now); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]string{"form_contact": "form", "form_context": "context", "meeting_preparation": "preparation", "meeting_start": "meeting-start"}[purpose]
			if session {
				want = "session3"
				if purpose == "form_contact" {
					want = "session2"
				}
			}
			if gotSID != want {
				t.Fatalf("%s session=%v SID=%s", purpose, session, gotSID)
			}
			tokens := 0
			for _, v := range vars {
				if strings.HasPrefix(v, "bia1.") {
					tokens++
				}
			}
			wantTokens := 3
			if purpose == "form_contact" {
				wantTokens = 2
			}
			for _, value := range vars {
				if strings.ContainsAny(value, "\n\r\t") || strings.Contains(value, "     ") {
					t.Fatal("invalid Twilio content variable", vars)
				}
			}
			if tokens != wantTokens {
				t.Fatal("missing action payload", vars)
			}
			if session && vars["1"] != strings.Join(strings.Fields(opening.Text), " ") {
				t.Fatal("changed conversational body")
			}
		}
	}
	if posts != 8 {
		t.Fatal("duplicated provider POST", posts)
	}
	// Unapproved business-initiated buttons wait without sending or handing off.
	approved = false
	s := testState("pending", now)
	if _, err := tw.Send(ctx, "pending-buttons", s, domain.ContactOpening(s, policy(), now).Text, "form_contact", now); err == nil {
		t.Fatal("pending template sent")
	}
	if posts != 8 {
		t.Fatal("pending approval posted")
	}
	// A no-goal booked opening uses its own body and dynamic action IDs.
	approved = true
	s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
	if _, err := tw.Send(ctx, "context-no-goal", s, domain.ContactContextOpening(s, policy(), now).Text, "form_context", now); err != nil || gotSID != "context-question" {
		t.Fatal(err, gotSID)
	}
}

func TestSignedButtonContinuesSameContextDeduplicatesAndNeverAuthorizesVoice(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	s := testState("same-button-contact", now.Add(-time.Hour))
	s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
	publishEffects(t, db, "seed-buttons", s)
	signer := quickreply.Signer{Key: strings.Repeat("test", 8)}
	payload := signer.Button(s, "confirm", "Confirmar presença", now).Payload
	sid := "SM" + strings.Repeat("a", 32)
	account := "AC" + strings.Repeat("b", 32)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(providers.TwilioMessage{SID: sid, From: "whatsapp:" + s.Profile.Phone, To: "whatsapp:" + voiceSender, Body: "Confirmar presença", Status: "received", Created: now.Format(time.RFC1123Z)})
	}))
	defer mock.Close()
	tw := &providers.TwilioMessaging{Client: mock.Client(), BaseURL: mock.URL, Account: account, AuthToken: "test"}
	server := &voiceworker.Server{Repo: voiceworker.Repository{DB: db}, Account: account, AuthToken: "test", Sender: voiceSender, PublicURL: "https://voice.example", Messages: tw, Mode: "shadow", TimeZone: policy().TimeZone}
	f := url.Values{"AccountSid": {account}, "MessageSid": {sid}, "From": {"whatsapp:" + s.Profile.Phone}, "To": {"whatsapp:" + voiceSender}, "Body": {"Confirmar presença"}, "ButtonText": {"Confirmar presença"}, "ButtonPayload": {payload}}
	for range 2 {
		req := httptest.NewRequest("POST", "/voice/messages", strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", voiceworker.Signature("test", server.PublicURL+"/voice/messages", f))
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, req)
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	p := fastWindowPolicy()
	ingest(t, db, p, now)
	e := frozen(t, db, p, now.Add(2*time.Second))
	if e.ButtonPayload != payload || e.LeadID != s.ID || len(e.Context.MessageIDs) != 1 {
		t.Fatal("lost binding/contact", e)
	}
	runner := &fakeRunner{}
	w := baseWorker(db)
	w.Buttons = signer
	w.Planner = runner
	w.Now = func() time.Time { return now.Add(2 * time.Second) }
	drain(t, w)
	current, err := db.Lead(ctx, s.ID)
	if err != nil || current.Booking.ConfirmedAt == nil || runner.calls != 0 {
		t.Fatal(current, err, runner.calls)
	}
	var contacts, calls int
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM bia_go.leads),(SELECT count(*) FROM bia_go.voice_requests)`).Scan(&contacts, &calls); err != nil || contacts != 1 || calls != 0 {
		t.Fatal(contacts, calls, err)
	}
}
func TestButtonAndTextBatchCannotSilentlyConfirm(t *testing.T) {
	for _, buttonLast := range []bool{false, true} {
		t.Run(fmt.Sprint(buttonLast), func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("mixed-button", now.Add(-time.Hour))
			s.Booking = &domain.Booking{ID: "b", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
			publishEffects(t, db, "seed-mixed", s)
			signer := quickreply.Signer{Key: strings.Repeat("test", 8)}
			click := inbound("click", s.ID, "Confirmar presença", now)
			click.ButtonPayload = signer.Button(s, "confirm", "Confirmar presença", now).Payload
			text := inbound("text", s.ID, "Espera, não vou conseguir nesse horário", now.Add(time.Millisecond))
			if buttonLast {
				click.At, text.At = text.At, click.At
			}
			submit(t, db, click, "shadow")
			submit(t, db, text, "shadow")
			p := fastWindowPolicy()
			ingest(t, db, p, now)
			e := frozen(t, db, p, now.Add(2*time.Second))
			if e.ButtonPayload != "mixed" {
				t.Fatal("lost earlier button", e)
			}
			w := baseWorker(db)
			w.Buttons = signer
			runner := &fakeRunner{}
			w.Planner = runner
			w.Now = func() time.Time { return now.Add(2 * time.Second) }
			drain(t, w)
			current, err := db.Lead(ctx, s.ID)
			if err != nil || current.Booking.ConfirmedAt != nil || current.Booking.ID != "b" || runner.calls != 0 {
				t.Fatal(current, err)
			}
		})
	}
}
