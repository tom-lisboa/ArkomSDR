package googlecalendar

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

// AccountToken serializes refresh with the management OAuth flow and persists
// refreshed credentials before external calendar operations. Ownership is explicit.
func (c *Client) AccountToken(ctx context.Context, pool *pgxpool.Pool, user, account string, requireScheduling bool) (*oauth2.Token, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "google-oauth:"+user); err != nil {
		return nil, err
	}
	var credential []byte
	var scopes []string
	var version int
	err = tx.QueryRow(ctx, `SELECT credential,credential_version,scopes FROM bia_go.google_accounts WHERE id=$1 AND user_id=$2 AND client_id=$3`, account, user, c.Config.ClientID).Scan(&credential, &version, &scopes)
	if err != nil {
		return nil, errors.New("google_account_not_found")
	}
	if requireScheduling && !CanSchedule(scopes) {
		return nil, errors.New("google_scheduling_permission_required")
	}
	subject := "account:" + user + ":" + account
	if version == 1 {
		subject = "token:" + user
	}
	raw, err := c.Open(subject, credential)
	var saved oauth2.Token
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		return nil, errors.New("google_credential_unavailable")
	}
	token, err := c.OAuth.TokenSource(c.Context(ctx), &saved).Token()
	if err != nil || !strings.EqualFold(token.Type(), "Bearer") || !token.Valid() {
		return nil, errors.New("google_refresh_failed")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = saved.RefreshToken
	}
	raw, err = json.Marshal(token)
	if err != nil {
		return nil, err
	}
	credential, err = c.Seal("account:"+user+":"+account, raw)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE bia_go.google_accounts SET credential=$2,credential_version=2,expires_at=$3 WHERE id=$1 AND user_id=$4`, account, credential, token.Expiry, user)
	if err != nil {
		return nil, err
	}
	return token, tx.Commit(ctx)
}
