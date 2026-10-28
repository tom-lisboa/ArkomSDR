package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arkom/bia/internal/contextworker"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
	"github.com/coder/websocket"
)

type fakeAudio struct{ in, out chan json.RawMessage }

func (a *fakeAudio) SendRaw(ctx context.Context, v json.RawMessage) error {
	select {
	case a.out <- v:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *fakeAudio) RecvRaw(ctx context.Context) (json.RawMessage, error) {
	select {
	case v := <-a.in:
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (a *fakeAudio) Close() error { return nil }
func TestVoiceAudioPlaybackInterruptionAndOptOut(t *testing.T) {
	db, repo, vw, provider, s := voiceFixture(t, "live")
	step(t, vw, true)
	approveVoice(t, repo, s)
	step(t, vw, true)
	r := voiceState(t, repo, "queued")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ai := &fakeAudio{in: make(chan json.RawMessage, 32), out: make(chan json.RawMessage, 32)}
	b := &voiceworker.Bridge{Repo: repo, Provider: provider, Connect: func(context.Context) (voiceworker.AudioConnection, error) { return ai, nil }, Tools: voiceworker.Tools{Repo: repo, Agenda: vw.Agenda, Policy: vw.Policy}, Model: "test-model", Voice: "marin", TranscriptionModel: "test-transcription", Brand: "Exemplo", MaxDuration: 7 * time.Second}
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, e := websocket.Accept(w, req, nil)
		if e != nil {
			finished <- e
			return
		}
		defer c.CloseNow()
		finished <- b.Run(ctx, c, r, "stream-test")
	}))
	defer server.Close()
	client, _, e := websocket.Dial(ctx, server.URL, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.CloseNow()
	emit := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		select {
		case ai.in <- raw:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	send := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if e := client.Write(ctx, websocket.MessageText, raw); e != nil {
			t.Fatal(e)
		}
	}
	receiveAI := func(want string) map[string]any {
		t.Helper()
		for {
			select {
			case raw := <-ai.out:
				var v map[string]any
				if e := json.Unmarshal(raw, &v); e != nil {
					t.Fatal(e)
				}
				if v["type"] == want {
					return v
				}
			case <-ctx.Done():
				t.Fatal("waiting for AI event", want)
				return nil
			}
		}
	}
	receiveTW := func(want string) map[string]any {
		t.Helper()
		for {
			_, raw, e := client.Read(ctx)
			if e != nil {
				t.Fatal(e)
			}
			var v map[string]any
			if e = json.Unmarshal(raw, &v); e != nil {
				t.Fatal(e)
			}
			if v["event"] == want {
				return v
			}
		}
	}
	receiveAI("session.update")
	audio := base64.StdEncoding.EncodeToString(make([]byte, 160))
	send(map[string]any{"event": "media", "streamSid": "stream-test", "media": map[string]string{"payload": audio, "track": "inbound"}})
	emit(map[string]string{"type": "session.updated"})
	receiveAI("input_audio_buffer.append")
	emit(map[string]string{"type": "response.output_audio.delta", "item_id": "heard", "delta": audio})
	mark := receiveTW("mark")
	emit(map[string]string{"type": "response.output_audio_transcript.done", "item_id": "heard", "transcript": "Sou a Bia, assistente de IA."})
	emit(map[string]string{"type": "response.output_audio.done", "item_id": "heard"})
	send(mark)
	// A second phrase is interrupted before its playback mark is acknowledged.
	emit(map[string]string{"type": "response.output_audio.delta", "item_id": "discarded", "delta": audio})
	discardedMark := receiveTW("mark")
	emit(map[string]string{"type": "response.output_audio_transcript.done", "item_id": "discarded", "transcript": "Esta frase não foi ouvida."})
	emit(map[string]string{"type": "response.output_audio.done", "item_id": "discarded"})
	// Wait for durable played transcript before creating an interruption.
	for {
		var count int
		if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.voice_transcripts WHERE role='assistant'`).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count == 1 {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("heard transcript missing")
		}
	}
	emit(map[string]string{"type": "input_audio_buffer.speech_started"})
	trunc := receiveAI("conversation.item.truncate")
	if trunc["item_id"] != "discarded" || trunc["audio_end_ms"] != float64(0) {
		t.Fatal(trunc)
	}
	receiveTW("clear")
	send(discardedMark) // Twilio also echoes marks for cleared, unheard audio.
	emit(map[string]string{"type": "conversation.item.input_audio_transcription.completed", "item_id": "stop", "transcript": "não me ligue"})
	select {
	case e = <-finished:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("call did not stop")
	}
	var count int
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.voice_transcripts WHERE role='assistant'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("unheard audio stored", count, e)
	}
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE payload->>'kind'='contact.opt_out'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("optout not published", count, e)
	}
	cw := &contextworker.Worker{DB: db, Policy: store.DefaultWindowPolicy()}
	drainRole(t, cw)
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.conversation_messages WHERE run_id=$1`, "voice/"+r.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("voice history not persisted by context", count, e)
	}
	if e = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.conversation_runs`).Scan(&count); e != nil || count != 0 {
		t.Fatal("voice transcript triggered text run", count, e)
	}

	if provider.hangups != 1 {
		t.Fatal("call not terminated", provider.hangups)
	}
}
