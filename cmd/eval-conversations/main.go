// eval-conversations runs synthetic text journeys against the actual planner.
// It has no transport, database or external calendar; effects run in memory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"arkom/bia/internal/agent"
	"arkom/bia/internal/conversation"
	"arkom/bia/internal/domain"
)

type turn struct {
	Text        string   `json:"text"`
	Expect      string   `json:"expect"`
	Agent       string   `json:"agent,omitempty"`
	ContainsAny []string `json:"contains_any,omitempty"`
	Forbidden   []string `json:"forbidden,omitempty"`
	RequireCTA  bool     `json:"require_cta,omitempty"`
}
type scenario struct {
	ID       string  `json:"id"`
	Category string  `json:"category"`
	Seed     string  `json:"seed,omitempty"`
	Calendar string  `json:"calendar,omitempty"`
	Goal     *string `json:"goal,omitempty"`
	Zone     string  `json:"zone,omitempty"`
	Opening  string  `json:"opening,omitempty"`
	Turns    []turn  `json:"turns"`
}
type result struct {
	Case         string                     `json:"case"`
	Category     string                     `json:"category"`
	Repeat       int                        `json:"repeat"`
	Turn         int                        `json:"turn"`
	Input        string                     `json:"input"`
	Expected     string                     `json:"expected"`
	Agent        string                     `json:"agent"`
	Assessment   *domain.DialogueAssessment `json:"assessment,omitempty"`
	Decision     domain.Decision            `json:"decision"`
	Replies      []string                   `json:"replies"`
	UsedPrevious bool                       `json:"used_previous"`
	LatencyMS    int64                      `json:"latency_ms"`
	Failures     []string                   `json:"failures"`
	Pass         bool                       `json:"pass"`
}

