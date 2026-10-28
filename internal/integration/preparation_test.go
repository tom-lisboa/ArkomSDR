package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/followupworker"
	"arkom/bia/internal/providers"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreparationReconcilesAndSendsOnceOutsideWhatsAppWindow(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	opened := now.Add(-15*time.Minute - time.Second)
	s := testState("preparation", now)
	s.LastFormContactAt = &opened
	s.Booking = &domain.Booking{ID: "meeting", Start: now.Add(24 * time.Hour), End: now.Add(24*time.Hour + 45*time.Minute)}
	publishEffects(t, db, "seed-preparation", s)
	for range 2 {
		if err := db.ReconcilePreparation(ctx, now, true); err != nil {
			t.Fatal(err)
		}
	}
	fx := domain.PreparationSequence(s, now)[0]
	assertJob(t, db, fx.ID, "followup", "pending")
	posts := 0
	sid := "HX" + strings.Repeat("4", 32)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ApprovalRequests") {
			fmt.Fprint(w, `{"whatsapp":{"status":"approved"}}`)
			return
		}
		if r.Method == "POST" {
			posts++
			r.ParseForm()
			var vars map[string]string
			json.Unmarshal([]byte(r.Form.Get("ContentVariables")), &vars)
			if r.Form.Get("ContentSid") != sid || r.Form.Has("Body") || vars["1"] != "Teste" {
				t.Error("bad preparation template", r.Form)
			}
		}
		fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "SM"+strings.Repeat("5", 32))
	}))
	defer mock.Close()
	w := runtimeWorker(db, now, &countingMessenger{})
	w.Policy.DisableFollowups = true
	w.Policy.MeetingPreparation = true
	w.Messenger = &providers.TwilioMessaging{Pool: db.Pool, Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, PreparationContentSID: sid}
	followup := followupworker.New(*w)
	step(t, followup, true)
	assertJob(t, db, fx.ID, "followup", "done")
	if err := db.ReconcilePreparation(ctx, now, true); err != nil {
		t.Fatal(err)
	}
	step(t, followupworker.New(*w), false)
	current, err := db.Lead(ctx, s.ID)
	if err != nil || current.LastPreparationAt == nil {
		t.Fatal("missing persisted preparation receipt", err)
	}
	current.Booking.Start = current.Booking.Start.Add(time.Hour)
	if len(domain.PreparationSequence(current, now)) != 0 {
		t.Fatal("reschedule repeated preparation")
	}
	if posts != 1 {
		t.Fatal("replayed preparation", posts)
	}
}
