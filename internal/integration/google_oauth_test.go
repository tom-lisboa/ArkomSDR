package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkom/bia/internal/googlecalendar"
	"golang.org/x/oauth2"
)

type googleTransport func(*http.Request) (*http.Response, error)

func (f googleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type googleFlow struct {
	state, verifier string
	cookie          *http.Cookie
}

func beginGoogle(t *testing.T, f panelFixture) googleFlow {
	t.Helper()
	out := panelCall(t, f, "POST", "/integrations/google/authorize", nil, "")
	v := decodeMap(t, out)
	u, err := url.Parse(v["authorization_url"].(string))
	if err != nil || u.Host != "sdk.arkom.ai" {
		t.Fatal("invalid local navigation URL", err)
	}
	req := httptest.NewRequest("GET", u.String(), nil)
	start := httptest.NewRecorder()
	f.handler.ServeHTTP(start, req)
	if start.Code != 302 {
		t.Fatal(start.Code, start.Body.String())
	}
	redirect, _ := url.Parse(start.Header().Get("Location"))
	q := redirect.Query()
	if redirect.Host != "accounts.google.com" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("code_verifier") != "" {
		t.Fatal("missing PKCE")
	}
	cookies := start.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatal("unsafe browser binding")
	}
	return googleFlow{state: q.Get("state"), verifier: q.Get("code_challenge"), cookie: cookies[0]}
}

