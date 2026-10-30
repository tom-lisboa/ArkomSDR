// Package management exposes the staff backend. It never needs a frontend or a
// separate copy of the SDR database, and never grants staff the internal API key.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/lumi"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"github.com/jackc/pgx/v5"
)

var errInput = errors.New("invalid_input")

type Server struct {
	Lumi          *lumi.Client
	CredentialKey string
	StalledAfter  time.Duration
	DB            *store.DB
	Mode          string
	Policy        domain.Policy
	Origins       []string
	Download      *providers.Downloader
	Now           func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]any{"error": code, "request_id": w.Header().Get("X-Request-ID"), "retryable": status == 503 || code == "contact_busy"})
}
func readError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, 404, "not_found")
	} else {
		fail(w, 503, "storage_unavailable")
	}
	return true
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_json")
		return false
	}
	return true
}
func (s *Server) leadRoute(role string, next http.HandlerFunc) http.HandlerFunc {
	return s.auth(role, func(w http.ResponseWriter, r *http.Request) {
		var exists bool
		err := s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bia_go.leads WHERE id=$1)`, r.PathValue("id")).Scan(&exists)
		if readError(w, err) {
			return
		}
		if !exists {
			fail(w, 404, "not_found")
			return
		}
		next(w, r)
	})
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	prefix := "/v1/management"
	m.HandleFunc("POST "+prefix+"/auth/login", s.login)
	m.HandleFunc("POST "+prefix+"/auth/logout", s.auth("viewer", func(w http.ResponseWriter, r *http.Request) {
		_, err := s.DB.Pool.Exec(r.Context(), `DELETE FROM bia_go.management_sessions WHERE token_hash=$1`, digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if readError(w, err) {
			return
		}
		write(w, 200, map[string]bool{"logged_out": true})
	}))
	m.HandleFunc("GET "+prefix+"/auth/me", s.auth("viewer", func(w http.ResponseWriter, r *http.Request) { write(w, 200, actor(r)) }))
	m.HandleFunc("GET "+prefix+"/users", s.auth("viewer", s.users))
	m.HandleFunc("POST "+prefix+"/users", s.auth("admin", s.createUser))
	m.HandleFunc("PATCH "+prefix+"/users/{user}", s.auth("admin", s.updateUser))
	m.HandleFunc("GET "+prefix+"/leads", s.auth("viewer", s.leads))
	m.HandleFunc("GET "+prefix+"/leads/{id}", s.auth("viewer", s.lead))
	m.HandleFunc("GET "+prefix+"/leads/{id}/messages", s.leadRoute("viewer", s.messages))
	m.HandleFunc("GET "+prefix+"/leads/{id}/calls", s.leadRoute("viewer", s.calls))
	m.HandleFunc("GET "+prefix+"/leads/{id}/calls/{call}/transcript", s.leadRoute("viewer", s.transcript))
	m.HandleFunc("GET "+prefix+"/leads/{id}/followups", s.leadRoute("viewer", s.followups))
	m.HandleFunc("GET "+prefix+"/leads/{id}/actions", s.leadRoute("viewer", s.audit))
	m.HandleFunc("POST "+prefix+"/leads/{id}/actions", s.leadRoute("operator", s.action))
	m.HandleFunc("GET "+prefix+"/leads/{id}/bookings", s.leadRoute("viewer", s.bookings))
	m.HandleFunc("GET "+prefix+"/leads/{id}/media/{asset}", s.leadRoute("viewer", s.media))
	m.HandleFunc("GET "+prefix+"/metrics", s.auth("viewer", s.metrics))
	m.HandleFunc("GET "+prefix+"/sellers", s.auth("admin", s.sellers))
	m.HandleFunc("GET "+prefix+"/sellers/whatsapp", s.auth("operator", s.sellerStatus))
	m.HandleFunc("POST "+prefix+"/sellers/whatsapp", s.auth("operator", s.connectSeller))
	m.HandleFunc("PATCH "+prefix+"/sellers/whatsapp", s.auth("operator", s.sellerPreferences))
	m.HandleFunc("POST "+prefix+"/sellers/whatsapp/qr", s.auth("operator", s.sellerQRCommand))
	m.HandleFunc("GET "+prefix+"/sellers/whatsapp/qr.png", s.auth("operator", s.sellerQRImage))
	m.HandleFunc("POST "+prefix+"/sellers/whatsapp/disconnect", s.auth("operator", s.disconnectSeller))
	m.HandleFunc("POST "+prefix+"/leads/{id}/notifications/stalled", s.leadRoute("operator", s.notifyStalled))
	m.HandleFunc("GET "+prefix+"/notifications", s.auth("viewer", s.notifications))
	m.HandleFunc("GET "+prefix+"/openapi.json", s.auth("viewer", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPI)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", randomToken()[:24])
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, o := range s.Origins {
				allowed = allowed || origin == o
			}
			if !allowed {
				fail(w, 403, "origin_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization,Content-Type,Idempotency-Key")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
