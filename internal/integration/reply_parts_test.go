package integration

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/worker"
	"context"
	"strings"
	"testing"
	"time"
)

type partsMessenger struct {
	keys, texts []string
	failAt      int
}

func (m *partsMessenger) Send(_ context.Context, key string, _ domain.State, text, _ string, _ time.Time) (providers.Receipt, error) {
	m.keys = append(m.keys, key)
	m.texts = append(m.texts, text)
	if len(m.keys) == m.failAt {
		return providers.Receipt{}, &providers.Failure{Code: "uncertain_test", Ambiguous: true}
	}
	return providers.Receipt{ID: key, Status: "sent"}, nil
}
func TestDurableReplyPartsResumeInterruptAndFailure(t *testing.T) {
	for _, scenario := range []string{"resume", "new_input", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			s := testState("parts-lead", now)
			m := &partsMessenger{}
			makeWorker := func() *worker.Worker {
				w := baseWorker(db)
				w.Live = true
				w.Messenger = m
				w.Now = func() time.Time { return now }
				return w
			}
			w := makeWorker()
			text := "Recebi sua mensagem e entendi que você quer organizar o atendimento da sua empresa para responder melhor aos contatos.\n\nPodemos conversar sobre o problema que mais toma tempo hoje e avaliar um próximo passo concreto.\n\nQual é a principal dificuldade que você enfrenta no atendimento?"
			parts := domain.SplitReply(text)
			if len(parts) != 3 {
				t.Fatal(parts)
			}
			fx := domain.Effect{ID: "reply-parent", LeadID: s.ID, Kind: "send", Purpose: "reply", Text: text, Due: now.Add(-time.Second), Expires: now.Add(time.Hour)}
			publishEffects(t, db, "publish-parts", s, fx)
			step(t, w, true) // Materialize the immutable first part without sending the full text.
			if len(m.keys) != 0 {
				t.Fatal("parent sent a message")
			}
			step(t, w, true)
			if len(m.keys) != 1 || m.texts[0] != parts[0] {
				t.Fatal(m.texts)
			}
			step(t, w, false) // Pause is persisted in the queue.
			var due time.Time
			if e := db.Pool.QueryRow(ctx, `SELECT due_at FROM bia_go.jobs WHERE id='reply-parent:part:2'`).Scan(&due); e != nil || !due.After(now) {
				t.Fatal(due, e)
			}
			ingest(t, db, fastWindowPolicy(), now)
			var actual []string
			rows, e := db.Pool.Query(ctx, `SELECT body FROM bia_go.conversation_messages WHERE role='assistant' ORDER BY seq`)
			if e != nil {
				t.Fatal(e)
			}
			for rows.Next() {
				var b string
				rows.Scan(&b)
				actual = append(actual, b)
			}
			rows.Close()
			if len(actual) != 1 || actual[0] != parts[0] {
				t.Fatal("history fabricated unsent parts", actual)
			}
			w = makeWorker() // Recreate the worker between parts.
			if scenario == "new_input" {
				submit(t, db, inbound("interrupt", s.ID, "Espere, quero outra coisa", now), "live")
			}
			if scenario == "uncertain" {
				m.failAt = 2
			}
			if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now()-interval '1 second' WHERE id='reply-parent:part:2'`); e != nil {
				t.Fatal(e)
			}
			step(t, w, true)
			if scenario == "new_input" {
				if len(m.keys) != 1 {
					t.Fatal("stale part sent")
				}
				assertJob(t, db, "reply-parent:part:2", "execution", "skipped")
			} else if scenario == "uncertain" {
				assertJob(t, db, "reply-parent:part:2", "execution", "uncertain")
			} else {
				if _, e = db.Pool.Exec(ctx, `UPDATE bia_go.jobs SET due_at=now()-interval '1 second' WHERE id='reply-parent:part:3'`); e != nil {
					t.Fatal(e)
				}
				step(t, w, true)
				step(t, w, false)
				if len(m.keys) != 3 || strings.Join(m.texts, "|") != strings.Join(parts, "|") {
					t.Fatal(m.texts)
				}
				count, _, e := db.Counts(ctx, s.ID)
				if e != nil || count != 3 {
					t.Fatal("parent counted as send", count, e)
				}
			}
			if scenario != "resume" {
				var n int
				db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE id='reply-parent:part:3'`).Scan(&n)
				if n != 0 {
					t.Fatal("published after interruption/failure")
				}
			}
		})
	}
}
