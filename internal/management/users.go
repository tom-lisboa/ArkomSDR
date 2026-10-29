package management

import (
	"encoding/json"
	"net/http"
)

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  *bool  `json:"enabled,omitempty"`
		Role     string `json:"role,omitempty"`
		Password string `json:"password,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Enabled == nil && req.Role == "" && req.Password == "" {
		fail(w, 422, "empty_update")
		return
	}
	if req.Role != "" && req.Role != "viewer" && req.Role != "operator" && req.Role != "admin" {
		fail(w, 422, "invalid_role")
		return
	}
	hash := ""
	var err error
	if req.Password != "" {
		hash, err = passwordHash(req.Password)
		if err != nil {
			fail(w, 422, "password_length_12_to_256_required")
			return
		}
	}
	tx, err := s.DB.Pool.Begin(r.Context())
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize admin demotions to keep at least one active administrator.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('management-admin-users',0))`); readError(w, err) {
		return
	}
	var u User
	err = tx.QueryRow(r.Context(), `SELECT id,username,name,role,enabled FROM bia_go.management_users WHERE id=$1 FOR UPDATE`, r.PathValue("user")).Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.Enabled)
	if readError(w, err) {
		return
	}
	oldRole := u.Role
	oldEnabled := u.Enabled
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	if req.Role != "" {
		u.Role = req.Role
	}
	if oldRole == "admin" && oldEnabled && (!u.Enabled || u.Role != "admin") {
		var others int
		err = tx.QueryRow(r.Context(), `SELECT count(*) FROM bia_go.management_users WHERE id<>$1 AND role='admin' AND enabled`, u.ID).Scan(&others)
		if readError(w, err) {
			return
		}
		if others == 0 {
			fail(w, 409, "last_admin_required")
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE bia_go.management_users SET role=$2,enabled=$3,password_hash=CASE WHEN $4='' THEN password_hash ELSE $4 END WHERE id=$1`, u.ID, u.Role, u.Enabled, hash); readError(w, err) {
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM bia_go.management_sessions WHERE user_id=$1`, u.ID); readError(w, err) {
		return
	}
	request, _ := json.Marshal(map[string]any{"user_id": u.ID, "enabled": u.Enabled, "role": u.Role, "password_changed": hash != ""})
	output, _ := json.Marshal(u)
	id := "user_" + randomToken()[:32]
	if _, err = tx.Exec(r.Context(), `INSERT INTO bia_go.management_actions(id,actor_id,kind,fingerprint,request,result) VALUES($1,$2,'user.updated',$3,$4,$5)`, id, actor(r).ID, digest(string(request)), request, output); readError(w, err) {
		return
	}
	if readError(w, tx.Commit(r.Context())) {
		return
	}
	write(w, 200, u)
}
