package management

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"arkom/bia/internal/googlecalendar"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const googleCookie = "__Host-bia-google-oauth"

func (s *Server) googleReady(w http.ResponseWriter) bool {
	if s.Google == nil {
		fail(w, 503, "google_not_configured")
		return false
	}
	return true
}

func googleLock(ctx context.Context, tx pgx.Tx, user string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "google-oauth:"+user)
	return err
}

func googleOpaque(v string) bool {
	_, err := hex.DecodeString(v)
	return len(v) == 64 && err == nil
}

func (s *Server) googleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	user := actor(r).ID
	state, verifier := randomToken(), oauth2.GenerateVerifier()
	sealed, err := s.Google.Seal("state:"+digest(state), []byte(verifier))
	if readError(w, err) {
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	if readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	// A newer attempt replaces older pending flows for this user. Logout and
	// session revocation also invalidate pending flows through the foreign key.
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_oauth_states WHERE user_id=$1 OR expires_at<=$2`, user, s.now()); readError(w, err) {
		return
	}
	expires := s.now().Add(10 * time.Minute)
	_, err = tx.Exec(r.Context(), `INSERT INTO bia_go.google_oauth_states(state_hash,user_id,session_hash,client_id,verifier,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, digest(state), user, digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), s.Google.Config.ClientID, sealed, s.now(), expires)
	if readError(w, err) || readError(w, tx.Commit(r.Context())) {
		return
	}
	u, _ := url.Parse(s.Google.Config.RedirectURL)
	u.Path = "/v1/management/integrations/google/start"
	u.RawQuery = url.Values{"ticket": {state}}.Encode()
	write(w, 200, map[string]any{"authorization_url": u.String(), "expires_at": expires})
}

// The frontend navigates to this one-time URL rather than passing its Bearer
// session to Google or requiring third-party cookies on a cross-origin fetch.
func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	q := r.URL.Query()
	state := q.Get("ticket")
	if !googleOpaque(state) || len(q["ticket"]) != 1 {
		fail(w, 400, "google_invalid_state")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	var user string
	err = tx.QueryRow(r.Context(), `SELECT user_id FROM bia_go.google_oauth_states WHERE state_hash=$1`, digest(state)).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "google_invalid_state")
		return
	}
	if readError(w, err) || readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	browser := randomToken()
	var sealed []byte
	err = tx.QueryRow(r.Context(), `UPDATE bia_go.google_oauth_states o SET browser_hash=$2,started_at=$3 FROM bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.started_at IS NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.verifier`, digest(state), digest(browser), s.now(), s.Google.Config.ClientID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "google_invalid_state")
		return
	}
	if readError(w, err) {
		return
	}
	verifier, err := s.Google.Open("state:"+digest(state), sealed)
	if readError(w, err) || readError(w, tx.Commit(r.Context())) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: browser, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, s.Google.AuthorizationURL(state, string(verifier)), http.StatusFound)
}

func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !s.googleReady(w) {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	cookie, err := r.Cookie(googleCookie)
	if !googleOpaque(state) || len(q["state"]) != 1 || err != nil || !googleOpaque(cookie.Value) {
		fail(w, 400, "google_invalid_state")
		return
	}
	// Claim in its own committed statement. A timeout or rollback during token
	// exchange cannot make this browser/state pair usable a second time.
	var user string
	err = s.DB.Pool.QueryRow(r.Context(), `UPDATE bia_go.google_oauth_states o SET consumed_at=$3 FROM bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.browser_hash=$2 AND o.consumed_at IS NULL AND o.started_at IS NOT NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.user_id`, digest(state), digest(cookie.Value), s.now(), s.Google.Config.ClientID).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "google_invalid_state")
		return
	}
	if readError(w, err) {
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	if readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	var sealed []byte
	err = tx.QueryRow(r.Context(), `DELETE FROM bia_go.google_oauth_states o USING bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.browser_hash=$2 AND o.consumed_at IS NOT NULL AND o.started_at IS NOT NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.verifier`, digest(state), digest(cookie.Value), s.now(), s.Google.Config.ClientID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 400, "google_invalid_state")
		return
	}
	if readError(w, err) {
		return
	}
	// Commit consumption even when consent is denied or the exchange fails. A
	// code is never retried automatically after an ambiguous provider response.
	finishFailure := func(status int, code string) {
		if readError(w, tx.Commit(r.Context())) {
			return
		}
		http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		fail(w, status, code)
	}
	if q.Get("error") != "" {
		finishFailure(400, "google_authorization_denied")
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 8192 || len(q["code"]) != 1 {
		finishFailure(400, "google_authorization_code_required")
		return
	}
	verifier, err := s.Google.Open("state:"+digest(state), sealed)
	if err != nil {
		finishFailure(503, "google_credential_unavailable")
		return
	}
	token, err := s.Google.OAuth.Exchange(s.Google.Context(r.Context()), code, oauth2.VerifierOption(string(verifier)))
	if err != nil {
		finishFailure(502, "google_token_exchange_failed")
		return
	}
	if !token.Valid() || token.Expiry.IsZero() || token.RefreshToken == "" || !strings.EqualFold(token.Type(), "Bearer") {
		finishFailure(422, "google_offline_access_required")
		return
	}
	scopes := strings.Fields(stringExtra(token, "scope"))
	found := false
	for _, scope := range scopes {
		found = found || scope == googlecalendar.CalendarListScope
	}
	if !found {
		finishFailure(422, "google_calendar_scope_required")
		return
	}
	raw, err := json.Marshal(token)
	if err != nil {
		finishFailure(503, "google_credential_unavailable")
		return
	}
	credential, err := s.Google.Seal("token:"+user, raw)
	if err != nil {
		finishFailure(503, "google_credential_unavailable")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO bia_go.google_connections(user_id,client_id,credential,scopes,expires_at,connected_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$6) ON CONFLICT(user_id) DO UPDATE SET client_id=EXCLUDED.client_id,credential=EXCLUDED.credential,scopes=EXCLUDED.scopes,expires_at=EXCLUDED.expires_at,connected_at=EXCLUDED.connected_at,updated_at=EXCLUDED.updated_at`, user, s.Google.Config.ClientID, credential, scopes, token.Expiry, s.now())
	if readError(w, err) || readError(w, googleAudit(r.Context(), tx, user, "google.connected")) || readError(w, tx.Commit(r.Context())) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		_, _ = w.Write([]byte(`<!doctype html><html lang="pt-BR"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Google conectado | Arkom</title><h1>Google conectado</h1><p>A conexão foi salva com segurança. Você pode fechar esta janela e voltar ao painel.</p><p>A sincronização de reuniões será habilitada separadamente.</p></html>`))
		return
	}
	write(w, 200, map[string]any{"connected": true, "synchronization_enabled": false})
}

func stringExtra(token *oauth2.Token, key string) string {
	v, _ := token.Extra(key).(string)
	return v
}

func googleAudit(ctx context.Context, tx pgx.Tx, user, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO bia_go.management_actions(id,actor_id,kind,fingerprint,request,result) VALUES($1,$2,$3,$4,'{}','{}')`, "google_"+randomToken()[:32], user, kind, digest(kind))
	return err
}

