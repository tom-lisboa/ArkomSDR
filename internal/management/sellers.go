package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/lumi"
	"github.com/jackc/pgx/v5"
)

type SellerConnection struct {
	UserID       string     `json:"user_id"`
	InstanceID   string     `json:"instance_id"`
	InstanceName string     `json:"instance_name"`
	Status       string     `json:"status"`
	Phone        string     `json:"phone"`
	Enabled      bool       `json:"notifications_enabled"`
	CheckedAt    *time.Time `json:"checked_at"`
	LastError    string     `json:"last_error,omitempty"`
	QRAvailable  bool       `json:"qr_available"`
	QRExpiresAt  *time.Time `json:"qr_expires_at,omitempty"`
	Credential   []byte     `json:"-"`
	Generation   int64      `json:"-"`
}

func (s *Server) sellerConnection(ctx context.Context, user string) (SellerConnection, error) {
	var c SellerConnection
	err := s.DB.Pool.QueryRow(ctx, `SELECT user_id,COALESCE(instance_id,''),instance_name,status,phone,notifications_enabled,checked_at,last_error,credential,generation FROM bia_go.seller_whatsapp WHERE user_id=$1`, user).Scan(&c.UserID, &c.InstanceID, &c.InstanceName, &c.Status, &c.Phone, &c.Enabled, &c.CheckedAt, &c.LastError, &c.Credential, &c.Generation)
	return c, err
}
func (s *Server) requireLumi(w http.ResponseWriter) bool {
	if s.Lumi == nil || s.Lumi.UserKey == "" || len(s.CredentialKey) < 32 {
		fail(w, 503, "lumi_not_configured")
		return false
	}
	return true
}
func lumiCode(err error) string {
	var failure *lumi.Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return "lumi_unavailable"
}
func (s *Server) refreshSeller(ctx context.Context, user string) (SellerConnection, error) {
	c, err := s.sellerConnection(ctx, user)
	if err != nil {
		return c, err
	}
	if c.InstanceID == "" || s.Lumi == nil {
		return c, nil
	}
	session, err := s.Lumi.Session(ctx, c.InstanceID)
	if err == nil {
		c.Status = session.Status
		c.QRAvailable = session.QRAvailable
		c.QRExpiresAt = session.QRExpiresAt
		if c.Status == "connected" {
			var instance lumi.Instance
			instance, err = s.Lumi.Instance(ctx, c.InstanceID)
			if err == nil {
				phone := strings.Split(strings.Split(instance.Device, "@")[0], ":")[0]
				if instance.ID != c.InstanceID {
					err = errors.New("instance_mismatch")
				} else if phone == "" {
					err = errors.New("phone_unavailable")
				} else {
					c.Phone, err = domain.NormalizePhone("+" + strings.TrimPrefix(phone, "+"))
				}
			}
		}
	}
	if err != nil {
		c.LastError = lumiCode(err)
		c.Status = "unavailable"
	} else {
		c.LastError = ""
	}
	now := s.now()
	c.CheckedAt = &now
	_, dbErr := s.DB.Pool.Exec(ctx, `UPDATE bia_go.seller_whatsapp SET status=$2,phone=$3,checked_at=$4,last_error=$5,updated_at=$4 WHERE user_id=$1 AND instance_id=$6 AND generation=$7`, user, c.Status, c.Phone, now, c.LastError, c.InstanceID, c.Generation)
	if dbErr != nil {
		return c, dbErr
	}
	return c, nil
}
func (s *Server) connectSeller(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decode(w, r, &req) || !s.requireLumi(w) {
		return
	}
	u := actor(r)
	name := "Arkom SDR - " + u.ID
	result, err := s.DB.Pool.Exec(r.Context(), `INSERT INTO bia_go.seller_whatsapp(user_id,instance_name,created_at,updated_at) VALUES($1,$2,$3,$3) ON CONFLICT(user_id) DO NOTHING`, u.ID, name, s.now())
	if readError(w, err) {
		return
	}
	if result.RowsAffected() == 0 {
		c, err := s.sellerConnection(r.Context(), u.ID)
		if readError(w, err) {
			return
		}
		// A previous request may still be provisioning. Never create a second instance.
		write(w, 202, c)
		return
	}
	instance, err := s.Lumi.Create(r.Context(), name)
	if err != nil {
		// Unknown outcome is visible and not retried: Lumi create is not idempotent.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.DB.Pool.Exec(ctx, `UPDATE bia_go.seller_whatsapp SET status='review_required',last_error=$2,updated_at=$3 WHERE user_id=$1`, u.ID, lumiCode(err), s.now())
		fail(w, 502, "lumi_provisioning_review_required")
		return
	}
	sealed, err := lumi.Seal(s.CredentialKey, u.ID, instance.APIKey)
	if err != nil {
		fail(w, 503, "credential_storage_unavailable")
		return
	}
	_, err = s.DB.Pool.Exec(r.Context(), `UPDATE bia_go.seller_whatsapp SET instance_id=$2,credential=$3,status='disconnected',last_error='',updated_at=$4 WHERE user_id=$1`, u.ID, instance.ID, sealed, s.now())
	if readError(w, err) {
		return
	}
	c, err := s.sellerConnection(r.Context(), u.ID)
	if readError(w, err) {
		return
	}
	write(w, 202, c)
}
func (s *Server) sellerStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireLumi(w) {
		return
	}
	c, err := s.refreshSeller(r.Context(), actor(r).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		write(w, 200, map[string]any{"status": "not_configured", "notifications_enabled": false})
		return
	}
	if readError(w, err) {
		return
	}
	write(w, 200, c)
}
func (s *Server) sellerPreferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"notifications_enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		fail(w, 422, "notifications_enabled_required")
		return
	}
	res, err := s.DB.Pool.Exec(r.Context(), `UPDATE bia_go.seller_whatsapp SET notifications_enabled=$2,updated_at=$3 WHERE user_id=$1`, actor(r).ID, *req.Enabled, s.now())
	if readError(w, err) {
		return
	}
	if res.RowsAffected() == 0 {
		fail(w, 404, "seller_connection_not_found")
		return
	}
	c, err := s.sellerConnection(r.Context(), actor(r).ID)
	if readError(w, err) {
		return
	}
	write(w, 200, c)
}
func (s *Server) sellerCommand(w http.ResponseWriter, r *http.Request, command string) {
	var req struct{}
	if !decode(w, r, &req) || !s.requireLumi(w) {
		return
	}
	c, err := s.sellerConnection(r.Context(), actor(r).ID)
	if readError(w, err) {
		return
	}
	if c.InstanceID == "" {
		fail(w, 409, "seller_connection_not_ready")
		return
	}
	if command == "logout" {
		// Stop new alerts before the remote logout; an upstream failure cannot leave sending enabled.
		_, err = s.DB.Pool.Exec(r.Context(), `UPDATE bia_go.seller_whatsapp SET notifications_enabled=false,status='disconnecting',generation=generation+1,updated_at=$2 WHERE user_id=$1`, c.UserID, s.now())
		if readError(w, err) {
			return
		}
	}
	if err = s.Lumi.Command(r.Context(), c.InstanceID, command); err != nil {
		fail(w, 502, lumiCode(err))
		return
	}
	write(w, 202, map[string]string{"status": "queued", "command": command})
}
func (s *Server) sellerQRCommand(w http.ResponseWriter, r *http.Request) { s.sellerCommand(w, r, "qr") }
func (s *Server) disconnectSeller(w http.ResponseWriter, r *http.Request) {
	s.sellerCommand(w, r, "logout")
}
func (s *Server) sellerQRImage(w http.ResponseWriter, r *http.Request) {
	if !s.requireLumi(w) {
		return
	}
	c, err := s.sellerConnection(r.Context(), actor(r).ID)
	if readError(w, err) {
		return
	}
	if c.InstanceID == "" {
		fail(w, 409, "seller_connection_not_ready")
		return
	}
	image, err := s.Lumi.QR(r.Context(), c.InstanceID)
	if err != nil {
		fail(w, 502, lumiCode(err))
		return
	}
	if len(image) < 8 || string(image[:8]) != "\x89PNG\r\n\x1a\n" {
		fail(w, 502, "invalid_lumi_qr")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(200)
	_, _ = w.Write(image)
}
func (s *Server) sellers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT u.id,u.name,u.enabled,COALESCE(c.instance_id,''),COALESCE(c.status,'not_configured'),COALESCE(c.phone,''),COALESCE(c.notifications_enabled,false),c.checked_at FROM bia_go.management_users u LEFT JOIN bia_go.seller_whatsapp c ON c.user_id=u.id WHERE u.role IN ('operator','admin') ORDER BY u.name,u.id LIMIT 1000`)
	if readError(w, err) {
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, instance, status, phone string
		var enabled, notifications bool
		var checked *time.Time
		if readError(w, rows.Scan(&id, &name, &enabled, &instance, &status, &phone, &notifications, &checked)) {
			return
		}
		items = append(items, map[string]any{"user_id": id, "name": name, "enabled": enabled, "instance_id": instance, "status": status, "phone": phone, "notifications_enabled": notifications, "checked_at": checked})
	}
	if readError(w, rows.Err()) {
		return
	}
	write(w, 200, map[string]any{"items": items, "lumi_configured": s.Lumi != nil, "stalled_after_seconds": int(s.stalledAfter().Seconds()), "audience": "all_connected_sellers"})
}
