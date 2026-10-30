package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/lumi"
	"github.com/jackc/pgx/v5"
)

func (s *Server) stalledAfter() time.Duration {
	if s.StalledAfter > 0 {
		return s.StalledAfter
	}
	return 30 * time.Minute
}

type salesLead struct {
	ID, Stage, Status string
	State             domain.State
}

func (l salesLead) activity(now time.Time) time.Time {
	at := l.State.CreatedAt
	for _, v := range []time.Time{l.State.LastInbound, l.State.LastEventAt} {
		if v.After(at) {
			at = v
		}
	}
	if l.State.FirstContactAt != nil && l.State.FirstContactAt.After(at) {
		at = *l.State.FirstContactAt
	}
	if l.Stage == "awaiting_attendance" && l.State.Booking != nil && l.State.Booking.End.After(at) {
		at = l.State.Booking.End
	}
	if at.IsZero() || at.After(now) {
		return now.UTC().Truncate(time.Microsecond)
	}
	return at.UTC().Truncate(time.Microsecond)
}
func (l salesLead) eligible() bool {
	if l.Status != "open" || l.State.OptedOut || l.State.Paused || !l.State.Profile.Consent {
		return false
	}
	switch l.Stage {
	case "contact_pending", "contacted", "engaged", "scheduling", "awaiting_attendance", "no_show", "cancelled":
		return true
	}
	return false
}

type salesQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func salesLeads(ctx context.Context, q salesQuery, now time.Time, id string) ([]salesLead, error) {
	rows, err := q.Query(ctx, leadCTE()+`SELECT id,state,pipeline_stage,status FROM scored WHERE ($2='' OR id=$2) ORDER BY id`, now, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []salesLead{}
	for rows.Next() {
		var l salesLead
		var raw []byte
		if err = rows.Scan(&l.ID, &raw, &l.Stage, &l.Status); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &l.State); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

var errNotStalled = errors.New("lead_not_stalled")
var errNoSellers = errors.New("no_connected_sellers")
var errNotifyConflict = errors.New("idempotency_conflict")

// A single episode ends on a stage change or new client/business event. Automated
// follow-ups do not restart its clock. All connected sellers share that episode.
func (s *Server) queueStalled(ctx context.Context, onlyLead, actorID, key string) (map[string]any, error) {
	now := s.now()
	tx, err := s.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('sales-stall-scan',0))`); err != nil {
		return nil, err
	}
	operationID := "snt_" + digest(actorID+"\x00"+key)
	if actorID != "" {
		var oldLead string
		var result []byte
		err = tx.QueryRow(ctx, `SELECT lead_id,result FROM bia_go.management_actions WHERE id=$1`, operationID).Scan(&oldLead, &result)
		if err == nil {
			if oldLead != onlyLead {
				return nil, errNotifyConflict
			}
			var out map[string]any
			if json.Unmarshal(result, &out) != nil {
				return nil, errInput
			}
			out["duplicate"] = true
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	leads, err := salesLeads(ctx, tx, now, onlyLead)
	if err != nil {
		return nil, err
	}
	queued := int64(0)
	ids := []string{}
	var stage string
	var stalledSince time.Time
	for _, l := range leads {
		var since time.Time
		var episode int64
		err = tx.QueryRow(ctx, `INSERT INTO bia_go.sales_progress(lead_id,stage,activity_at,since,eligible) VALUES($1,$2,$3,$3,$4)
 ON CONFLICT(lead_id) DO UPDATE SET stage=EXCLUDED.stage,activity_at=EXCLUDED.activity_at,eligible=EXCLUDED.eligible,
 since=CASE WHEN sales_progress.stage<>EXCLUDED.stage OR sales_progress.eligible<>EXCLUDED.eligible THEN $5 WHEN sales_progress.activity_at<>EXCLUDED.activity_at THEN EXCLUDED.activity_at ELSE sales_progress.since END,
 episode=sales_progress.episode+CASE WHEN sales_progress.stage<>EXCLUDED.stage OR sales_progress.activity_at<>EXCLUDED.activity_at OR sales_progress.eligible<>EXCLUDED.eligible THEN 1 ELSE 0 END
 RETURNING since,episode`, l.ID, l.Stage, l.activity(now), l.eligible(), now).Scan(&since, &episode)
		if err != nil {
			return nil, err
		}
		if !l.eligible() || now.Sub(since) < s.stalledAfter() {
			if onlyLead != "" {
				if err = tx.Commit(ctx); err != nil {
					return nil, err
				}
				return nil, errNotStalled
			}
			continue
		}
		stage, stalledSince = l.Stage, since
		result, err := tx.Exec(ctx, `INSERT INTO bia_go.sales_notifications(id,lead_id,user_id,episode,generation,stage,stalled_since,activity_at,status,mode,created_at,updated_at)
 SELECT 'sal_'||md5(jsonb_build_array($1::text,$2::bigint,c.user_id)::text),$1,c.user_id,$2,c.generation,$3,$4,$5,'pending',$6,$7,$7
 FROM bia_go.seller_whatsapp c JOIN bia_go.management_users u ON u.id=c.user_id
 WHERE u.enabled AND u.role IN ('operator','admin') AND c.notifications_enabled AND c.status='connected' AND c.phone<>'' AND c.credential IS NOT NULL AND c.checked_at>=$7::timestamptz-interval '2 minutes'
 ON CONFLICT(lead_id,episode,user_id) DO NOTHING`, l.ID, episode, l.Stage, since, l.activity(now), s.Mode, now)
		if err != nil {
			return nil, err
		}
		queued += result.RowsAffected()
		if onlyLead != "" {
			rows, err := tx.Query(ctx, `SELECT id FROM bia_go.sales_notifications WHERE lead_id=$1 AND episode=$2 ORDER BY id`, l.ID, episode)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if len(ids) == 0 {
				if err = tx.Commit(ctx); err != nil {
					return nil, err
				}
				return nil, errNoSellers
			}
		}
	}
	result := map[string]any{"queued": queued, "notification_ids": ids, "duplicate": queued == 0, "audience": "all_connected_sellers", "status": "queued", "stage": stage, "stalled_since": stalledSince}
	if actorID != "" {
		raw, _ := json.Marshal(result)
		request, _ := json.Marshal(map[string]string{"lead_id": onlyLead})
		_, err = tx.Exec(ctx, `INSERT INTO bia_go.management_actions(id,actor_id,lead_id,kind,fingerprint,request,result,created_at) VALUES($1,$2,$3,'notify.stalled',$4,$5,$6,$7)`, operationID, actorID, onlyLead, digest(onlyLead), request, raw, now)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *Server) notifyStalled(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decode(w, r, &req) || !s.requireLumi(w) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 200 {
		fail(w, 422, "idempotency_key_required")
		return
	}
	result, err := s.queueStalled(r.Context(), r.PathValue("id"), actor(r).ID, key)
	if errors.Is(err, errNotStalled) || errors.Is(err, errNoSellers) || errors.Is(err, errNotifyConflict) {
		fail(w, 409, err.Error())
		return
	}
	if readError(w, err) {
		return
	}
	write(w, 202, result)
}
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	n, c, err := page(r)
	if err != nil {
		fail(w, 422, "invalid_pagination")
		return
	}
	at, id := cursorValues(c)
	status := r.URL.Query().Get("status")
	if status != "" && !strings.Contains("|pending|sending|accepted|sent|failed|uncertain|skipped|simulated|", "|"+status+"|") {
		fail(w, 422, "invalid_filter_status")
		return
	}
	rows, err := s.DB.Pool.Query(r.Context(), `SELECT n.id,n.lead_id,n.user_id,u.name,n.stage,n.stalled_since,n.status,n.mode,n.provider_id,n.error_code,n.created_at,n.updated_at FROM bia_go.sales_notifications n JOIN bia_go.management_users u ON u.id=n.user_id WHERE ($1='' OR n.lead_id=$1) AND ($2='' OR n.status=$2) AND ($3::timestamptz IS NULL OR (n.created_at,n.id)<($3,$4)) ORDER BY n.created_at DESC,n.id DESC LIMIT $5`, r.URL.Query().Get("lead_id"), status, at, id, n+1)
	if readError(w, err) {
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	next := ""
	for rows.Next() {
		var id, lead, user, name, stage, status, mode, provider, code string
		var since, created, updated time.Time
		if readError(w, rows.Scan(&id, &lead, &user, &name, &stage, &since, &status, &mode, &provider, &code, &created, &updated)) {
			return
		}
		if len(items) == n {
			last := items[len(items)-1]
			next = encodeCursor(last["created_at"].(time.Time), last["id"].(string))
			break
		}
		items = append(items, map[string]any{"id": id, "lead_id": lead, "user_id": user, "seller_name": name, "stage": stage, "stalled_since": since, "status": status, "mode": mode, "provider_id": provider, "error_code": code, "created_at": created, "updated_at": updated})
	}
	if readError(w, rows.Err()) {
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func safeLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("*_~`", r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 140 {
		runes = runes[:140]
	}
	return string(runes)
}
func salesMessage(l salesLead, since, now time.Time) string {
	labels := map[string]string{"contact_pending": "Contato inicial pendente", "contacted": "Primeiro contato realizado", "engaged": "Em conversa", "scheduling": "Escolha de horário", "awaiting_attendance": "Resultado da reunião pendente", "no_show": "Falta registrada", "cancelled": "Reunião cancelada"}
	name := safeLabel(l.State.Profile.Name)
	if name == "" {
		name = "Cliente sem nome"
	}
	return fmt.Sprintf("*Lead parado no funil*\n\nCliente: %s\nEmpresa: %s\nWhatsApp: %s\nEtapa: %s\nSem interação ou avanço há %d minutos.\n\nAbrir conversa: https://wa.me/%s\nID do lead: %s", name, safeLabel(l.State.Profile.Company), l.State.Profile.Phone, labels[l.Stage], max(0, int(now.Sub(since).Minutes())), strings.TrimPrefix(l.State.Profile.Phone, "+"), l.ID)
}

