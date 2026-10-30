package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arkom/bia/internal/domain"
	"arkom/bia/internal/lumi"
	"arkom/bia/internal/management"
	"arkom/bia/internal/store"
)

type lumiMock struct {
	mu      sync.Mutex
	created int
	sent    []string
	unknown bool
}

func (m *lumiMock) deliveries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}
func (m *lumiMock) instanceCount() int { m.mu.Lock(); defer m.mu.Unlock(); return m.created }
func (m *lumiMock) setUnknown()        { m.mu.Lock(); defer m.mu.Unlock(); m.unknown = true }

func fakeLumi(t *testing.T) (*lumi.Client, *lumiMock) {
	t.Helper()
	mock := &lumiMock{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" && r.URL.Path == "/api/instances" {
			if r.Header.Get("X-API-Key") != "user-key" {
				t.Error("missing user credential")
			}
			mock.created++
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("inst-%d", mock.created), "api_key": fmt.Sprintf("instance-key-%d", mock.created)})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/instances/") {
			if r.Header.Get("X-API-Key") != "user-key" {
				t.Error("user key missing from instance operation")
			}
			id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/instances/"), "/")[0]
			switch {
			case strings.HasSuffix(r.URL.Path, "/session"):
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "connected", "qr_available": true})
			case strings.HasSuffix(r.URL.Path, "/qr.png"):
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
			case r.Method == "POST":
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "command-1", "status": "queued"})
			default:
				_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "whatsapp_device_jid": "551199999900" + strings.TrimPrefix(id, "inst-") + ":4@s.whatsapp.net"})
			}
			return
		}
		if r.URL.Path == "/message/sendText" {
			var body struct{ Number, Text string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if strings.HasPrefix(r.Header.Get("X-API-Key"), "user-") {
				t.Error("management key used for sending")
			}
			mock.sent = append(mock.sent, body.Number+"|"+body.Text)
			if mock.unknown {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("out-%d", len(mock.sent)), "status": "pending", "instance_id": "inst-" + strings.TrimPrefix(r.Header.Get("X-API-Key"), "instance-key-")})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/outbound-messages/") {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": strings.TrimPrefix(r.URL.Path, "/v1/outbound-messages/"), "status": "sent", "instance_id": "inst-" + strings.TrimPrefix(r.Header.Get("X-API-Key"), "instance-key-")})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := lumi.New(srv.URL, "user-key")
	if err != nil {
		t.Fatal(err)
	}
	return c, mock
}
func attachLumi(f panelFixture, c *lumi.Client) {
	f.s.Lumi = c
	f.s.CredentialKey = strings.Repeat("master-secret-", 4)
	f.s.StalledAfter = 30 * time.Minute
}
func connectVendor(t *testing.T, f panelFixture) {
	t.Helper()
	r := panelCall(t, f, "POST", "/sellers/whatsapp", map[string]any{}, "")
	if r.Code != 202 || strings.Contains(r.Body.String(), "instance-key-") {
		t.Fatal(r.Code, r.Body.String())
	}
	r = panelCall(t, f, "GET", "/sellers/whatsapp", nil, "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"connected"`) || strings.Contains(r.Body.String(), "credential") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func salesLeadState(t *testing.T, db *store.DB, now time.Time, id string) domain.State {
	t.Helper()
	at := now.Add(-31 * time.Minute)
	st := domain.State{ID: id, Profile: domain.Form{Name: "Cliente Exemplo", Company: "Empresa Exemplo", Phone: "+5511999999100", Consent: true}, Stage: "qualifying", CreatedAt: at, LastEventAt: at, FirstContactAt: &at}
	putSalesLead(t, db, st, now)
	return st
}
func putSalesLead(t *testing.T, db *store.DB, st domain.State, now time.Time) {
	t.Helper()
	raw, _ := json.Marshal(st)
	_, err := db.Pool.Exec(context.Background(), `INSERT INTO bia_go.leads(id,state,updated_at) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET state=EXCLUDED.state,updated_at=EXCLUDED.updated_at`, st.ID, raw, now)
	if err != nil {
		t.Fatal(err)
	}
}
func countNotifications(t *testing.T, db *store.DB, status string) int {
	t.Helper()
	var n int
	err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.sales_notifications WHERE ($1='' OR status=$1)`, status).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestSalesAlertsBroadcastIdempotencyAndDelivery(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	client, mock := fakeLumi(t)
	admin := newPanel(t, db, now, "admin")
	seller := newPanel(t, db, now, "operator")
	viewer := newPanel(t, db, now, "viewer")
	attachLumi(admin, client)
	attachLumi(seller, client)
	attachLumi(viewer, client)
	if r := panelCall(t, viewer, "POST", "/sellers/whatsapp", map[string]any{}, ""); r.Code != 403 {
		t.Fatal("viewer connected", r.Code)
	}
	connectVendor(t, admin)
	connectVendor(t, seller)
	connectVendor(t, admin)
	if mock.instanceCount() != 2 {
		t.Fatal("duplicate instance", mock.instanceCount())
	}
	var cipher []byte
	if err := db.Pool.QueryRow(context.Background(), `SELECT credential FROM bia_go.seller_whatsapp WHERE user_id=$1`, seller.user.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), "instance-key-") {
		t.Fatal("plaintext in Postgres")
	}
	st := salesLeadState(t, db, now, "sales-lead")
	r := panelCall(t, admin, "POST", "/leads/"+st.ID+"/notifications/stalled", map[string]any{}, "notify-key-1")
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"queued":2`) {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, key := range []string{"notify-key-1", "notify-key-2"} {
		r = panelCall(t, admin, "POST", "/leads/"+st.ID+"/notifications/stalled", map[string]any{}, key)
		if r.Code != 202 || !strings.Contains(r.Body.String(), `"duplicate":true`) {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	salesLeadState(t, db, now, "other-lead")
	r = panelCall(t, admin, "POST", "/leads/other-lead/notifications/stalled", map[string]any{}, "notify-key-1")
	if r.Code != 409 || !strings.Contains(r.Body.String(), "idempotency_conflict") {
		t.Fatal(r.Code, r.Body.String())
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := admin.s.SalesOnce(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(mock.deliveries()) != 2 || countNotifications(t, db, "accepted") != 2 {
		t.Fatal("broadcast count", len(mock.deliveries()), countNotifications(t, db, "accepted"))
	}
	for _, message := range mock.deliveries() {
		if !strings.Contains(message, "Cliente Exemplo") || !strings.Contains(message, "+5511999999100") || !strings.Contains(message, "Primeiro contato realizado") {
			t.Fatal(message)
		}
	}
	now = now.Add(11 * time.Second)
	admin.s.Now = func() time.Time { return now }
	if _, err := admin.s.SalesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countNotifications(t, db, "sent") != 2 || len(mock.deliveries()) != 2 {
		t.Fatal("accepted was not reconciled without resend")
	}
	list := panelCall(t, admin, "GET", "/notifications?lead_id="+st.ID, nil, "")
	if list.Code != 200 || strings.Contains(list.Body.String(), "instance-key-") {
		t.Fatal(list.Code, list.Body.String())
	}
}
func TestSalesAlertsSkipBookedPausedAdvancedAndUnknownSends(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	client, mock := fakeLumi(t)
	f := newPanel(t, db, now, "admin")
	attachLumi(f, client)
	connectVendor(t, f)
	booked := salesLeadState(t, db, now, "booked")
	booked.Booking = &domain.Booking{ID: "future", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}
	putSalesLead(t, db, booked, now)
	paused := salesLeadState(t, db, now, "paused")
	paused.Paused = true
	putSalesLead(t, db, paused, now)
	fresh := salesLeadState(t, db, now, "fresh")
	fresh.LastInbound = now
	putSalesLead(t, db, fresh, now)
	for _, id := range []string{"booked", "paused", "fresh"} {
		r := panelCall(t, f, "POST", "/leads/"+id+"/notifications/stalled", map[string]any{}, "key-"+id)
		if r.Code != 409 || !strings.Contains(r.Body.String(), "lead_not_stalled") {
			t.Fatal(id, r.Code, r.Body.String())
		}
	}
	st := salesLeadState(t, db, now, "advance")
	if err := f.s.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.LastInbound = now
	putSalesLead(t, db, st, now)
	if _, err := f.s.SalesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mock.deliveries()) != 0 || countNotifications(t, db, "skipped") != 1 {
		t.Fatal("stale alert sent", len(mock.deliveries()))
	}
	unknown := salesLeadState(t, db, now, "unknown")
	r := panelCall(t, f, "POST", "/leads/"+unknown.ID+"/notifications/stalled", map[string]any{}, "unknown-key")
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	mock.setUnknown()
	for range 3 {
		if _, err := f.s.SalesOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(mock.deliveries()) != 1 || countNotifications(t, db, "uncertain") != 1 {
		t.Fatal("ambiguous send was retried", len(mock.deliveries()))
	}
}
func TestSalesAlertsNewInteractionNewEpisodeAndOptOut(t *testing.T) {
	db := setup(t)
	now := time.Now().UTC()
	client, mock := fakeLumi(t)
	f := newPanel(t, db, now, "admin")
	attachLumi(f, client)
	connectVendor(t, f)
	st := salesLeadState(t, db, now, "episode")
	if err := f.s.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SalesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mock.deliveries()) != 1 {
		t.Fatal("first episode")
	}
	st.LastInbound = now
	putSalesLead(t, db, st, now)
	if err := f.s.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countNotifications(t, db, "") != 1 {
		t.Fatal("new activity did not restart clock")
	}
	now = now.Add(31 * time.Minute)
	f.s.Now = func() time.Time { return now }
	if err := f.s.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countNotifications(t, db, "pending") != 1 {
		t.Fatal("new episode not queued")
	}
	r := panelCall(t, f, "PATCH", "/sellers/whatsapp", map[string]any{"notifications_enabled": false}, "")
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	if _, err := f.s.SalesOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countNotifications(t, db, "skipped") != 1 || len(mock.deliveries()) != 1 {
		t.Fatal("disabled vendor notified")
	}
	st.OptedOut = true
	putSalesLead(t, db, st, now)
	if err := f.s.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countNotifications(t, db, "") != 2 {
		t.Fatal("opted out lead generated another alert")
	}
	// Configuration is allowed to remain absent while the user supplies the key later.
	unconfigured := &management.Server{DB: db, Mode: "live", Policy: policy()}
	if err := unconfigured.SalesScan(context.Background()); err != nil {
		t.Fatal(err)
	}
}
