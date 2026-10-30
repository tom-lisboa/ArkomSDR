package management

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/domain"
	"github.com/jackc/pgx/v5"
)

type cursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func page(r *http.Request) (int, *cursor, error) {
	n := 50
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err = strconv.Atoi(raw)
	}
	if err != nil || n < 1 || n > 100 {
		return 0, nil, errInput
	}
	var c *cursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(b) > 500 {
			return 0, nil, errInput
		}
		c = &cursor{}
		if json.Unmarshal(b, c) != nil || c.At.IsZero() || c.ID == "" || len(c.ID) > 300 {
			return 0, nil, errInput
		}
	}
	return n, c, nil
}
func encodeCursor(at time.Time, id string) string {
	b, _ := json.Marshal(cursor{At: at, ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func cursorValues(c *cursor) (any, string) {
	if c == nil {
		return nil, ""
	}
	return c.At, c.ID
}

const followupPurposes = `('followup','reminder','recovery','meeting_preparation','meeting_audio','meeting_call','meeting_sms','meeting_start','no_reply_15m','no_reply_2h','no_reply_24h','no_reply_72h','meeting_checkin_24h','meeting_checkin_2h','meeting_checkin_5m','meeting_checkin_7m_after')`
const deliveredSQL = `mode='live' AND kind='effect' AND payload->>'kind' IN ('send','send_audio','send_sms','send_video') AND COALESCE(result->>'chunked','false')<>'true'`

func leadCTE() string {
	score, facts := scoreExpressions()
	return `WITH inbound AS (
 SELECT c.lead_id,count(*)::bigint AS inbound_count,max(m.occurred_at) AS last_reply FROM bia_go.conversations c JOIN bia_go.conversation_messages m ON m.conversation_id=c.id WHERE m.role='user' GROUP BY c.lead_id
 ), outreach AS (
 SELECT lead_id,count(*) FILTER(WHERE status='done' AND ` + deliveredSQL + ` AND payload->>'purpose' IN ` + followupPurposes + `) AS followups_sent,
 count(*) FILTER(WHERE status='done' AND ` + deliveredSQL + ` AND payload->>'purpose' IN ` + followupPurposes + ` AND COALESCE((payload->>'manual_reminder')::boolean,false)) AS followups_manual,
 count(*) FILTER(WHERE status IN ('dead','uncertain')) AS failed_jobs FROM bia_go.jobs GROUP BY lead_id
 ), base AS (
 SELECT l.id,l.state,l.revision,l.updated_at,COALESCE(m.status,'open') AS status,m.assigned_to,m.first_won_at,m.closed_at,
 COALESCE(i.inbound_count,0) AS inbound_count,i.last_reply,COALESCE(o.followups_sent,0) AS followups_sent,COALESCE(o.followups_manual,0) AS followups_manual,COALESCE(o.failed_jobs,0) AS failed_jobs,
 COALESCE((SELECT a.outcome FROM bia_go.meeting_attendance a WHERE a.lead_id=l.id ORDER BY a.starts_at DESC LIMIT 1),NULLIF(l.state#>>'{booking,outcome}',''),'') AS last_outcome,
 CASE WHEN COALESCE((l.state->>'opted_out')::boolean,false) THEN 'opted_out' WHEN COALESCE((l.state->>'paused')::boolean,false) THEN 'paused' WHEN COALESCE((l.state->>'human')::boolean,false) THEN 'human' WHEN COALESCE((l.state#>>'{profile,consent}')::boolean,false) THEN 'active' ELSE 'no_consent' END AS automation,
 CASE WHEN l.state#>>'{booking,outcome}' IN ('attended','no_show') THEN l.state#>>'{booking,outcome}'
 WHEN (l.state#>>'{booking,start}')::timestamptz<=$1 AND (l.state#>>'{booking,end}')::timestamptz>$1 THEN 'in_meeting'
 WHEN (l.state#>>'{booking,end}')::timestamptz<=$1 AND COALESCE(l.state#>>'{booking,outcome}','')='' THEN 'awaiting_attendance'
 WHEN l.state#>>'{booking,confirmed_at}' IS NOT NULL THEN 'presence_confirmed'
 WHEN l.state->'booking' IS NOT NULL AND l.state->'booking'<>'null'::jsonb THEN 'booked'
 WHEN l.state->>'stage' IN ('choosing_slot','awaiting_availability') THEN 'scheduling'
 WHEN l.state->>'stage'='cancelled' THEN 'cancelled'
 WHEN COALESCE(i.inbound_count,0)>0 THEN 'engaged'
 WHEN l.state->>'first_contact_at' IS NOT NULL THEN 'contacted'
 ELSE 'contact_pending' END AS pipeline_stage
 FROM bia_go.leads l LEFT JOIN bia_go.lead_management m ON m.lead_id=l.id LEFT JOIN inbound i ON i.lead_id=l.id LEFT JOIN outreach o ON o.lead_id=l.id
 ), scored AS (SELECT *,` + score + ` AS score,` + facts + ` AS facts FROM base) `
}
func (s *Server) leads(w http.ResponseWriter, r *http.Request) {
	n, c, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	q := r.URL.Query()
	if len(q.Get("q")) > 200 {
		fail(w, 422, "invalid_query")
		return
	}
	for key, allowed := range map[string]string{"status": "|open|closed|won|lost|", "temperature": "|hot|warm|cold|", "automation": "|active|paused|human|opted_out|no_consent|", "stage": "|contact_pending|contacted|engaged|scheduling|booked|presence_confirmed|in_meeting|awaiting_attendance|attended|no_show|cancelled|"} {
		v := q.Get(key)
		if v != "" && !strings.Contains(allowed, "|"+v+"|") {
			fail(w, 422, "invalid_filter_"+key)
			return
		}
	}
	at, id := cursorValues(c)
	rows, err := s.DB.Pool.Query(r.Context(), leadCTE()+`SELECT id,state,revision,updated_at,status,COALESCE(assigned_to,''),pipeline_stage,automation,inbound_count,last_reply,followups_sent,followups_manual,failed_jobs,score,facts FROM scored
 WHERE ($2='' OR concat_ws(' ',state#>>'{profile,name}',state#>>'{profile,company}',state#>>'{profile,phone}',state#>>'{profile,email}') ILIKE '%'||$2||'%')
 AND ($3='' OR status=$3) AND ($4='' OR pipeline_stage=$4) AND ($5='' OR automation=$5) AND ($6='' OR COALESCE(assigned_to,'')=$6)
 AND ($7='' OR CASE WHEN score>=70 THEN 'hot' WHEN score>=40 THEN 'warm' ELSE 'cold' END=$7)
 AND ($8::timestamptz IS NULL OR (updated_at,id)<($8,$9)) ORDER BY updated_at DESC,id DESC LIMIT $10`, s.now(), q.Get("q"), q.Get("status"), q.Get("stage"), q.Get("automation"), q.Get("assigned_to"), q.Get("temperature"), at, id, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		v, at, id, err := scanLead(rows, s.now())
		if err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(items) == n {
			last := items[len(items)-1]
			next = encodeCursor(last["updated_at"].(time.Time), last["id"].(string))
			break
		}
		_ = at
		_ = id
		items = append(items, v)
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next, "as_of": s.now()})
}
func scanLead(row pgx.Row, now time.Time) (map[string]any, time.Time, string, error) {
	var id, status, assigned, stage, automation string
	var raw, factsRaw []byte
	var at time.Time
	var reply *time.Time
	var revision, inbound, followups, manual, failed int64
	var score int
	err := row.Scan(&id, &raw, &revision, &at, &status, &assigned, &stage, &automation, &inbound, &reply, &followups, &manual, &failed, &score, &factsRaw)
	if err != nil {
		return nil, at, id, err
	}
	var state domain.State
	var facts map[string]bool
	if json.Unmarshal(raw, &state) != nil || json.Unmarshal(factsRaw, &facts) != nil {
		return nil, at, id, errInput
	}
	result := map[string]any{"id": id, "profile": state.Profile, "booking": state.Booking, "pipeline_stage": stage, "sdr_stage": state.Stage, "status": status, "assigned_to": assigned, "automation": automation, "revision": revision, "created_at": state.CreatedAt, "updated_at": at, "last_reply_at": reply, "inbound_messages": inbound, "followups_sent": followups, "followups_manual": manual, "attention_required": failed > 0 || state.Human, "failed_jobs": failed, "score": evaluate(facts, now)}
	return result, at, id, nil
}
func (s *Server) lead(w http.ResponseWriter, r *http.Request) {
	row := s.DB.Pool.QueryRow(r.Context(), leadCTE()+`SELECT id,state,revision,updated_at,status,COALESCE(assigned_to,''),pipeline_stage,automation,inbound_count,last_reply,followups_sent,followups_manual,failed_jobs,score,facts FROM scored WHERE id=$2`, s.now(), r.PathValue("id"))
	v, _, _, err := scanLead(row, s.now())
	if readError(w, err) {
		return
	}
	st, err := s.DB.Lead(r.Context(), r.PathValue("id"))
	if readError(w, err) {
		return
	}
	v["dialogue"], v["offered_slots"], v["meeting_results"], v["flags"] = st.Dialogue, st.Offered, st.MeetingResults, map[string]bool{"paused": st.Paused, "human": st.Human, "opted_out": st.OptedOut, "consent": st.Profile.Consent}
	write(w, 200, v)
}
func (s *Server) followups(w http.ResponseWriter, r *http.Request) {
	s.jobHistory(w, r, true)
}
func (s *Server) jobHistory(w http.ResponseWriter, r *http.Request, onlyFollowup bool) {
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
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT id,seq,queue,status,mode,payload->>'kind',payload->>'purpose',COALESCE((payload->>'manual_reminder')::boolean,false),attempts,due_at,completed_at,COALESCE(error_code,''),COALESCE(result->>'status','') FROM bia_go.jobs WHERE lead_id=$1 AND kind='effect' AND seq<$2 AND (NOT $3::boolean OR payload->>'purpose' IN `+followupPurposes+`) ORDER BY seq DESC LIMIT $4`, r.PathValue("id"), before, onlyFollowup, n+1)
	if err != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	var next any
	for rows.Next() {
		var id, queue, status, mode, kind, purpose, code, receipt string
		var manual bool
		var seq int64
		var attempts int
		var due time.Time
		var completed *time.Time
		if err = rows.Scan(&id, &seq, &queue, &status, &mode, &kind, &purpose, &manual, &attempts, &due, &completed, &code, &receipt); err != nil {
			fail(w, 503, "storage_unavailable")
			return
		}
		if len(out) == n {
			next = out[len(out)-1]["seq"]
			break
		}
		out = append(out, map[string]any{"id": id, "seq": seq, "queue": queue, "status": status, "mode": mode, "kind": kind, "purpose": purpose, "manual": manual, "attempts": attempts, "due_at": due, "completed_at": completed, "error_code": code, "provider_acceptance": receipt})
	}
	if rows.Err() != nil {
		fail(w, 503, "storage_unavailable")
		return
	}
	write(w, 200, map[string]any{"items": out, "next_before_seq": next})
}
