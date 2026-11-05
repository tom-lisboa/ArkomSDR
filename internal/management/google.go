package management

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"arkom/bia/internal/googlecalendar"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const googleCookie = "__Host-bia-google-oauth"

func googleResultPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="pt-BR"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>` + html.EscapeString(title) + ` · Arkom</title><link rel="stylesheet" href="/conectar-agenda/connect.css"><main><section class="card"><h1>` + html.EscapeString(title) + `</h1><p class="muted">` + html.EscapeString(message) + `</p><p class="help">Você pode fechar esta aba e voltar à página de conexões.</p><a class="continue-link" href="/conectar-agenda/">Abrir página de conexões</a></section></main></html>`))
}

func googleFlowFailure(w http.ResponseWriter, r *http.Request, status int, code string) {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		fail(w, status, code)
		return
	}
	message := "Não foi possível concluir a conexão agora. Volte à página de conexões e tente novamente."
	switch code {
	case "google_invalid_state":
		message = "Esta autorização expirou ou já foi utilizada. Inicie outra autorização na página de conexões."
	case "google_authorization_denied":
		message = "A autorização não foi concluída. Você pode voltar e tentar novamente quando desejar."
	case "google_account_mismatch":
		message = "Você escolheu outra conta Google. Para reconectar, escolha o mesmo e-mail indicado no cartão da conta."
	case "google_account_already_connected":
		message = "Esta conta Google já está conectada em outro cartão. Volte à página de conexões para usar a conta identificada."
	case "google_identity_unavailable":
		message = "Não foi possível confirmar a identidade da conta Google. Reinicie a autorização e permita a identificação do seu e-mail."
	case "google_calendar_scope_required":
		message = "Para concluir a conexão, autorize a consulta da lista de agendas da conta Google."
	case "google_offline_access_required":
		message = "O Google não liberou o acesso contínuo. Reinicie a autorização e confira as permissões solicitadas."
	}
	googleResultPage(w, status, "Conexão não concluída", message)
}

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

func googleAccountRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		AccountID string `json:"account_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return "", false
	}
	if len(req.AccountID) > 80 {
		fail(w, 422, "invalid_account_id")
		return "", false
	}
	return req.AccountID, true
}

func googleCredentialSubject(user, account string, version int) string {
	if version == 1 {
		return "token:" + user
	}
	return "account:" + user + ":" + account
}

