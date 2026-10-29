package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/casemedia"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/mediaworker"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/worker"
)

type videoHarness struct {
	db            *store.DB
	w             *worker.Worker
	api           *httpapi.Server
	asset         casemedia.Asset
	openingStatus string
	approved      bool
	videoFails    bool
	insideSession bool
	openingPosts  int
	videoPosts    int
}

func introVideoHarness(t *testing.T) *videoHarness {
	t.Helper()
	h := &videoHarness{db: setup(t), openingStatus: "sent", approved: true}
	data := append([]byte("\x00\x00\x00\x18ftypisom"), bytes.Repeat([]byte{0}, 100)...)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hash+".mp4"), data, 0600); err != nil {
		t.Fatal(err)
	}
	h.asset = casemedia.Asset{Directory: dir, SHA256: hash, PublicURL: "https://media.example"}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ApprovalRequests") {
			status := "approved"
			if !h.approved && strings.Contains(r.URL.Path, strings.Repeat("3", 32)) {
				status = "pending"
			}
			fmt.Fprintf(w, `{"whatsapp":{"status":%q}}`, status)
			return
		}
		if r.Method == "GET" {
			sid := strings.TrimSuffix(filepath.Base(r.URL.Path), ".json")
			fmt.Fprintf(w, `{"sid":%q,"status":%q,"from":%q,"to":"whatsapp:+5565999999900"}`, sid, h.openingStatus, "whatsapp:"+voiceSender)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("ContentSid") == "HX"+strings.Repeat("2", 32) {
			h.openingPosts++
			fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, fmt.Sprintf("SM%032x", h.openingPosts))
			return
		}
		h.videoPosts++
		if h.insideSession {
			if r.Form.Get("MediaUrl") != h.asset.URL() || r.Form.Has("ContentSid") || r.Form.Has("Body") {
				t.Error("video inside the service window must use only MediaUrl", r.Form)
			}
		} else if r.Form.Get("ContentSid") != "HX"+strings.Repeat("3", 32) || r.Form.Has("MediaUrl") || r.Form.Has("Body") {
			t.Error("video outside the service window must use only its approved template", r.Form)
		}
		if h.videoFails {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"code":20500}`)
			return
		}
		fmt.Fprintf(w, `{"sid":%q,"status":"sent"}`, "MM"+strings.Repeat("4", 32))
	}))
	t.Cleanup(mock.Close)
	tw := &providers.TwilioMessaging{Pool: h.db.Pool, Account: "AC" + strings.Repeat("5", 32), AuthToken: "test", Sender: voiceSender, Client: mock.Client(), BaseURL: mock.URL, ContentBaseURL: mock.URL, FormContentSID: "HX" + strings.Repeat("2", 32), IntroVideoContentSID: "HX" + strings.Repeat("3", 32)}
	h.w = runtimeWorker(h.db, time.Now(), &countingMessenger{})
	h.w.Now = nil
	h.w.Messenger, h.w.VideoSender = tw, tw
	h.w.IntroVideoURL = h.asset.URL()
	h.api = &httpapi.Server{DB: h.db, FormAutoContact: true, Mode: "live", VoiceSender: voiceSender, Policy: policy(), TestFormToken: strings.Repeat("v", 48)}
	return h
}

func (h *videoHarness) submit(t *testing.T, id string) string {
	t.Helper()
	body := `{"name":"Ana","company":"Exemplo","phone":"+5565999999900","goal":"Melhorar o atendimento","consent":true}`
	r := demoCall(h.api.Handler(), "POST", "/webhooks/test-form/"+h.api.TestFormToken, id, body)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	drain(t, h.w)
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE bia_go.jobs SET due_at=now() WHERE status='pending' AND payload->>'purpose'='form_contact'`); err != nil {
		t.Fatal(err)
	}
	drain(t, h.w)
	return domain.LeadKey("+5565999999900")
}

func (h *videoHarness) mediaStep(t *testing.T) {
	t.Helper()
	if _, err := h.db.Pool.Exec(context.Background(), `UPDATE bia_go.jobs SET due_at=now() WHERE status='pending' AND payload->>'purpose'='intro_video'`); err != nil {
		t.Fatal(err)
	}
	step(t, mediaworker.Outbound(*h.w, h.api.ReminderAudio, h.asset), true)
}