// SalesScan is called by the follow-up process, independently of client cadences.
func (s *Server) SalesScan(ctx context.Context) error {
	if s.Lumi == nil {
		return nil
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.user_id FROM bia_go.seller_whatsapp c JOIN bia_go.management_users u ON u.id=c.user_id WHERE u.enabled AND u.role IN ('operator','admin') AND c.instance_id IS NOT NULL AND c.notifications_enabled AND (c.checked_at IS NULL OR c.checked_at<$1::timestamptz-interval '30 seconds') ORDER BY c.checked_at NULLS FIRST LIMIT 100`, s.now())
	if err != nil {
		return err
	}
	users := []string{}
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			rows.Close()
			return err
		}
		users = append(users, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range users {
		if _, err = s.refreshSeller(ctx, u); err != nil {
			return err
		}
	}
	_, err = s.queueStalled(ctx, "", "", "")
	return err
}

func (s *Server) SalesOnce(ctx context.Context) (bool, error) {
	if s.Lumi == nil {
		return false, nil
	}
	now := s.now()
	// A crashed sender may have reached Lumi. It must never be replayed blindly.
	_, err := s.DB.Pool.Exec(ctx, `UPDATE bia_go.sales_notifications SET status='uncertain',error_code='sender_interrupted',updated_at=$1 WHERE status='sending' AND updated_at<$1::timestamptz-interval '1 minute'`, now)
	if err != nil {
		return false, err
	}
	_, err = s.DB.Pool.Exec(ctx, `UPDATE bia_go.sales_notifications n SET status='skipped',error_code='notification_obsolete',updated_at=$1
 FROM bia_go.sales_progress p,bia_go.seller_whatsapp c,bia_go.management_users u
 WHERE n.status='pending' AND p.lead_id=n.lead_id AND c.user_id=n.user_id AND u.id=n.user_id
 AND (p.episode<>n.episode OR NOT p.eligible OR c.generation<>n.generation OR NOT c.notifications_enabled OR NOT u.enabled OR u.role NOT IN ('operator','admin'))`, now)
	if err != nil {
		return false, err
	}
	var id, lead, user, stage, mode string
	var since, activity time.Time
	var credential []byte
	var phone, instance string
	err = s.DB.Pool.QueryRow(ctx, `WITH pick AS (
 SELECT n.id FROM bia_go.sales_notifications n JOIN bia_go.seller_whatsapp c ON c.user_id=n.user_id JOIN bia_go.management_users u ON u.id=n.user_id
 WHERE n.status='pending' AND c.notifications_enabled AND c.status='connected' AND c.checked_at>=$1::timestamptz-interval '2 minutes' AND c.phone<>'' AND c.generation=n.generation AND u.enabled AND u.role IN ('operator','admin') ORDER BY n.created_at,n.id FOR UPDATE OF n SKIP LOCKED LIMIT 1
 ) UPDATE bia_go.sales_notifications n SET status='sending',updated_at=$1 FROM pick,bia_go.seller_whatsapp c WHERE n.id=pick.id AND c.user_id=n.user_id RETURNING n.id,n.lead_id,n.user_id,n.stage,n.mode,n.stalled_since,n.activity_at,c.credential,c.phone,c.instance_id`, now).Scan(&id, &lead, &user, &stage, &mode, &since, &activity, &credential, &phone, &instance)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, s.pollSalesDeliveries(ctx)
	}
	if err != nil {
		return false, err
	}
	finish := func(status, code, provider string) error {
		_, e := s.DB.Pool.Exec(ctx, `UPDATE bia_go.sales_notifications SET status=$2,error_code=$3,provider_id=$4,updated_at=$5 WHERE id=$1 AND status='sending' AND updated_at=$6`, id, status, code, provider, s.now(), now)
		return e
	}
	leads, err := salesLeads(ctx, s.DB.Pool, now, lead)
	if err != nil {
		return true, finish("pending", "storage_unavailable", "")
	}
	if len(leads) != 1 || !leads[0].eligible() || leads[0].Stage != stage || !leads[0].activity(now).Equal(activity) {
		return true, finish("skipped", "lead_advanced", "")
	}
	if mode != "live" || s.Mode != "live" {
		return true, finish("simulated", "", "")
	}
	key, err := lumi.Open(s.CredentialKey, user, credential)
	if err != nil {
		return true, finish("failed", "credential_unavailable", "")
	}
	receipt, err := s.Lumi.Send(ctx, key, phone, salesMessage(leads[0], since, now))
	if err != nil {
		status := "uncertain"
		var failure *lumi.Failure
		if errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 {
			status = "failed"
		}
		return true, finish(status, lumiCode(err), "")
	}
	if receipt.InstanceID != "" && receipt.InstanceID != instance {
		return true, finish("uncertain", "lumi_instance_mismatch", receipt.ID)
	}
	return true, finish("accepted", "", receipt.ID)
}
func (s *Server) pollSalesDeliveries(ctx context.Context) error {
	rows, err := s.DB.Pool.Query(ctx, `SELECT n.id,n.provider_id,n.user_id,c.credential,c.instance_id FROM bia_go.sales_notifications n JOIN bia_go.seller_whatsapp c ON c.user_id=n.user_id WHERE n.status='accepted' AND n.updated_at<$1::timestamptz-interval '10 seconds' ORDER BY n.updated_at LIMIT 20`, s.now())
	if err != nil {
		return err
	}
	type item struct {
		id, provider, user, instance string
		credential                   []byte
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.provider, &v.user, &v.credential, &v.instance); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		key, err := lumi.Open(s.CredentialKey, v.user, v.credential)
		if err != nil {
			continue
		}
		receipt, err := s.Lumi.Delivery(ctx, key, v.provider)
		status, code := "accepted", ""
		if err != nil {
			code = lumiCode(err)
		} else if receipt.InstanceID != v.instance || receipt.ID != v.provider {
			code = "lumi_receipt_mismatch"
		} else {
			switch receipt.Status {
			case "sent":
				status = "sent"
			case "failed":
				status = "failed"
				code = "lumi_delivery_failed"
			}
		}
		_, err = s.DB.Pool.Exec(ctx, `UPDATE bia_go.sales_notifications SET status=$2,error_code=$3,updated_at=$4 WHERE id=$1 AND status='accepted'`, v.id, status, code, s.now())
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) RunSalesAlerts(ctx context.Context) {
	if s.Lumi == nil {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var nextScan time.Time
	for {
		step, cancel := context.WithTimeout(ctx, 25*time.Second)
		if !s.now().Before(nextScan) {
			if err := s.SalesScan(step); err != nil && ctx.Err() == nil {
				slog.Error("seller alert scan failed")
			}
			nextScan = s.now().Add(30 * time.Second)
		}
		if err := s.pollSalesDeliveries(step); err != nil && ctx.Err() == nil {
			slog.Error("seller alert receipts failed")
		}
		for range 5 {
			worked, err := s.SalesOnce(step)
			if err != nil && ctx.Err() == nil {
				slog.Error("seller alert processing failed")
			}
			if err != nil || !worked || step.Err() != nil {
				break
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
