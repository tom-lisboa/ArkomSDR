package voiceworker

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"context"
	"crypto/hmac"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Messages                              *providers.TwilioMessaging
	Mode, TimeZone, TestPhone             string
	Repo                                  Repository
	Provider                              Provider
	Bridge                                *Bridge
	Account, Sender, AuthToken, PublicURL string
	Enabled                               bool
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","role":"voice"}`))
	})
	m.HandleFunc("POST /voice/permission", s.permission)
	m.HandleFunc("POST /voice/messages", s.messages)
	m.HandleFunc("POST /voice/incoming", s.incoming)
	m.HandleFunc("POST /voice/status/{id}", s.status)
	m.HandleFunc("POST /voice/twiml/{id}", s.twiml)
	m.HandleFunc("GET /voice/stream", s.stream)
	return m
}
func (s *Server) verified(w http.ResponseWriter, r *http.Request) bool {
	if !s.Enabled {
		http.Error(w, "voice disabled", 503)
		return false
	}
	return s.verifiedSignature(w, r)
}

// Incoming calls do not have the request/consent state required by the outgoing
// bridge. End them explicitly; never route them through an arbitrary request ID.
// This endpoint stays available while outbound voice is disabled.
func (s *Server) incoming(w http.ResponseWriter, r *http.Request) {
	if !s.verifiedSignature(w, r) {
		return
	}
	if s.Sender == "" || r.PostForm.Get("To") != "whatsapp:"+s.Sender {
		http.Error(w, "invalid recipient", 403)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte("<Response><Hangup/></Response>"))
}

func (s *Server) verifiedSignature(w http.ResponseWriter, r *http.Request) bool {
	if s.AuthToken == "" || s.Account == "" || s.PublicURL == "" {
		http.Error(w, "voice endpoint not configured", 503)
		return false
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "unexpected query", 400)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if e := r.ParseForm(); e != nil {
		http.Error(w, "invalid form", 400)
		return false
	}
	got := r.Header.Get("X-Twilio-Signature")
	absolute := s.PublicURL + r.URL.EscapedPath()
	valid := hmac.Equal([]byte(got), []byte(Signature(s.AuthToken, absolute, r.PostForm)))
	if r.Method == "GET" {
		valid = valid || hmac.Equal([]byte(got), []byte(Signature(s.AuthToken, strings.Replace(absolute, "https://", "wss://", 1), nil)))
	}
	if !valid {
		http.Error(w, "invalid signature", 403)
		return false
	}
	if r.Method == "POST" && r.PostForm.Get("AccountSid") != s.Account {
		http.Error(w, "invalid account", 403)
		return false
	}
	return true
}
func (s *Server) permission(w http.ResponseWriter, r *http.Request) {
	if !s.verified(w, r) {
		return
	}
	f := r.PostForm
	// The existing messaging router must forward this specific native event here.
	if f.Get("Body") != "VOICE_CALL_REQUEST" || f.Get("To") != "whatsapp:"+s.Sender {
		http.Error(w, "unsupported callback", 400)
		return
	}
	value := f.Get("ButtonPayload")
	if value != "ACCEPTED" && value != "REJECTED" {
		http.Error(w, "invalid permission", 400)
		return
	}
	phone, e := domain.NormalizePhone(strings.TrimPrefix(f.Get("From"), "whatsapp:"))
	if e != nil {
		http.Error(w, "invalid sender", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	at, e := s.Provider.MessageTime(ctx, f.Get("MessageSid"), f.Get("From"), f.Get("To"))
	if e != nil || at.After(time.Now().Add(time.Minute)) {
		http.Error(w, "permission verification unavailable", 503)
		return
	}
	if e = s.Repo.Permission(ctx, f.Get("MessageSid"), phone, s.Sender, value == "ACCEPTED", f.Get("CallPermissionIsPermanent") == "true", at); e != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte("<Response/>"))
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if !s.verified(w, r) {
		return
	}
	f := r.PostForm
	req, e := s.Repo.Get(r.Context(), r.PathValue("id"))
	if e != nil || req.Sender != s.Sender || !sid(f.Get("CallSid"), "CA") {
		http.Error(w, "unknown call", 404)
		return
	}
	seq, e := strconv.ParseInt(f.Get("SequenceNumber"), 10, 64)
	if e != nil || seq < 0 {
		http.Error(w, "invalid sequence", 400)
		return
	}
	if e = s.Repo.Status(r.Context(), req.ID, f.Get("CallSid"), f.Get("CallStatus"), seq); e != nil {
		http.Error(w, "invalid callback", 400)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) twiml(w http.ResponseWriter, r *http.Request) {
	if !s.verified(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	req, e := s.Repo.Get(r.Context(), r.PathValue("id"))
	if e != nil || !sid(r.PostForm.Get("CallSid"), "CA") {
		_, _ = w.Write([]byte("<Response><Hangup/></Response>"))
		return
	}
	st, e := s.Repo.Active(r.Context(), req)
	if e != nil || r.PostForm.Get("From") != "whatsapp:"+req.Sender || r.PostForm.Get("To") != "whatsapp:"+st.Profile.Phone || (req.CallSID != "" && req.CallSID != r.PostForm.Get("CallSid")) {
		_, _ = w.Write([]byte("<Response><Hangup/></Response>"))
		return
	}
	_, _ = w.Write([]byte(streamXML(s.PublicURL, s.AuthToken, req.ID, r.PostForm.Get("CallSid"))))
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if !s.verified(w, r) || s.Bridge == nil {
		return
	}
	conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if e != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 << 10)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	var start wireEvent
	for i := 0; i < 2; i++ {
		_, raw, e := conn.Read(ctx)
		if e != nil {
			cancel()
			return
		}
		if json.Unmarshal(raw, &start) != nil {
			cancel()
			return
		}
		if start.Event == "start" {
			break
		}
		if start.Event != "connected" {
			cancel()
			return
		}
	}
	cancel()
	req, e := s.Repo.Get(r.Context(), start.Start.CustomParameters["request_id"])
	if e != nil || start.Event != "start" || start.Start.AccountSID != s.Account || req.Sender != s.Sender || !sid(start.Start.CallSID, "CA") || start.Start.StreamSID == "" || start.Start.MediaFormat.Encoding != "audio/x-mulaw" || start.Start.MediaFormat.SampleRate != 8000 || start.Start.MediaFormat.Channels != 1 {
		return
	}
	if !hmac.Equal([]byte(start.Start.CustomParameters["ticket"]), []byte(ticket(s.AuthToken, req.ID, start.Start.CallSID))) {
		return
	}
	if _, e = s.Repo.Active(r.Context(), req); e != nil {
		return
	}
	if e = s.Repo.BeginStream(r.Context(), req, start.Start.CallSID); e != nil {
		return
	}
	req.CallSID = start.Start.CallSID
	_ = s.Bridge.Run(r.Context(), conn, req, start.Start.StreamSID)
}