func (s *Server) googleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	user := actor(r).ID
	account, valid := googleAccountRequest(w, r)
	if !valid {
		return
	}
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
	if account != "" {
		var exists bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bia_go.google_accounts WHERE id=$1 AND user_id=$2 AND client_id=$3)`, account, user, s.Google.Config.ClientID).Scan(&exists)
		if readError(w, err) {
			return
		}
		if !exists {
			fail(w, 404, "google_account_not_found")
			return
		}
	}
	// A newer attempt replaces older pending flows for this user. Logout and
	// session revocation also invalidate pending flows through the foreign key.
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_oauth_states WHERE user_id=$1 OR expires_at<=$2`, user, s.now()); readError(w, err) {
		return
	}
	expires := s.now().Add(10 * time.Minute)
	_, err = tx.Exec(r.Context(), `INSERT INTO bia_go.google_oauth_states(state_hash,user_id,session_hash,client_id,verifier,created_at,expires_at,account_id) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))`, digest(state), user, digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), s.Google.Config.ClientID, sealed, s.now(), expires, account)
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
		googleFlowFailure(w, r, 400, "google_invalid_state")
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
		googleFlowFailure(w, r, 400, "google_invalid_state")
		return
	}
	if readError(w, err) || readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	browser := randomToken()
	var sealed []byte
	err = tx.QueryRow(r.Context(), `UPDATE bia_go.google_oauth_states o SET browser_hash=$2,started_at=$3 FROM bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.started_at IS NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.verifier`, digest(state), digest(browser), s.now(), s.Google.Config.ClientID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		googleFlowFailure(w, r, 400, "google_invalid_state")
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
		googleFlowFailure(w, r, 400, "google_invalid_state")
		return
	}
	// Claim in its own committed statement. A timeout or rollback during token
	// exchange cannot make this browser/state pair usable a second time.
	var user string
	err = s.DB.Pool.QueryRow(r.Context(), `UPDATE bia_go.google_oauth_states o SET consumed_at=$3 FROM bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.browser_hash=$2 AND o.consumed_at IS NULL AND o.started_at IS NOT NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.user_id`, digest(state), digest(cookie.Value), s.now(), s.Google.Config.ClientID).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		googleFlowFailure(w, r, 400, "google_invalid_state")
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
	var account string
	err = tx.QueryRow(r.Context(), `DELETE FROM bia_go.google_oauth_states o USING bia_go.management_sessions s,bia_go.management_users u WHERE o.state_hash=$1 AND o.browser_hash=$2 AND o.consumed_at IS NOT NULL AND o.started_at IS NOT NULL AND o.expires_at>$3 AND o.client_id=$4 AND s.token_hash=o.session_hash AND s.expires_at>$3 AND u.id=o.user_id AND u.enabled AND u.role IN ('operator','admin') RETURNING o.verifier,COALESCE(o.account_id,'')`, digest(state), digest(cookie.Value), s.now(), s.Google.Config.ClientID).Scan(&sealed, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		googleFlowFailure(w, r, 400, "google_invalid_state")
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
		googleFlowFailure(w, r, status, code)
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
	identity, err := s.Google.Identity(r.Context(), token)
	if err != nil {
		finishFailure(502, "google_identity_unavailable")
		return
	}
	if account != "" {
		var subject string
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(google_subject,'') FROM bia_go.google_accounts WHERE id=$1 AND user_id=$2 AND client_id=$3`, account, user, s.Google.Config.ClientID).Scan(&subject)
		if errors.Is(err, pgx.ErrNoRows) {
			finishFailure(400, "google_invalid_state")
			return
		}
		if readError(w, err) {
			return
		}
		if subject != "" && subject != identity.Subject {
			finishFailure(409, "google_account_mismatch")
			return
		}
	}
	var existing string
	err = tx.QueryRow(r.Context(), `SELECT id FROM bia_go.google_accounts WHERE user_id=$1 AND client_id=$2 AND google_subject=$3`, user, s.Google.Config.ClientID, identity.Subject).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		readError(w, err)
		return
	}
	if existing != "" {
		if account != "" && account != existing {
			finishFailure(409, "google_account_already_connected")
			return
		}
		account = existing
	}
	if account == "" {
		account = "gac_" + randomToken()[:32]
	}
	raw, err := json.Marshal(token)
	if err != nil {
		finishFailure(503, "google_credential_unavailable")
		return
	}
	credential, err := s.Google.Seal(googleCredentialSubject(user, account, 2), raw)
	if err != nil {
		finishFailure(503, "google_credential_unavailable")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO bia_go.google_accounts(id,user_id,client_id,google_subject,google_email,credential,credential_version,scopes,expires_at,connected_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,2,$7,$8,$9,$9) ON CONFLICT(id) DO UPDATE SET google_subject=EXCLUDED.google_subject,google_email=EXCLUDED.google_email,credential=EXCLUDED.credential,credential_version=2,scopes=EXCLUDED.scopes,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at`, account, user, s.Google.Config.ClientID, identity.Subject, identity.Email, credential, scopes, token.Expiry, s.now())
	if readError(w, err) || readError(w, googleAudit(r.Context(), tx, user, "google.connected")) || readError(w, tx.Commit(r.Context())) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		googleResultPage(w, 200, "Conta Google conectada", "A autorização desta conta foi salva. Volte à página de conexões para conferir suas contas e agendas. A sincronização de reuniões será habilitada separadamente.")
		return
	}
	write(w, 200, map[string]any{"connected": true, "account_id": account, "account_email": identity.Email, "synchronization_enabled": false})
}

func stringExtra(token *oauth2.Token, key string) string {
	v, _ := token.Extra(key).(string)
	return v
}

func googleAudit(ctx context.Context, tx pgx.Tx, user, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO bia_go.management_actions(id,actor_id,kind,fingerprint,request,result) VALUES($1,$2,$3,$4,'{}','{}')`, "google_"+randomToken()[:32], user, kind, digest(kind))
	return err
}

type googleAccountStatus struct {
	ID                string    `json:"id"`
	Email             string    `json:"email"`
	ConnectedAt       time.Time `json:"connected_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	TokenExpiresAt    time.Time `json:"token_expires_at"`
	Scopes            []string  `json:"scopes"`
	RequiresReconnect bool      `json:"requires_reconnect"`
}

