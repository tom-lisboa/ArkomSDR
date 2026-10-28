package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPublicFormCORSAndWhatsAppBinding(t *testing.T) {
	db := setup(t)
	key := strings.Repeat("t", 48)
	h := (&httpapi.Server{DB: db, TestFormToken: key, VoiceSender: voiceSender, Policy: policy(), Mode: "live"}).Handler()
	path := "/webhooks/test-form/" + key
	call := func(method, path, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", "http://localhost:5173")
		r.Header.Set("Idempotency-Key", id)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	pre := call("OPTIONS", path, "", "")
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal(pre.Code, pre.Header())
	}
	if w := call("POST", "/webhooks/test-form/wrong", "submit-one", `{}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
	body := `{"name":"Ana","company":"Empresa Ana","phone":"+5577999749058","revenue":0,"goal":"Atender mais rápido","consent":true}`
	first := call("POST", path, "submit-one", body)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body)
	}
	var response struct {
		Link      string `json:"whatsapp_url"`
		Duplicate bool   `json:"duplicate"`
	}
	json.Unmarshal(first.Body.Bytes(), &response)
	link, e := url.Parse(response.Link)
	if e != nil || link.Host != "wa.me" {
		t.Fatal(response.Link, e)
	}
	text := link.Query().Get("text")
	duplicate := call("POST", path, "submit-one", body)
	var again struct {
		Link      string `json:"whatsapp_url"`
		Duplicate bool   `json:"duplicate"`
	}
	json.Unmarshal(duplicate.Body.Bytes(), &again)
	if duplicate.Code != 202 || !again.Duplicate || again.Link != response.Link {
		t.Fatal(duplicate.Code, again)
	}
	conflict := call("POST", path, "submit-one", strings.Replace(body, "Ana", "Bia", 1))
	if conflict.Code != 409 {
		t.Fatal(conflict.Code)
	}
	var count int
	db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.leads`).Scan(&count)
	if count != 0 {
		t.Fatal("public form created contact or sent message")
	}
	// Possession of the code is insufficient from a different WhatsApp number.
	wrong := domain.Form{Name: "Outro", Phone: "+5565999999900", Consent: true, Channel: "gateway", ConversationID: "wrong", TimeZone: "America/Cuiaba"}
	ev := domain.Event{ID: "wrong-code-message", LeadID: domain.LeadKey(wrong.Phone), Kind: "message.received", At: time.Now(), Text: text, ConversationID: wrong.ConversationID}
	if _, e = db.SubmitWhatsAppInbound(context.Background(), ev, "live", wrong); e != nil {
		t.Fatal(e)
	}
	db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE id='test-form-submit-one'`).Scan(&count)
	if count != 0 {
		t.Fatal("claimed someone else's profile")
	}
	// Provider canonical number without ninth digit receives only its matching form.
	actual := domain.Form{Name: "Perfil WhatsApp", Phone: "+557799749058", Consent: true, Channel: "gateway", ConversationID: "real", TimeZone: "America/Cuiaba"}
	ev = domain.Event{ID: "valid-code-message", LeadID: domain.LeadKey(actual.Phone), Kind: "message.received", At: time.Now(), Text: text, ConversationID: actual.ConversationID}
	if _, e = db.SubmitWhatsAppInbound(context.Background(), ev, "live", actual); e != nil {
		t.Fatal(e)
	}
	if inserted, e := db.SubmitWhatsAppInbound(context.Background(), ev, "live", actual); e != nil || inserted {
		t.Fatal("duplicate inbound", inserted, e)
	}
	j, e := db.Job(context.Background(), "test-form-submit-one")
	if e != nil {
		t.Fatal(e)
	}
	var formEvent domain.Event
	if e = json.Unmarshal(j.Payload, &formEvent); e != nil {
		t.Fatal(e)
	}
	if formEvent.Form.Company != "Empresa Ana" || formEvent.Form.Name != "Ana" || formEvent.Form.Revenue == nil || *formEvent.Form.Revenue != 0 || formEvent.Form.Phone != actual.Phone {
		t.Fatal(formEvent.Form)
	}
	step(t, baseWorker(db), true)
	state, e := db.Lead(context.Background(), ev.LeadID)
	if e != nil || state.Profile.Company != "Empresa Ana" || state.Owner != "go" || state.Human {
		t.Fatal(state, e)
	}
	db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE kind='effect' AND payload->>'kind'='send'`).Scan(&count)
	if count != 0 {
		t.Fatal("unexpected extra welcome")
	}
	// Neither administrative fields nor arbitrary booking data are accepted.
	for _, bad := range []string{strings.Replace(body, `"consent":true`, `"consent":false`, 1), strings.Replace(body, `"revenue":0`, `"revenue":-1`, 1), strings.TrimSuffix(body, "}") + `,"owner":"go"}`} {
		w := call("POST", path, "bad-body", bad)
		if w.Code < 400 {
			t.Fatal(w.Code)
		}
	}
	// Ordinary admin endpoints remain protected independently of the scoped key.
	r := httptest.NewRequest("POST", "/v1/forms", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal(w.Code)
	}
}
func TestPublicFormRateLimitAndExpiredCode(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	f := domain.Form{Name: "Teste", Phone: "+5565999999900", Consent: true, Channel: "gateway"}
	var code string
	for i := 0; i < 10; i++ {
		var e error
		code, _, e = db.RegisterTestForm(ctx, string(rune('a'+i)), f)
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := db.RegisterTestForm(ctx, "overflow", f); e == nil {
		t.Fatal("rate limit bypassed")
	}
	if _, _, e := db.RegisterTestForm(ctx, "a", f); e != nil {
		t.Fatal("idempotent retry counted", e)
	}
	if _, e := db.Pool.Exec(ctx, `UPDATE bia_go.test_forms SET expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	ev := domain.Event{ID: "expired-form", LeadID: domain.LeadKey(f.Phone), Kind: "message.received", Text: "ARKOM-" + code, At: time.Now()}
	if _, e := db.SubmitWhatsAppInbound(ctx, ev, "live", f); e != nil {
		t.Fatal(e)
	}
	var count int
	db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.test_forms WHERE claimed_at IS NOT NULL`).Scan(&count)
	if count != 0 {
		t.Fatal("expired form claimed")
	}
}
