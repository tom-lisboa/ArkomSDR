package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/googlecalendar"
	"golang.org/x/oauth2"
)

func multiGoogleClient(t *testing.T) *googlecalendar.Client {
	t.Helper()
	c, err := googlecalendar.New(googlecalendar.Config{Enabled: true, ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: googlecalendar.DefaultRedirectURL, TokenKey: strings.Repeat("k", 64)})
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP.Transport = googleTransport(func(r *http.Request) (*http.Response, error) {
		var body any
		switch r.URL.Host {
		case "oauth2.googleapis.com":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			name := r.Form.Get("code")
			body = map[string]any{"access_token": "access-" + name, "refresh_token": "refresh-" + name, "token_type": "Bearer", "expires_in": 3600, "scope": googlecalendar.CalendarListScope + " openid email"}
		case "openidconnect.googleapis.com":
			name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer access-")
			body = map[string]any{"sub": "subject-" + name, "email": name + "@example.com", "email_verified": name != "unverified"}
		case "www.googleapis.com":
			name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer access-")
			body = map[string]any{"items": []map[string]any{{"id": "primary-" + name, "summary": "Principal " + name, "primary": true}, {"id": "shared-" + name, "summary": "Equipe " + name}}}
		default:
			t.Fatal("unexpected provider request", r.URL.Host)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
	})
	return c
}

func TestGoogleMultipleAccountsAreIndependentAndOwnedBySeller(t *testing.T) {
	db := setup(t)
	f := newPanel(t, db, time.Now(), "operator")
	f.s.Google = multiGoogleClient(t)
	connect := func(name, target string) string {
		flow := beginGoogleAccount(t, f, target)
		out := googleCallback(t, f, flow, "&code="+name)
		return decodeMap(t, out)["account_id"].(string)
	}
	first := connect("one", "")
	second := connect("two", "")
	if first == second {
		t.Fatal("second account replaced the first")
	}
	status := decodeMap(t, panelCall(t, f, "GET", "/integrations/google/status", nil, ""))
	if status["account_count"] != float64(2) || len(status["accounts"].([]any)) != 2 {
		t.Fatal(status)
	}
	for _, path := range []string{"/integrations/google/calendars", "/integrations/google/disconnect"} {
		method := "GET"
		if strings.HasSuffix(path, "disconnect") {
			method = "POST"
		}
		out := panelCall(t, f, method, path, nil, "")
		if out.Code != 422 || !strings.Contains(out.Body.String(), "google_account_id_required") {
			t.Fatal("ambiguous legacy call accepted", out.Code, out.Body.String())
		}
	}
	var secondCredential []byte
	if err := db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.google_accounts WHERE id=$1`, second).Scan(&secondCredential); err != nil {
		t.Fatal(err)
	}
	if again := connect("one", ""); again != first {
		t.Fatal("same Google subject created duplicate account")
	}
	var unchanged []byte
	if err := db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.google_accounts WHERE id=$1`, second).Scan(&unchanged); err != nil || !bytes.Equal(unchanged, secondCredential) {
		t.Fatal("refreshing first account overwrote second", err)
	}
	for id, name := range map[string]string{first: "one", second: "two"} {
		out := decodeMap(t, panelCall(t, f, "GET", "/integrations/google/calendars?account_id="+id, nil, ""))
		if out["account_id"] != id || len(out["items"].([]any)) != 2 || out["items"].([]any)[0].(map[string]any)["id"] != "primary-"+name {
			t.Fatal("account calendars mixed", out)
		}
	}
	other := newPanel(t, db, time.Now(), "admin")
	other.s.Google = f.s.Google
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/integrations/google/calendars?account_id=" + first, nil},
		{"POST", "/integrations/google/authorize", map[string]any{"account_id": first}},
		{"POST", "/integrations/google/disconnect", map[string]any{"account_id": first}},
	} {
		if out := panelCall(t, other, op.method, op.path, op.body, ""); out.Code != 404 {
			t.Fatal("another user's account accessible", out.Code)
		}
	}
	flow := beginGoogleAccount(t, f, first)
	if out := googleCallback(t, f, flow, "&code=two"); out.Code != 409 || !strings.Contains(out.Body.String(), "google_account_mismatch") {
		t.Fatal("reconnect silently switched Google identity", out.Code, out.Body.String())
	}
	flow = beginGoogle(t, f)
	if out := googleCallback(t, f, flow, "&code=unverified"); out.Code != 502 {
		t.Fatal("unverified identity saved", out.Code)
	}
	var firstCredential []byte
	if err := db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.google_accounts WHERE id=$1`, first).Scan(&firstCredential); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.google_accounts SET credential=$2 WHERE id=$1`, second, firstCredential); err != nil {
		t.Fatal(err)
	}
	if out := panelCall(t, f, "GET", "/integrations/google/calendars?account_id="+second, nil, ""); out.Code != 503 {
		t.Fatal("credential transplant between accounts accepted", out.Code)
	}
	if _, err := db.Pool.Exec(context.Background(), `UPDATE bia_go.google_accounts SET credential=$2 WHERE id=$1`, second, secondCredential); err != nil {
		t.Fatal(err)
	}
	removed := decodeMap(t, panelCall(t, f, "POST", "/integrations/google/disconnect", map[string]any{"account_id": first}, ""))
	if removed["connected"] != true || removed["account_count"] != float64(1) {
		t.Fatal("disconnect removed other accounts", removed)
	}
	if out := panelCall(t, f, "GET", "/integrations/google/calendars?account_id="+second, nil, ""); out.Code != 200 {
		t.Fatal("remaining account unusable", out.Code)
	}
	if out := panelCall(t, f, "GET", "/integrations/google/calendars?account_id="+first, nil, ""); out.Code != 404 {
		t.Fatal("disconnected account still available", out.Code)
	}
}

