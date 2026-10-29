package management

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/store"
	"github.com/jackc/pgx/v5"
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}
type userContext struct{}

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", errors.New("password_length_12_to_256_required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	return "pbkdf2_sha256$600000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), err
}
func passwordMatches(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds != 600000 {
		return false
	}
	salt, e1 := hex.DecodeString(parts[2])
	want, e2 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, rounds, 32)
	return err == nil && hmac.Equal(key, want)
}
func CreateUser(ctx context.Context, db *store.DB, username, name, password, role string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	name = strings.TrimSpace(name)
	if !usernameRE.MatchString(username) || name == "" || len(name) > 120 || (role != "viewer" && role != "operator" && role != "admin") {
		return User{}, errors.New("invalid_user")
	}
	hash, err := passwordHash(password)
	if err != nil {
		return User{}, err
	}
	u := User{ID: "usr_" + randomToken()[:32], Username: username, Name: name, Role: role, Enabled: true}
	_, err = db.Pool.Exec(ctx, `INSERT INTO bia_go.management_users(id,username,name,role,password_hash) VALUES($1,$2,$3,$4,$5)`, u.ID, username, name, role, hash)
	return u, err
}
func (s *Server) auth(required string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			fail(w, 401, "unauthorized")
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(token) != 64 {
			fail(w, 401, "unauthorized")
			return
		}
		var u User
		err := s.DB.Pool.QueryRow(r.Context(), `SELECT u.id,u.username,u.name,u.role,u.enabled FROM bia_go.management_sessions s JOIN bia_go.management_users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled`, digest(token)).Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.Enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			fail(w, 401, "unauthorized")
			return
		}
		if err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
		if rank[u.Role] < rank[required] {
			fail(w, 403, "forbidden")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContext{}, u)))
	}
}
func actor(r *http.Request) User { return r.Context().Value(userContext{}).(User) }
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.ToLower(strings.TrimSpace(req.Username))
	if len(req.Password) > 256 || len(req.Username) > 64 {
		fail(w, 401, "invalid_credentials")
		return
	}
	// A shared reverse proxy address is deliberately a shared safety ceiling.
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = "unknown"
	}
	var globalAttempts int
	if err := s.DB.Pool.QueryRow(r.Context(), `INSERT INTO bia_go.management_login_limits(id,attempts,reset_at) VALUES($1,1,now()+interval '15 minutes') ON CONFLICT(id) DO UPDATE SET attempts=CASE WHEN management_login_limits.reset_at<=now() THEN 1 ELSE management_login_limits.attempts+1 END,reset_at=CASE WHEN management_login_limits.reset_at<=now() THEN now()+interval '15 minutes' ELSE management_login_limits.reset_at END RETURNING attempts`, digest("connection:"+host)).Scan(&globalAttempts); err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	if globalAttempts > 120 {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "login_rate_limited")
		return
	}
	var attempts int
	err := s.DB.Pool.QueryRow(r.Context(), `INSERT INTO bia_go.management_login_limits(id,attempts,reset_at) VALUES($1,1,now()+interval '15 minutes') ON CONFLICT(id) DO UPDATE SET attempts=CASE WHEN management_login_limits.reset_at<=now() THEN 1 ELSE management_login_limits.attempts+1 END,reset_at=CASE WHEN management_login_limits.reset_at<=now() THEN now()+interval '15 minutes' ELSE management_login_limits.reset_at END RETURNING attempts`, digest("username:"+req.Username)).Scan(&attempts)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	if attempts > 10 {
		w.Header().Set("Retry-After", "900")
		fail(w, 429, "login_rate_limited")
		return
	}
	var u User
	var hash string
	err = s.DB.Pool.QueryRow(r.Context(), `SELECT id,username,name,role,enabled,password_hash FROM bia_go.management_users WHERE username=$1`, req.Username).Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.Enabled, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		fail(w, 503, "storage_unavailable")
		return
	}
	// Use equal-cost derivation for unknown users, without storing a fake account.
	if hash == "" {
		hash = "pbkdf2_sha256$600000$00000000000000000000000000000000$" + strings.Repeat("0", 64)
	}
	valid := passwordMatches(req.Password, hash)
	if !valid || u.ID == "" || !u.Enabled {
		fail(w, 401, "invalid_credentials")
		return
	}
	token := randomToken()
	expires := s.now().Add(12 * time.Hour)
	if _, err = s.DB.Pool.Exec(r.Context(), `INSERT INTO bia_go.management_sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, digest(token), u.ID, expires); err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	_, _ = s.DB.Pool.Exec(r.Context(), `DELETE FROM bia_go.management_login_limits WHERE id=$1 OR reset_at<now()-interval '1 day'`, digest("username:"+req.Username))
	_, _ = s.DB.Pool.Exec(r.Context(), `DELETE FROM bia_go.management_sessions WHERE expires_at<now()`)
	write(w, 200, map[string]any{"access_token": token, "token_type": "Bearer", "expires_at": expires, "user": u})
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT id,username,name,role,enabled FROM bia_go.management_users ORDER BY name,id LIMIT 500`)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.Enabled); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		out = append(out, u)
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": out})
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := CreateUser(r.Context(), s.DB, req.Username, req.Name, req.Password, req.Role)
	if err != nil {
		fail(w, 422, "invalid_or_existing_user")
		return
	}
	write(w, 201, u)
}
