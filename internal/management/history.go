package management

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	n, c, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	at, id := cursorValues(c)
	rows, err := s.DB.Pool.Query(r.Context(), `WITH timeline AS (
 SELECT m.id,m.occurred_at AS at,m.body,m.role,
 CASE WHEN m.run_id LIKE 'voice/%' THEN 'voice' WHEN j.payload->>'kind'='send_sms' THEN 'sms' ELSE 'whatsapp' END AS channel,
 CASE WHEN m.role='user' THEN 'received' ELSE COALESCE(t.status,sms.status,'sent') END AS delivery_status,
 COALESCE(j.payload->>'purpose','') AS purpose,COALESCE(m.event->>'operator_id',j.payload->>'operator_id','') AS operator_id,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'kind',a.kind,'status',a.status,'text',a.body,'error_code',a.error_code) ORDER BY a.id) FROM bia_go.media_assets a WHERE a.message_id=m.id),CASE WHEN j.payload->>'kind' IN ('send_audio','send_video') THEN jsonb_build_array(jsonb_build_object('id','outbound-media','kind',CASE WHEN j.payload->>'kind'='send_audio' THEN 'audio' ELSE 'video' END,'status',j.status,'download_path',j.payload->>'media_url')) ELSE '[]'::jsonb END) AS attachments
 FROM bia_go.conversation_messages m JOIN bia_go.conversations c ON c.id=m.conversation_id
 LEFT JOIN bia_go.jobs j ON m.id='outbound/'||j.id LEFT JOIN bia_go.twilio_messages t ON t.id=j.id LEFT JOIN bia_go.sms_messages sms ON sms.id=j.id
 WHERE c.lead_id=$1
 UNION ALL
 SELECT 'outbox/'||j.id,COALESCE(j.completed_at,j.created_at),COALESCE(j.payload->>'text',''),'assistant',CASE WHEN j.payload->>'kind'='send_sms' THEN 'sms' ELSE 'whatsapp' END,
 CASE WHEN j.status='done' THEN COALESCE(t.status,sms.status,'sent') ELSE j.status END,COALESCE(j.payload->>'purpose',''),COALESCE(j.payload->>'operator_id',''),CASE WHEN j.payload->>'kind' IN ('send_audio','send_video') THEN jsonb_build_array(jsonb_build_object('id','outbound-media','kind',CASE WHEN j.payload->>'kind'='send_audio' THEN 'audio' ELSE 'video' END,'status',j.status,'download_path',j.payload->>'media_url')) ELSE '[]'::jsonb END
 FROM bia_go.jobs j LEFT JOIN bia_go.twilio_messages t ON t.id=j.id LEFT JOIN bia_go.sms_messages sms ON sms.id=j.id WHERE j.lead_id=$1 AND j.kind='effect' AND j.payload->>'kind' IN ('send','send_audio','send_sms','send_video') AND COALESCE(j.result->>'chunked','false')<>'true' AND NOT EXISTS(SELECT 1 FROM bia_go.conversation_messages m WHERE m.id='outbound/'||j.id)
 ) SELECT id,at,body,role,channel,delivery_status,purpose,operator_id,attachments FROM timeline WHERE ($2::timestamptz IS NULL OR (at,id)<($2,$3)) ORDER BY at DESC,id DESC LIMIT $4`, r.PathValue("id"), at, id, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		var id, body, role, channel, status, purpose, operator string
		var at time.Time
		var raw []byte
		if err = rows.Scan(&id, &at, &body, &role, &channel, &status, &purpose, &operator, &raw); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			last := items[n-1]
			next = encodeCursor(last["at"].(time.Time), last["id"].(string))
			break
		}
		var attachments []map[string]any
		if json.Unmarshal(raw, &attachments) != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		for _, a := range attachments {
			if a["download_path"] == nil {
				a["download_path"] = "/v1/management/leads/" + r.PathValue("id") + "/media/" + a["id"].(string)
			}
		}
		sender := "ai"
		if role == "user" {
			sender = "customer"
		}
		if operator != "" {
			sender = "operator"
		}
		items = append(items, map[string]any{"id": id, "at": at, "text": body, "role": role, "channel": channel, "sender_type": sender, "operator_id": operator, "delivery_status": status, "purpose": purpose, "attachments": attachments})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) calls(w http.ResponseWriter, r *http.Request) {
	n, c, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	at, id := cursorValues(c)
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT v.id,v.state,v.mode,v.purpose,v.created_at,v.updated_at,COALESCE(v.call_sid,''),COALESCE(v.error_code,''),v.stop_state,v.permission_at,v.revoked_at,(SELECT count(*) FROM bia_go.voice_transcripts t WHERE t.request_id=v.id) FROM bia_go.voice_requests v WHERE lead_id=$1 AND ($2::timestamptz IS NULL OR (v.created_at,v.id)<($2,$3)) ORDER BY v.created_at DESC,v.id DESC LIMIT $4`, r.PathValue("id"), at, id, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		var id, state, mode, purpose, sid, code, stopState string
		var created, updated time.Time
		var permission, revoked *time.Time
		var count int64
		if err = rows.Scan(&id, &state, &mode, &purpose, &created, &updated, &sid, &code, &stopState, &permission, &revoked, &count); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			last := items[n-1]
			next = encodeCursor(last["created_at"].(time.Time), last["id"].(string))
			break
		}
		items = append(items, map[string]any{"id": id, "state": state, "mode": mode, "purpose": purpose, "created_at": created, "updated_at": updated, "call_sid": sid, "error_code": code, "permission_at": permission, "revoked_at": revoked, "transcript_items": count, "recording_available": false, "stop_state": stopState})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	n, _, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after_seq"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			fail(w, 422, "invalid_after_seq")
			return
		}
	}
	var exists bool
	err = s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM bia_go.voice_requests WHERE id=$1 AND lead_id=$2)`, r.PathValue("call"), r.PathValue("id")).Scan(&exists)
	if readError(w, err) {
		return
	}
	if !exists {
		fail(w, 404, "not_found")
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT seq,role,body,created_at FROM bia_go.voice_transcripts WHERE request_id=$1 AND seq>$2 ORDER BY seq LIMIT $3`, r.PathValue("call"), after, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var next any
	for rows.Next() {
		var seq int64
		var role, body string
		var at time.Time
		if err = rows.Scan(&seq, &role, &body, &at); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			next = items[n-1]["seq"]
			break
		}
		items = append(items, map[string]any{"seq": seq, "role": role, "text": body, "at": at})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_after_seq": next, "recording_available": false})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	n, _, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	before := int64(1<<63 - 1)
	if raw := r.URL.Query().Get("before_seq"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			fail(w, 422, "invalid_before_seq")
			return
		}
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT a.id,a.seq,a.kind,a.created_at,a.request,a.result,u.id,u.name FROM bia_go.management_actions a JOIN bia_go.management_users u ON u.id=a.actor_id WHERE a.lead_id=$1 AND a.seq<$2 ORDER BY a.seq DESC LIMIT $3`, r.PathValue("id"), before, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var next any
	for rows.Next() {
		var id, kind, uid, name string
		var seq int64
		var at time.Time
		var request, result json.RawMessage
		if err = rows.Scan(&id, &seq, &kind, &at, &request, &result, &uid, &name); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			next = items[n-1]["seq"]
			break
		}
		items = append(items, map[string]any{"id": id, "seq": seq, "kind": kind, "at": at, "request": request, "result": result, "actor": map[string]string{"id": uid, "name": name}})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_before_seq": next})
}
func (s *Server) bookings(w http.ResponseWriter, r *http.Request) {
	n, c, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	at, id := cursorValues(c)
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT b.id,b.payload,b.created_at,b.cancelled_at,COALESCE(a.outcome,''),a.recorded_at FROM bia_go.bookings b LEFT JOIN bia_go.meeting_attendance a ON a.booking_id=b.id WHERE b.lead_id=$1 AND ($2::timestamptz IS NULL OR (b.created_at,b.id)<($2,$3)) ORDER BY b.created_at DESC,b.id DESC LIMIT $4`, r.PathValue("id"), at, id, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		var id, outcome string
		var raw json.RawMessage
		var at time.Time
		var cancelled, recorded *time.Time
		if err = rows.Scan(&id, &raw, &at, &cancelled, &outcome, &recorded); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			last := items[n-1]
			next = encodeCursor(last["created_at"].(time.Time), last["id"].(string))
			break
		}
		items = append(items, map[string]any{"id": id, "booking": raw, "created_at": at, "cancelled_at": cancelled, "attendance": outcome, "attendance_recorded_at": recorded})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	var source, kind string
	err := s.DB.Pool.QueryRow(r.Context(), `SELECT a.source_url,a.kind FROM bia_go.media_assets a JOIN bia_go.conversations c ON c.id=a.conversation_id WHERE a.id=$1 AND c.lead_id=$2`, r.PathValue("asset"), r.PathValue("id")).Scan(&source, &kind)
	if readError(w, err) {
		return
	}
	if s.Download == nil {
		fail(w, 503, "media_download_unavailable")
		return
	}
	data, ct, err := s.Download.Download(r.Context(), source)
	if err != nil {
		fail(w, 502, "media_provider_unavailable")
		return
	}
	if !(strings.HasPrefix(ct, "audio/") || ct == "application/ogg" || ct == "application/pdf" || ct == "text/plain" || ct == "image/png" || ct == "image/jpeg" || ct == "image/webp" || ct == "image/gif") {
		fail(w, 415, "unsupported_media_type")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename=media")
	if kind == "audio" || kind == "image" {
		w.Header().Set("Content-Disposition", "inline")
	}
	_, _ = w.Write(data)
}