func (s *Server) googleStatus(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"configured": s.Google != nil, "connected": false, "redirect_uri": googlecalendar.DefaultRedirectURL, "synchronization_enabled": false}
	if s.Google == nil {
		write(w, 200, result)
		return
	}
	result["redirect_uri"] = s.Google.Config.RedirectURL
	var connected, expires time.Time
	var scopes []string
	err := s.DB.Pool.QueryRow(r.Context(), `SELECT connected_at,expires_at,scopes FROM bia_go.google_connections WHERE user_id=$1 AND client_id=$2`, actor(r).ID, s.Google.Config.ClientID).Scan(&connected, &expires, &scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, 200, result)
		return
	}
	if readError(w, err) {
		return
	}
	result["connected"], result["connected_at"], result["token_expires_at"], result["scopes"] = true, connected, expires, scopes
	write(w, 200, result)
}

func (s *Server) googleCalendars(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	page := r.URL.Query().Get("page_token")
	if len(page) > 4096 {
		fail(w, 422, "invalid_page_token")
		return
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	user := actor(r).ID
	if readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	var credential []byte
	err = tx.QueryRow(r.Context(), `SELECT credential FROM bia_go.google_connections WHERE user_id=$1 AND client_id=$2`, user, s.Google.Config.ClientID).Scan(&credential)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 409, "google_not_connected")
		return
	}
	if readError(w, err) {
		return
	}
	raw, err := s.Google.Open("token:"+user, credential)
	var saved oauth2.Token
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		fail(w, 503, "google_credential_unavailable")
		return
	}
	token, err := s.Google.OAuth.TokenSource(s.Google.Context(r.Context()), &saved).Token()
	if err != nil {
		fail(w, 502, "google_refresh_failed")
		return
	}
	if !strings.EqualFold(token.Type(), "Bearer") || !token.Valid() {
		fail(w, 502, "google_refresh_failed")
		return
	}
	// Persist a rotated refresh token before requesting Calendar data. A failed
	// Calendar request must not discard a successful refresh and its new token.
	if token.RefreshToken == "" {
		token.RefreshToken = saved.RefreshToken
	}
	raw, err = json.Marshal(token)
	if readError(w, err) {
		return
	}
	credential, err = s.Google.Seal("token:"+user, raw)
	if readError(w, err) {
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE bia_go.google_connections SET credential=$2,expires_at=$3,updated_at=$4 WHERE user_id=$1`, user, credential, token.Expiry, s.now())
	if readError(w, err) || readError(w, tx.Commit(r.Context())) {
		return
	}
	items, next, err := s.Google.Calendars(r.Context(), token, page)
	if err != nil {
		fail(w, 502, "google_calendar_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_page_token": next})
}

// Disconnect removes the local grant and pending flows atomically. It does not
// revoke Google's entire app grant, which may be shared by another staff user.
func (s *Server) googleDisconnect(w http.ResponseWriter, r *http.Request) {
	user := actor(r).ID
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	if readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_oauth_states WHERE user_id=$1`, user); readError(w, err) {
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_connections WHERE user_id=$1`, user); readError(w, err) {
		return
	}
	if readError(w, googleAudit(r.Context(), tx, user, "google.disconnected")) || readError(w, tx.Commit(r.Context())) {
		return
	}
	write(w, 200, map[string]bool{"connected": false})
}