func TestGoogleLegacyMigrationDoesNotRestoreDisconnectedGrants(t *testing.T) {
	db := setup(t)
	f := newPanel(t, db, time.Now(), "operator")
	client := multiGoogleClient(t)
	f.s.Google = client
	token := oauth2.Token{AccessToken: "access-legacy", RefreshToken: "refresh-legacy", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	raw, _ := json.Marshal(token)
	sealed, err := client.Seal("token:"+f.user.ID, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(context.Background(), `INSERT INTO bia_go.google_connections(user_id,client_id,credential,scopes,expires_at,connected_at,updated_at) VALUES($1,$2,$3,$4,$5,now(),now())`, f.user.ID, client.Config.ClientID, sealed, []string{googlecalendar.CalendarListScope}, token.Expiry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(context.Background(), `DELETE FROM bia_go.schema_versions WHERE version=21`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := decodeMap(t, panelCall(t, f, "GET", "/integrations/google/status", nil, ""))
	accounts := status["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["requires_reconnect"] != true {
		t.Fatal("legacy grant was not preserved", status)
	}
	id := accounts[0].(map[string]any)["id"].(string)
	if out := panelCall(t, f, "GET", "/integrations/google/calendars?account_id="+id, nil, ""); out.Code != 200 {
		t.Fatal("legacy encryption could not be read", out.Code, out.Body.String())
	}
	var version int
	if err = db.Pool.QueryRow(context.Background(), `SELECT credential_version FROM bia_go.google_accounts WHERE id=$1`, id).Scan(&version); err != nil || version != 2 {
		t.Fatal("legacy grant was not rewrapped for its account", err)
	}
	if out := panelCall(t, f, "POST", "/integrations/google/disconnect", map[string]any{"account_id": id}, ""); out.Code != 200 {
		t.Fatal(out.Code)
	}
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = decodeMap(t, panelCall(t, f, "GET", "/integrations/google/status", nil, ""))
	if status["account_count"] != float64(0) {
		t.Fatal("disconnected account resurrected on migration", status)
	}
	var legacyCount int
	if err = db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.google_connections WHERE user_id=$1`, f.user.ID).Scan(&legacyCount); err != nil || legacyCount != 0 {
		t.Fatal("legacy credential survived disconnect", err)
	}
}
