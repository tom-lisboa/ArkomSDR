package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/httpapi"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/voiceworker"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReminderDoesNotDialCancelledOrChangedMeeting(t *testing.T) {
	for _, change := range []string{"cancelled", "rescheduled"} {
		t.Run(change, func(t *testing.T) {
			db, repo, w, p, s := voiceFixture(t, "live")
			step(t, w, true)
			approveVoice(t, repo, s)
			if change == "cancelled" {
				if e := (&providers.LocalCalendar{Pool: db.Pool}).Cancel(context.Background(), "cancel", *s.Booking); e != nil {
					t.Fatal(e)
				}
			} else {
				s.Booking.Start = s.Booking.Start.Add(time.Hour)
				publishEffects(t, db, "changed-meeting", s)
			}
			step(t, w, true)
			voiceState(t, repo, "skipped")
			if p.dials != 0 {
				t.Fatal("obsolete reminder dialed")
			}
		})
	}
}
func TestReminderRequiresBookingAndAllowsOnlyConfirm(t *testing.T) {
	db, repo, _, _, s := voiceFixture(t, "live")
	if _, e := repo.Create(context.Background(), "new-schedule", s.ID, "live", voiceSender, "schedule"); e == nil {
		t.Fatal("schedule call accepted")
	}
	s.Booking = nil
	publishEffects(t, db, "no-meeting", s)
	if _, e := repo.Create(context.Background(), "new-reminder", s.ID, "live", voiceSender, "confirm"); e == nil {
		t.Fatal("reminder without booking accepted")
	}
}
func TestSharedContextAPIAndBriefingScope(t *testing.T) {
	db, _, vw, _, s := voiceFixture(t, "live")
	token := strings.Repeat("a", 32)
	h := (&httpapi.Server{DB: db, Token: token, Policy: policy()}).Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	res := call("GET", "/v1/leads/"+s.ID+"/context", "")
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
	var c domain.Customer
	if e := json.Unmarshal(res.Body.Bytes(), &c); e != nil || c.Company != "Empresa Teste" || c.Name != s.Profile.Name || c.Revenue == nil || *c.Revenue != 0 || !c.MeetingDate.Equal(s.Booking.Start) {
		t.Fatal(c, e)
	}
	briefing := `{"consultor":"Ana","consultor_genero":"ela","pauta":"Operação comercial","contexto":"Quer reduzir trabalho manual"}`
	res = call("PUT", "/v1/leads/wrong-lead/bookings/"+s.Booking.ID+"/briefing", briefing)
	if res.Code != 404 {
		t.Fatal("cross lead write", res.Code)
	}
	res = call("PUT", "/v1/leads/"+s.ID+"/bookings/"+s.Booking.ID+"/briefing", briefing)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body)
	}
	m, e := vw.Agenda.Lookup(context.Background(), "voice-one")
	if e != nil || m.Briefing.Consultant != "Ana" || m.Briefing.Agenda != "Operação comercial" {
		t.Fatal(m, e)
	}
}
func TestReminderHangupWaitsForPlayback(t *testing.T) {
	for _, tool := range []string{"end_call", "voicemail_detection"} {
		t.Run(tool, func(t *testing.T) {
			_, repo, vw, provider, s := voiceFixture(t, "live")
			step(t, vw, true)
			approveVoice(t, repo, s)
			step(t, vw, true)
			r := voiceState(t, repo, "queued")
			ctx, stop := context.WithTimeout(context.Background(), 6*time.Second)
			defer stop()
			ai := &fakeAudio{in: make(chan json.RawMessage, 32), out: make(chan json.RawMessage, 32)}
			b := &voiceworker.Bridge{Repo: repo, Provider: provider, Connect: func(context.Context) (voiceworker.AudioConnection, error) { return ai, nil }, Tools: voiceworker.Tools{Repo: repo, Agenda: vw.Agenda, Policy: vw.Policy}, Model: "test", MaxDuration: 5 * time.Second}
			finished := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				c, e := websocket.Accept(w, req, nil)
				if e != nil {
					finished <- e
					return
				}
				defer c.CloseNow()
				finished <- b.Run(ctx, c, r, "test-stream")
			}))
			defer server.Close()
			client, _, e := websocket.Dial(ctx, server.URL, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer client.CloseNow()
			emit := func(v any) {
				raw, _ := json.Marshal(v)
				select {
				case ai.in <- raw:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			recv := func(want string) map[string]any {
				for {
					select {
					case raw := <-ai.out:
						var v map[string]any
						json.Unmarshal(raw, &v)
						if v["type"] == want {
							return v
						}
					case <-ctx.Done():
						t.Fatal("missing", want)
						return nil
					}
				}
			}
			config := recv("session.update")["session"].(map[string]any)
			if !strings.Contains(config["instructions"].(string), `"nome_empresa":"Empresa Teste"`) {
				t.Fatal("voice missing form")
			}
			emit(map[string]any{"type": "session.updated"})
			recv("response.create")
			emit(map[string]any{"type": "response.created"})
			if tool == "voicemail_detection" {
				emit(map[string]any{"type": "response.function_call_arguments.done", "name": tool, "call_id": "call-1", "arguments": "{}"})
				emit(map[string]any{"type": "response.done"})
				next := recv("response.create")["response"].(map[string]any)
				if next["tool_choice"] != "none" || strings.Contains(next["instructions"].(string), s.Profile.Company) {
					t.Fatal("unsafe voicemail", next)
				}
				emit(map[string]any{"type": "response.created"})
			}
			emit(map[string]any{"type": "response.output_audio.delta", "item_id": "bye", "delta": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 800))})
			emit(map[string]any{"type": "response.output_audio_transcript.done", "item_id": "bye", "transcript": "Até a reunião!"})
			emit(map[string]any{"type": "response.output_audio.done", "item_id": "bye"})
			if tool == "end_call" {
				emit(map[string]any{"type": "response.function_call_arguments.done", "name": tool, "call_id": "call-1", "arguments": "{}"})
			}
			emit(map[string]any{"type": "response.done"})
			var mark string
			for mark == "" {
				_, raw, e := client.Read(ctx)
				if e != nil {
					t.Fatal(e)
				}
				var v map[string]any
				json.Unmarshal(raw, &v)
				if v["event"] == "mark" {
					mark = v["mark"].(map[string]any)["name"].(string)
				}
			}
			select {
			case e := <-finished:
				t.Fatal("hung up before audio receipt", e)
			case <-time.After(100 * time.Millisecond):
			}
			raw, _ := json.Marshal(map[string]any{"event": "mark", "streamSid": "test-stream", "mark": map[string]string{"name": mark}})
			if e := client.Write(ctx, websocket.MessageText, raw); e != nil {
				t.Fatal(e)
			}
			select {
			case e := <-finished:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("did not end after playback")
			}
			if provider.hangups != 1 {
				t.Fatal("hangup count", provider.hangups)
			}
		})
	}
}