func (s *Server) googleStatus(w http.ResponseWriter, r *http.Request) {
	accounts := []googleAccountStatus{}
	result := map[string]any{"configured": s.Google != nil, "connected": false, "account_count": 0, "accounts": accounts, "redirect_uri": googlecalendar.DefaultRedirectURL, "synchronization_enabled": false}
	if s.Google == nil {
		write(w, 200, result)
		return
	}
	result["redirect_uri"] = s.Google.Config.RedirectURL
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT id,google_email,connected_at,updated_at,expires_at,scopes,google_subject IS NULL FROM bia_go.google_accounts WHERE user_id=$1 AND client_id=$2 ORDER BY connected_at,id`, actor(r).ID, s.Google.Config.ClientID)
	if readError(w, err) {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var a googleAccountStatus
		if readError(w, rows.Scan(&a.ID, &a.Email, &a.ConnectedAt, &a.UpdatedAt, &a.TokenExpiresAt, &a.Scopes, &a.RequiresReconnect)) {
			return
		}
		accounts = append(accounts, a)
	}
	if readError(w, rows.Err()) {
		return
	}
	result["accounts"], result["account_count"], result["connected"] = accounts, len(accounts), len(accounts) > 0
	// Keep the original single-account fields for existing consumers.
	if len(accounts) == 1 {
		a := accounts[0]
		result["connected_at"], result["token_expires_at"], result["scopes"] = a.ConnectedAt, a.TokenExpiresAt, a.Scopes
	}
	write(w, 200, result)
}

var errGoogleAccountAmbiguous = errors.New("google_account_id_required")

func (s *Server) googleAccountID(ctx context.Context, tx pgx.Tx, user, requested string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM bia_go.google_accounts WHERE user_id=$1 AND client_id=$2 AND ($3='' OR id=$3) ORDER BY connected_at,id LIMIT 2`, user, s.Google.Config.ClientID, requested)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", pgx.ErrNoRows
	}
	if len(ids) > 1 {
		return "", errGoogleAccountAmbiguous
	}
	return ids[0], nil
}
func googleAccountError(w http.ResponseWriter, err error, requested string) bool {
	if errors.Is(err, errGoogleAccountAmbiguous) {
		fail(w, 422, "google_account_id_required")
		return true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if requested != "" {
			fail(w, 404, "google_account_not_found")
		} else {
			fail(w, 409, "google_not_connected")
		}
		return true
	}
	return readError(w, err)
}

func (s *Server) googleCalendars(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	page, requested := r.URL.Query().Get("page_token"), r.URL.Query().Get("account_id")
	if len(page) > 4096 {
		fail(w, 422, "invalid_page_token")
		return
	}
	if len(requested) > 80 {
		fail(w, 422, "invalid_account_id")
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
	account, err := s.googleAccountID(r.Context(), tx, user, requested)
	if googleAccountError(w, err, requested) {
		return
	}
	var credential []byte
	var version int
	err = tx.QueryRow(r.Context(), `SELECT credential,credential_version FROM bia_go.google_accounts WHERE id=$1 AND user_id=$2`, account, user).Scan(&credential, &version)
	if readError(w, err) {
		return
	}
	raw, err := s.Google.Open(googleCredentialSubject(user, account, version), credential)
	var saved oauth2.Token
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		fail(w, 503, "google_credential_unavailable")
		return
	}
	token, err := s.Google.OAuth.TokenSource(s.Google.Context(r.Context()), &saved).Token()
	if err != nil || !strings.EqualFold(token.Type(), "Bearer") || !token.Valid() {
		fail(w, 502, "google_refresh_failed")
		return
	}
	if token.RefreshToken == "" {
		token.RefreshToken = saved.RefreshToken
	}
	raw, err = json.Marshal(token)
	if readError(w, err) {
		return
	}
	credential, err = s.Google.Seal(googleCredentialSubject(user, account, 2), raw)
	if readError(w, err) {
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE bia_go.google_accounts SET credential=$2,credential_version=2,expires_at=$3,updated_at=$4 WHERE id=$1 AND user_id=$5`, account, credential, token.Expiry, s.now(), user)
	if readError(w, err) || readError(w, tx.Commit(r.Context())) {
		return
	}
	items, next, err := s.Google.Calendars(r.Context(), token, page)
	if err != nil {
		fail(w, 502, "google_calendar_unavailable")
		return
	}
	write(w, 200, map[string]any{"account_id": account, "items": items, "next_page_token": next})
}

// A removal is always scoped to one account. With multiple accounts the caller
// must identify it explicitly, so legacy clients cannot disconnect everything.
func (s *Server) googleDisconnect(w http.ResponseWriter, r *http.Request) {
	if !s.googleReady(w) {
		return
	}
	requested, valid := googleAccountRequest(w, r)
	if !valid {
		return
	}
	user := actor(r).ID
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	if readError(w, googleLock(r.Context(), tx, user)) {
		return
	}
	account, err := s.googleAccountID(r.Context(), tx, user, requested)
	if errors.Is(err, pgx.ErrNoRows) && requested == "" {
		account = ""
	} else if googleAccountError(w, err, requested) {
		return
	}
	// Also invalidate the user's in-flight add/reconnect flow to prevent an old
	// callback from restoring an account immediately after removal.
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_oauth_states WHERE user_id=$1`, user); readError(w, err) {
		return
	}
	if account != "" {
		if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_accounts WHERE id=$1 AND user_id=$2`, account, user); readError(w, err) {
			return
		}
		if strings.HasPrefix(account, "gac_legacy_") {
			if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.google_connections WHERE user_id=$1`, user); readError(w, err) {
				return
			}
		}
	}
	var remaining int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM bia_go.google_accounts WHERE user_id=$1 AND client_id=$2`, user, s.Google.Config.ClientID).Scan(&remaining); readError(w, err) {
		return
	}
	if readError(w, googleAudit(r.Context(), tx, user, "google.disconnected")) || readError(w, tx.Commit(r.Context())) {
		return
	}
	write(w, 200, map[string]any{"connected": remaining > 0, "account_id": account, "account_count": remaining})
}