func googleCallback(t *testing.T, f panelFixture, flow googleFlow, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", googlecalendar.DefaultRedirectURL+"?state="+flow.state+query, nil)
	if flow.cookie != nil {
		r.AddCookie(flow.cookie)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func TestGoogleOAuthCallbackRefreshIsolationAndDisconnect(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	f := newPanel(t, db, now, "operator")
	client, err := googlecalendar.New(googlecalendar.Config{Enabled: true, ClientID: "test-client", ClientSecret: "test-secret", TokenKey: strings.Repeat("k", 64), RedirectURL: googlecalendar.DefaultRedirectURL})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Google = client
	var exchanges, refreshes, lists atomic.Int32
	var challenge string
	client.HTTP.Transport = googleTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Host {
		case "oauth2.googleapis.com":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != "test-client" || r.Form.Get("client_secret") != "test-secret" {
				t.Fatal("invalid client credentials")
			}
			if r.Form.Get("grant_type") == "authorization_code" {
				exchanges.Add(1)
				if r.Form.Get("code") != "test-code" || r.Form.Get("redirect_uri") != googlecalendar.DefaultRedirectURL || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != challenge {
					t.Fatal("invalid code exchange or PKCE")
				}
				body = `{"access_token":"access-secret","refresh_token":"refresh-secret","token_type":"Bearer","expires_in":3600,"scope":"` + googlecalendar.CalendarListScope + `"}`
			} else {
				refreshes.Add(1)
				if r.Form.Get("refresh_token") != "refresh-secret" {
					t.Fatal("invalid refresh")
				}
				body = `{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`
			}
		case "www.googleapis.com":
			lists.Add(1)
			if r.URL.Path != "/calendar/v3/users/me/calendarList" || r.URL.Query().Get("pageToken") != "page-two" || r.Header.Get("Authorization") != "Bearer rotated-access" {
				t.Fatal("wrong Calendar request")
			}
			body = `{"items":[{"id":"agenda@example.com","summary":"Equipe","timeZone":"America/Cuiaba","accessRole":"owner","primary":true}],"nextPageToken":"page-three"}`
		default:
			t.Fatal("unexpected provider request", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	flow := beginGoogle(t, f)
	challenge = flow.verifier
	wrong := flow
	wrong.cookie = nil
	if out := googleCallback(t, f, wrong, "&code=test-code"); out.Code != 400 || exchanges.Load() != 0 {
		t.Fatal("callback accepted without its browser")
	}
	if out := googleCallback(t, f, flow, "&code=test-code"); out.Code != 200 || strings.Contains(out.Body.String(), "secret") || len(out.Result().Cookies()) != 1 || out.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(out.Code, out.Body.String())
	}
	if out := googleCallback(t, f, flow, "&code=test-code"); out.Code != 400 || exchanges.Load() != 1 {
		t.Fatal("state replay was accepted", out.Code)
	}
	status := decodeMap(t, panelCall(t, f, "GET", "/integrations/google/status", nil, ""))
	if status["connected"] != true || status["synchronization_enabled"] != false {
		t.Fatal(status)
	}
	var credential []byte
	if err = db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.google_connections WHERE user_id=$1`, f.user.ID).Scan(&credential); err != nil || bytes.Contains(credential, []byte("secret")) {
		t.Fatal("plaintext token in database", err)
	}
	// Expire the encrypted access token to exercise the actual refresh path.
	raw, err := client.Open("token:"+f.user.ID, credential)
	if err != nil {
		t.Fatal(err)
	}
	var token oauth2.Token
	if err = json.Unmarshal(raw, &token); err != nil {
		t.Fatal(err)
	}
	token.Expiry = time.Now().Add(-time.Hour)
	raw, _ = json.Marshal(token)
	credential, _ = client.Seal("token:"+f.user.ID, raw)
	if _, err = db.Pool.Exec(context.Background(), `UPDATE bia_go.google_connections SET credential=$2 WHERE user_id=$1`, f.user.ID, credential); err != nil {
		t.Fatal(err)
	}
	out := panelCall(t, f, "GET", "/integrations/google/calendars?page_token=page-two", nil, "")
	if out.Code != 200 || refreshes.Load() != 1 || lists.Load() != 1 || strings.Contains(out.Body.String(), "secret") || !strings.Contains(out.Body.String(), "page-three") {
		t.Fatal(out.Code, out.Body.String())
	}
	if err = db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.google_connections WHERE user_id=$1`, f.user.ID).Scan(&credential); err != nil {
		t.Fatal(err)
	}
	raw, _ = client.Open("token:"+f.user.ID, credential)
	if !bytes.Contains(raw, []byte("rotated-refresh")) {
		t.Fatal("rotated refresh token was lost")
	}
	other := newPanel(t, db, now, "admin")
	other.s.Google = client
	if status := decodeMap(t, panelCall(t, other, "GET", "/integrations/google/status", nil, "")); status["connected"] != false {
		t.Fatal("another user's connection exposed")
	}
	if out := panelCall(t, other, "GET", "/integrations/google/calendars", nil, ""); out.Code != 409 {
		t.Fatal("another user's calendar was exposed")
	}
	flow = beginGoogle(t, f)
	if out := panelCall(t, f, "POST", "/integrations/google/disconnect", nil, ""); out.Code != 200 {
		t.Fatal(out.Code)
	}
	if out := googleCallback(t, f, flow, "&code=test-code"); out.Code != 400 || exchanges.Load() != 1 {
		t.Fatal("pending callback restored disconnected grant")
	}
	if out := panelCall(t, f, "GET", "/integrations/google/calendars", nil, ""); out.Code != 409 {
		t.Fatal("grant survived disconnect")
	}
	for _, reason := range []string{"denied", "expired", "logout", "superseded", "demoted"} {
		t.Run(reason, func(t *testing.T) {
			flow := beginGoogle(t, f)
			query := "&code=test-code"
			switch reason {
			case "denied":
				query = "&error=access_denied&error_description=PRIVATE"
			case "expired":
				_, err = db.Pool.Exec(context.Background(), `UPDATE bia_go.google_oauth_states SET expires_at=$1`, now.Add(-time.Second))
			case "logout":
				_, err = db.Pool.Exec(context.Background(), `DELETE FROM bia_go.management_sessions WHERE user_id=$1 AND token_hash IN (SELECT session_hash FROM bia_go.google_oauth_states)`, f.user.ID)
			case "superseded":
				_ = beginGoogle(t, f)
			case "demoted":
				_, err = db.Pool.Exec(context.Background(), `UPDATE bia_go.management_users SET role='viewer' WHERE id=$1`, f.user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if out := googleCallback(t, f, flow, query); out.Code != 400 || exchanges.Load() != 1 || strings.Contains(out.Body.String(), "PRIVATE") {
				t.Fatal("unsafe callback", out.Code, out.Body.String())
			}
			if reason == "logout" {
				out := panelCall(t, f, "POST", "/auth/login", map[string]any{"username": f.user.Username, "password": "a-test-password-12345"}, "")
				f.token = decodeMap(t, out)["access_token"].(string)
			}
		})
	}
}

func TestGoogleRoutesRequireRolesAndConfiguration(t *testing.T) {
	db := setup(t)
	f := newPanel(t, db, time.Now(), "viewer")
	if out := panelCall(t, f, "POST", "/integrations/google/authorize", nil, ""); out.Code != 403 {
		t.Fatal("viewer can authorize", out.Code)
	}
	if out := panelCall(t, f, "POST", "/integrations/google/disconnect", nil, ""); out.Code != 403 {
		t.Fatal("viewer can disconnect", out.Code)
	}
	status := decodeMap(t, panelCall(t, f, "GET", "/integrations/google/status", nil, ""))
	if status["configured"] != false || status["connected"] != false {
		t.Fatal(status)
	}
	r := httptest.NewRequest("GET", googlecalendar.DefaultRedirectURL, nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "google_not_configured") {
		t.Fatal("callback route not available in unconfigured environment", w.Code)
	}
}

func TestGoogleRejectsIncompleteGrantsAndConsumesFailedExchanges(t *testing.T) {
	db := setup(t)
	f := newPanel(t, db, time.Now(), "operator")
	client, err := googlecalendar.New(googlecalendar.Config{Enabled: true, ClientID: "client", ClientSecret: "secret", TokenKey: strings.Repeat("k", 64), RedirectURL: googlecalendar.DefaultRedirectURL})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Google = client
	for _, test := range []struct {
		name, body     string
		provider, want int
	}{
		{"provider_rejected", `{"error":"invalid_grant","error_description":"PRIVATE"}`, 400, 502},
		{"no_refresh", `{"access_token":"a","token_type":"Bearer","expires_in":3600}`, 200, 422},
		{"no_calendar_scope", `{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":3600,"scope":"email"}`, 200, 422},
		{"expired_token", `{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":-10}`, 200, 422},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client.HTTP.Transport = googleTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: test.provider, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: r}, nil
			})
			flow := beginGoogle(t, f)
			out := googleCallback(t, f, flow, "&code=code")
			if out.Code != test.want || strings.Contains(out.Body.String(), "PRIVATE") || calls.Load() != 1 {
				t.Fatal(out.Code, out.Body.String(), calls.Load())
			}
			if out := googleCallback(t, f, flow, "&code=code"); out.Code != 400 || calls.Load() != 1 {
				t.Fatal("failed exchange replayed")
			}
			var grants int
			if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.google_connections`).Scan(&grants); err != nil || grants != 0 {
				t.Fatal("incomplete grant saved", err)
			}
		})
	}
}

func TestGoogleConcurrentCallbackOnlyExchangesOnce(t *testing.T) {
	db := setup(t)
	f := newPanel(t, db, time.Now(), "operator")
	client, err := googlecalendar.New(googlecalendar.Config{Enabled: true, ClientID: "client", ClientSecret: "secret", TokenKey: strings.Repeat("k", 64), RedirectURL: googlecalendar.DefaultRedirectURL})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Google = client
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	client.HTTP.Transport = googleTransport(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		body := `{"access_token":"a","refresh_token":"r","token_type":"Bearer","expires_in":3600,"scope":"` + googlecalendar.CalendarListScope + `"}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	flow := beginGoogle(t, f)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- googleCallback(t, f, flow, "&code=code") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("token exchange did not start")
	}
	// The first transaction has deleted the state but not committed. A second
	// claim waits for it, so run it concurrently and release the provider call.
	replay := make(chan *httptest.ResponseRecorder, 1)
	go func() { replay <- googleCallback(t, f, flow, "&code=code") }()
	close(release)
	if first, second := <-done, <-replay; first.Code != 200 || second.Code != 400 || calls.Load() != 1 {
		t.Fatal("concurrent replay accepted", first.Code, second.Code, calls.Load())
	}
}