func TestIntroVideoWaitsForOpeningDeliveryAndSendsOnlyOnce(t *testing.T) {
	h := introVideoHarness(t)
	lead := h.submit(t, "video-form-1")
	id := "fx/intro-video:" + lead
	assertJob(t, h.db, id, "media", "pending")
	if h.openingPosts != 1 || h.videoPosts != 0 {
		t.Fatal("case sent before opening", h.openingPosts, h.videoPosts)
	}
	h.mediaStep(t)
	assertJob(t, h.db, id, "media", "pending")
	if h.videoPosts != 0 {
		t.Fatal("case sent before opening delivery")
	}
	h.openingStatus = "delivered"
	h.approved = false
	h.mediaStep(t)
	assertJob(t, h.db, id, "media", "pending")
	if h.videoPosts != 0 {
		t.Fatal("unapproved video template sent")
	}
	h.approved = true
	h.mediaStep(t)
	assertJob(t, h.db, id, "media", "done")
	if h.videoPosts != 1 {
		t.Fatal("video missing", h.videoPosts)
	}
	s, err := h.db.Lead(context.Background(), lead)
	if err != nil || s.Human || !s.LastInbound.IsZero() {
		t.Fatal("video changed contact control or fabricated an inbound window", s, err)
	}
	past := time.Now().Add(-16 * time.Minute)
	s.LastFormContactAt = &past
	publishEffects(t, h.db, "older-opening", s)
	h.submit(t, "video-form-2")
	if h.openingPosts != 2 || h.videoPosts != 1 {
		t.Fatal("repeated form repeated case", h.openingPosts, h.videoPosts)
	}
	s, err = h.db.Lead(context.Background(), lead)
	if err != nil || s.Human {
		t.Fatal("repeated form failed", s, err)
	}
	var count int
	if err := h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM bia_go.jobs WHERE payload->>'purpose'='intro_video'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("case is not idempotent per contact", count, err)
	}
}

func TestIntroVideoInsideSessionDoesNotNeedTemplate(t *testing.T) {
	h := introVideoHarness(t)
	lead := h.submit(t, "video-in-session")
	h.openingStatus, h.approved, h.insideSession = "read", false, true
	s, err := h.db.Lead(context.Background(), lead)
	if err != nil {
		t.Fatal(err)
	}
	s.LastInbound = time.Now().Add(-time.Minute)
	publishEffects(t, h.db, "inbound-before-video", s)
	h.mediaStep(t)
	assertJob(t, h.db, "fx/intro-video:"+lead, "media", "done")
	if h.videoPosts != 1 {
		t.Fatal("video missing inside session", h.videoPosts)
	}
}

func TestIntroVideoFailureAndContactControlDoNotSendOrBlockConversation(t *testing.T) {
	for _, reason := range []string{"optout", "pause", "human", "opening_failed", "video_uncertain"} {
		t.Run(reason, func(t *testing.T) {
			h := introVideoHarness(t)
			lead := h.submit(t, "video-form")
			h.openingStatus = "delivered"
			s, err := h.db.Lead(context.Background(), lead)
			if err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "optout":
				s.OptedOut = true
			case "pause":
				s.Paused = true
			case "human":
				s.Human = true
			case "opening_failed":
				h.openingStatus = "failed"
			case "video_uncertain":
				h.videoFails = true
			}
			raw, _ := json.Marshal(s)
			if _, err := h.db.Pool.Exec(context.Background(), `UPDATE bia_go.leads SET state=$2 WHERE id=$1`, lead, raw); err != nil {
				t.Fatal(err)
			}
			h.mediaStep(t)
			if reason == "video_uncertain" {
				assertJob(t, h.db, "fx/intro-video:"+lead, "media", "uncertain")
				step(t, mediaworker.Outbound(*h.w, h.api.ReminderAudio, h.asset), false)
				if h.videoPosts != 1 {
					t.Fatal("uncertain video repeated", h.videoPosts)
				}
			} else if h.videoPosts != 0 {
				t.Fatal("blocked video sent", reason)
			}
			current, err := h.db.Lead(context.Background(), lead)
			if err != nil || (reason != "human" && current.Human) {
				t.Fatal("optional video took over conversation", reason, current, err)
			}
		})
	}
}