func main() {
	dataset := flag.String("dataset", "evals/conversations.json", "Synthetic scenarios")
	outdir := flag.String("out", ".local/conversation-eval", "Output directory (transcripts stay local)")
	filter := flag.String("case", "", "Comma-separated case IDs; empty means all")
	workers := flag.Int("workers", 2, "Concurrent scenarios, 1 to 3")
	repeats := flag.Int("repeat", 1, "Complete repetitions, 1 to 3")
	flag.Parse()
	if *workers < 1 || *workers > 3 || *repeats < 1 || *repeats > 3 {
		panic("workers and repeat must be 1..3")
	}
	key, model := os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_MODEL")
	if key == "" || model == "" {
		fmt.Fprintln(os.Stderr, "Set OPENAI_API_KEY and OPENAI_MODEL in the environment")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*dataset)
	if err != nil {
		panic(err)
	}
	var cases []scenario
	if err = json.Unmarshal(raw, &cases); err != nil {
		panic(err)
	}
	selected := map[string]bool{}
	for _, id := range strings.Split(*filter, ",") {
		selected[id] = true
	}
	seen := map[string]bool{}
	n := 0
	for _, c := range cases {
		if c.ID == "" || seen[c.ID] || len(c.Turns) == 0 {
			panic("invalid/duplicate scenario")
		}
		seen[c.ID] = true
		if *filter == "" || selected[c.ID] {
			n++
		}
	}
	if n == 0 {
		panic("no selected cases")
	}
	if err = os.MkdirAll(*outdir, 0700); err != nil {
		panic(err)
	}
	f, err := os.OpenFile(filepath.Join(*outdir, "turns.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	a := agent.New(key, model, "Arkom", "")
	a.Specialists = true
	a.Classifier = agent.DecisionsClassifier{Client: a.Client, Model: "gpt-6-luna"}
	type job struct {
		c      scenario
		repeat int
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	all := []result{}
	for range *workers {
		wg.Go(func() {
			for j := range jobs {
				rows := run(a, j.c, j.repeat)
				mu.Lock()
				for _, r := range rows {
					if err := json.NewEncoder(f).Encode(r); err != nil {
						panic(err)
					}
					all = append(all, r)
				}
				_ = f.Sync()
				failed := 0
				for _, r := range rows {
					if !r.Pass {
						failed++
					}
				}
				fmt.Printf("case=%s repeat=%d turns=%d failures=%d\n", j.c.ID, j.repeat, len(rows), failed)
				mu.Unlock()
			}
		})
	}
	for repeat := 1; repeat <= *repeats; repeat++ {
		for _, c := range cases {
			if *filter == "" || selected[c.ID] {
				jobs <- job{c, repeat}
			}
		}
	}
	close(jobs)
	wg.Wait()
	failed, fallback, head := 0, 0, 0
	failedCases := map[string]bool{}
	for _, r := range all {
		if !r.Pass {
			failed++
			failedCases[r.Case] = true
		}
		if r.Assessment != nil && r.Assessment.Source != "decisions" {
			fallback++
		}
		if r.UsedPrevious {
			head++
		}
	}
	summary := map[string]any{"synthetic": true, "model": model, "decisions_model": "gpt-6-luna", "prompt_version": agent.SpecialistPromptVersion, "scenarios": n, "repeats": *repeats, "turns": len(all), "failed_turns": failed, "failed_cases": failedCases, "classifier_fallback_turns": fallback, "previous_response_turns": head, "transport": "none", "calendar": "in_memory", "database": "none", "at": time.Now().UTC()}
	b, _ := json.MarshalIndent(summary, "", "  ")
	if err = os.WriteFile(filepath.Join(*outdir, "summary.json"), b, 0600); err != nil {
		panic(err)
	}
	fmt.Println(string(b))
	if failed > 0 {
		os.Exit(1)
	}
}

func run(a *agent.OpenAI, c scenario, repeat int) []result {
	now := time.Now().UTC()
	zone, _ := time.LoadLocation("America/Cuiaba")
	if c.Zone != "" {
		zone, _ = time.LoadLocation(c.Zone)
	}
	local := now.In(zone)
	day := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone)
	slots := []domain.Slot{}
	for i, h := range []time.Duration{8 * time.Hour, 8*time.Hour + 45*time.Minute, 10 * time.Hour, 14 * time.Hour, 14*time.Hour + 45*time.Minute, 18 * time.Hour} {
		start := day.Add(h)
		slots = append(slots, domain.Slot{ID: fmt.Sprintf("slot-%d", i+1), Provider: "local", CloserID: 1, Start: start, End: start.Add(45 * time.Minute)})
	}
	zero := int64(0)
	s := domain.State{ID: "synthetic-" + c.ID, Owner: "go", Stage: "contact_pending", Profile: domain.Form{Name: "Ana", Company: "Oficina Exemplo", Goal: "Reduzir trabalho manual no atendimento", Revenue: &zero, Consent: true, TimeZone: zone.String()}}
	if c.Goal != nil {
		s.Profile.Goal = *c.Goal
	}
	if c.Seed == "offered" || c.Seed == "one_offer" {
		s.Stage = "choosing_slot"
		s.Offered = append([]domain.Slot(nil), slots[3:5]...)
		if c.Seed == "one_offer" {
			s.Offered = s.Offered[:1]
		}
	}
	if strings.HasPrefix(c.Seed, "booked") || c.Seed == "in_progress" || c.Seed == "no_show" || c.Seed == "past_unknown" {
		start := day.Add(14 * time.Hour)
		if c.Seed == "in_progress" {
			start = now.Add(-time.Minute)
		}
		if c.Seed == "no_show" || c.Seed == "past_unknown" {
			start = now.Add(-24 * time.Hour)
		}
		s.Stage = "booked"
		s.Booking = &domain.Booking{ID: "current-booking", Provider: "local", CloserID: 1, Start: start, End: start.Add(45 * time.Minute)}
		if c.Seed == "no_show" {
			s.Booking.Outcome = "no_show"
			s.Stage = "no_show"
		}
	}
	if c.Seed == "opted_out" {
		s.OptedOut = true
		s.Paused = true
	}
	if c.Seed == "human" {
		s.Human = true
	}
	if c.Seed == "no_consent" {
		s.Profile.Consent = false
	}
	// A real availability query excludes the reservation's occupied interval.
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.Start.After(now) {
		free := make([]domain.Slot, 0, len(slots))
		for _, slot := range slots {
			if slot.Start.Before(s.Booking.End) && slot.End.After(s.Booking.Start) {
				continue
			}
			free = append(free, slot)
		}
		slots = free
	}
	p := domain.Policy{TimeZone: zone.String(), Brand: "Arkom", DisableFollowups: true}
	opening := domain.ContactOpening(s, p, now).Text
	if len(s.Offered) > 0 {
		parts := []string{}
		for _, slot := range s.Offered {
			parts = append(parts, domain.SlotLabel(slot, zone))
		}
		opening = "Tenho " + strings.Join(parts, " ou ") + ". Qual você escolhe?"
	}
	if c.Seed == "no_show" {
		opening = "Não conseguimos conversar no horário marcado. Você quer encontrar outro horário?"
	}
	if c.Seed == "in_progress" {
		opening = domain.MeetingStartOpening(s, p).Text
	}
	if c.Opening != "" {
		opening = c.Opening
	}
	s.History = []domain.Turn{{Role: "assistant", Text: opening, At: now.Add(-time.Minute)}}
	if c.Calendar == "empty" || c.Calendar == "unavailable" {
		slots = nil
	}
	heads := map[string]conversation.Head{}
	rows := []result{}
	for i, tr := range c.Turns {
		e := domain.Event{ID: fmt.Sprintf("eval-%s-%d-%d", c.ID, repeat, i), Kind: "message.received", LeadID: s.ID, At: time.Now().UTC(), Text: tr.Text, CalendarUnavailable: c.Calendar == "unavailable"}
		hist := []conversation.Message{}
		for _, h := range s.History {
			hist = append(hist, conversation.Message{Role: h.Role, Text: h.Text, At: h.At})
		}
		e.Context = &conversation.Window{ConversationID: s.ID, RunID: e.ID, Version: int64(i + 1), History: hist, Heads: heads}
		started := time.Now()
		out := agent.Outcome{Agent: "domain"}
		var err error
		if domain.NeedsPlan(s, e, e.At) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			out, err = a.Run(ctx, s, e, slots, agent.Select(s, e))
			cancel()
		}
		r := result{Case: c.ID, Category: c.Category, Repeat: repeat, Turn: i + 1, Input: tr.Text, Expected: tr.Expect, Agent: out.Agent, Decision: out.Decision, UsedPrevious: out.UsedPrevious, Replies: []string{}, Failures: []string{}}
		fail := func(msg string) { r.Failures = append(r.Failures, msg) }
		if err != nil {
			fail(err.Error())
			r.LatencyMS = time.Since(started).Milliseconds()
			rows = append(rows, r)
			break
		}
		if out.Dialogue != nil {
			s.Dialogue = out.Dialogue
			assessment := out.Dialogue.Assessment
			r.Assessment = &assessment
		}
		beforeBooking := ""
		if s.Booking != nil {
			beforeBooking = s.Booking.ID
		}
		res := domain.Reduce(s, e, out.Decision, slots, p, time.Now().UTC())
		s = res.State
		r.Decision = res.Decision
		books, cancels := 0, 0
		var mutations []domain.Effect
		for _, fx := range res.Effects {
			switch fx.Kind {
			case "book":
				books++
				mutations = append(mutations, fx)
			case "cancel":
				cancels++
				mutations = append(mutations, fx)
			case "send":
				if fx.Purpose != "reply" {
					continue
				}
				if ok, _ := domain.Deliverable(s, fx, time.Now().UTC()); !ok {
					continue
				}
				text := domain.Render(s, fx, p, time.Now().UTC())
				r.Replies = append(r.Replies, text)
				fx.Dialogue = &domain.DialogueDelivery{Agent: out.Agent, TurnID: e.ID}
				if s.Dialogue != nil && s.Dialogue.Objection != nil {
					fx.Dialogue.ObjectionID = s.Dialogue.Objection.ID
				}
				domain.AppendTurn(&s, "assistant", text, e.At)
				domain.RecordDialogueDelivery(&s, fx, text, e.At)
			}
		}
		matched := false
		for _, want := range strings.Split(tr.Expect, "|") {
			switch want {
			case "reply":
				matched = matched || (books == 0 && cancels == 0 && len(r.Replies) > 0 && len(s.Offered) == 0 && !s.Human && !s.Paused)
			case "keep_booking":
				matched = matched || (beforeBooking != "" && s.Booking != nil && s.Booking.ID == beforeBooking && books == 0 && cancels == 0 && len(s.Offered) == 0 && len(r.Replies) > 0 && !s.Human && !s.Paused)
			case "offer":
				matched = matched || (len(s.Offered) > 0 && books == 0 && cancels == 0 && len(r.Replies) > 0 && !s.Human)
			case "clarify":
				matched = matched || (books == 0 && cancels == 0 && len(r.Replies) > 0 && !s.Human && !s.Paused)
			case "book":
				matched = matched || (books == 1 && cancels == 0 && beforeBooking == "" && !s.Human)
			case "reschedule":
				matched = matched || (books == 1 && cancels == 0 && beforeBooking != "" && mutations[0].BookingID == beforeBooking && !s.Human)
			case "cancel":
				matched = matched || (cancels == 1 && books == 0 && !s.Human)
			case "confirm":
				matched = matched || (s.Booking != nil && s.Booking.ConfirmedAt != nil && books == 0 && cancels == 0 && s.Booking.Outcome == "")
			case "opt_out":
				matched = matched || (s.OptedOut && s.Paused && books == 0 && cancels == 0)
			case "pause":
				matched = matched || (s.Paused && !s.OptedOut && books == 0 && cancels == 0)
			case "handoff":
				matched = matched || (s.Human && books == 0 && cancels == 0)
			case "silent":
				matched = matched || (len(r.Replies) == 0 && books == 0 && cancels == 0)
			case "no_availability":
				matched = matched || (s.Stage == "awaiting_availability" && len(s.Offered) == 0 && len(r.Replies) > 0 && books == 0 && cancels == 0 && !s.Human)
			default:
				panic("unknown expectation: " + want)
			}
		}
		if !matched {
			fail("expected " + tr.Expect + "; action=" + r.Decision.Action + " stage=" + s.Stage)
		}
		if tr.Agent != "" && out.Agent != tr.Agent {
			fail("expected specialist " + tr.Agent)
		}
		text := strings.ToLower(strings.Join(r.Replies, "\n\n"))
		if tr.RequireCTA && !directCustomerAction.MatchString(text) {
			fail("missing concrete call to action")
		}
		if tr.RequireCTA && strings.Contains(text, "se quiser") {
			fail("vague conditional invitation instead of direct response")
		}
		if strings.Contains(text, "america/") || strings.Contains(text, "europe/") || strings.Contains(text, "etc/") {
			fail("technical timezone exposed")
		}
		if len(tr.ContainsAny) > 0 {
			found := false
			for _, v := range tr.ContainsAny {
				found = found || strings.Contains(text, strings.ToLower(v))
			}
			if !found {
				fail("missing expected factual response")
			}
		}
		for _, v := range append([]string{"sou o agente", "agente de objeções", "previous_response_id", "decisions api", "worker de", "vou te transferir para outro agente"}, tr.Forbidden...) {
			if strings.Contains(text, strings.ToLower(v)) {
				fail("forbidden text: " + v)
			}
		}
		if strings.Count(text, "?") > 1 {
			fail("more than one question")
		}
		if out.ResponseID != "" {
			heads[out.Agent] = conversation.Head{ResponseID: out.ResponseID, PromptVersion: out.PromptVersion}
		}
		// Execute only synthetic calendar outcomes; no provider can be reached.
		for _, fx := range mutations {
			kind := "booking.cancelled"
			event := domain.Event{ID: e.ID + "-calendar", LeadID: s.ID, At: time.Now().UTC(), BookingID: fx.BookingID}
			if fx.Kind == "book" {
				kind = "booking.created"
				if fx.BookingID != "" {
					kind = "booking.rescheduled"
				}
				event.Booking = &domain.Booking{ID: e.ID + "-booking", Provider: fx.Slot.Provider, CloserID: fx.Slot.CloserID, Start: fx.Slot.Start, End: fx.Slot.End}
			}
			event.Kind = kind
			done := domain.Reduce(s, event, domain.Decision{}, nil, p, event.At)
			s = done.State
			for _, send := range done.Effects {
				if send.Kind == "send" && send.Purpose == "booking_confirmation" {
					text := domain.Render(s, send, p, event.At)
					domain.AppendTurn(&s, "assistant", text, event.At)
					r.Replies = append(r.Replies, text)
				}
			}
			// The scheduling worker emits this acknowledgment only after Cancel
			// succeeds; include it in the synthetic history for subsequent turns.
			if fx.Kind == "cancel" {
				text := "Sua reserva foi cancelada. Se quiser retomar, posso ajudar a encontrar outro horário."
				domain.AppendTurn(&s, "assistant", text, event.At)
				r.Replies = append(r.Replies, text)
			}
		}
		r.LatencyMS = time.Since(started).Milliseconds()
		r.Pass = len(r.Failures) == 0
		rows = append(rows, r)
	}
	return rows
}

var directCustomerAction = regexp.MustCompile(`(?i)(\?|\b(responda|responde|escolha|escolhe|confirme|confirma|separe|separa|traga|traz|leve|leva|envie|envia|me mande|me manda|me conte|me conta|me diga|me diz|vamos (usar|avaliar|olhar))\b)`)
