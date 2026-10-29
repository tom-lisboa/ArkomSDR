package integration

import (
	"context"
	"testing"
	"time"

	"arkom/bia/internal/agent"
	"arkom/bia/internal/domain"
)

type dialogueRunner struct {
	run func(domain.State, domain.Event) agent.Outcome
}

func (f dialogueRunner) Plan(context.Context, domain.State, domain.Event, []domain.Slot) (domain.Decision, error) {
	panic("must use Runner")
}
func (f dialogueRunner) Run(_ context.Context, s domain.State, e domain.Event, _ []domain.Slot, _ string) (agent.Outcome, error) {
	return f.run(s, e), nil
}

func TestDialogueMemoryCommitsOnlyActualDeliveryAndContextOwnsHead(t *testing.T) {
	db := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	m := &countingMessenger{}
	w := runtimeWorker(db, now, m)
	p := fastWindowPolicy()
	s := testState("dialogue-delivery", now)
	publishEffects(t, db, "seed-dialogue", s)
	w.Planner = dialogueRunner{run: func(s domain.State, e domain.Event) agent.Outcome {
		a := domain.DialogueAssessment{Intent: "objection", Objection: "fit", Progress: "open", Source: "decisions"}
		return agent.Outcome{Agent: "objections", PromptVersion: agent.SpecialistPromptVersion, ResponseID: "resp_fit", Dialogue: domain.AdvanceDialogue(s, e, a, "objections", agent.SpecialistPromptVersion, now), Decision: domain.Decision{Action: "reply", Message: "O que te fez sentir isso?"}}
	}}
	submit(t, db, inbound("question-dialogue", s.ID, "acho que a Arkom não é para mim", now), "live")
	ingest(t, db, p, now)
	ev := frozen(t, db, p, now.Add(2*time.Second))
	step(t, w, true)
	current, err := db.Lead(ctx, s.ID)
	if err != nil || current.Dialogue == nil || len(current.Dialogue.Objection.Attempts) != 0 {
		t.Fatal("draft changed delivered attempts", current.Dialogue, err)
	}
	step(t, w, true)
	current, err = db.Lead(ctx, s.ID)
	if err != nil || len(current.Dialogue.Objection.Attempts) != 1 || current.Dialogue.LastQuestion != "O que te fez sentir isso?" {
		t.Fatal("delivery missing from shared memory", current.Dialogue, err)
	}
	ingest(t, db, p, now.Add(3*time.Second))
	var version string
	err = db.Pool.QueryRow(ctx, `SELECT heads->'objections'->>'prompt_version' FROM bia_go.conversations WHERE id=$1`, ev.Context.ConversationID).Scan(&version)
	if err != nil || version != agent.SpecialistPromptVersion {
		t.Fatal("context lost prompt version", version, err)
	}
}

func TestNewInboundDiscardsSpecialistAssessmentAndDraft(t *testing.T) {
	for _, ingested := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending_inbox", true: "new_context_version"}[ingested], func(t *testing.T) {
			db := setup(t)
			ctx := context.Background()
			now := time.Now().UTC()
			p := fastWindowPolicy()
			m := &countingMessenger{}
			w := runtimeWorker(db, now, m)
			s := testState("stale-dialogue", now)
			publishEffects(t, db, "seed-stale", s)
			w.Planner = dialogueRunner{run: func(s domain.State, e domain.Event) agent.Outcome {
				_, err := db.SubmitInbound(ctx, inbound("new-fragment", s.ID, "quero ver os horários", now.Add(time.Second)), "live")
				if err != nil {
					t.Fatal(err)
				}
				if ingested {
					ingest(t, db, p, now.Add(time.Second))
				}
				a := domain.DialogueAssessment{Intent: "objection", Objection: "fit", Progress: "open", Source: "decisions"}
				return agent.Outcome{Agent: "objections", PromptVersion: agent.SpecialistPromptVersion, ResponseID: "resp_stale", Dialogue: domain.AdvanceDialogue(s, e, a, "objections", agent.SpecialistPromptVersion, now), Decision: domain.Decision{Action: "reply", Message: "O que te fez sentir isso?"}}
			}}
			submit(t, db, inbound("old-fragment", s.ID, "acho que não serve", now), "live")
			ingest(t, db, p, now)
			ev := frozen(t, db, p, now.Add(2*time.Second))
			step(t, w, true)
			assertJob(t, db, ev.ID, "execution", "skipped")
			current, err := db.Lead(ctx, s.ID)
			if err != nil || current.Dialogue != nil || current.Human {
				t.Fatal("stale commercial state committed", current, err)
			}
			var drafts int
			if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM bia_go.jobs WHERE kind='effect' AND payload->>'purpose'='reply'`).Scan(&drafts); err != nil || drafts != 0 {
				t.Fatal("stale draft published", drafts, err)
			}
		})
	}
}
