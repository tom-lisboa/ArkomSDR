// replay evaluates synthetic or sanitized scenarios without any network access.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
	_ "time/tzdata"

	"arkom/bia/internal/domain"
)

func main() {
	input := flag.String("input", "examples/replay.jsonl", "JSONL with event, decision and optional slots")
	flag.Parse()
	f, e := os.Open(*input)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	defer f.Close()
	p := domain.Policy{Brand: "Grupo Bilhon", SchedulingURL: "https://example.invalid/agendar", TimeZone: "America/Cuiaba", QuietStart: 8, QuietEnd: 20, FollowupAfter: 2 * time.Hour, Reminders: []time.Duration{24 * time.Hour, 2 * time.Hour, 15 * time.Minute}}
	states := map[string]domain.State{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var v struct {
			Event    domain.Event    `json:"event"`
			Decision domain.Decision `json:"decision"`
			Slots    []domain.Slot   `json:"slots"`
		}
		if e = json.Unmarshal(scan.Bytes(), &v); e != nil {
			fmt.Fprintln(os.Stderr, "invalid scenario JSON")
			os.Exit(1)
		}
		if e = domain.ValidateEvent(v.Event); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		r := domain.Reduce(states[v.Event.LeadID], v.Event, v.Decision, v.Slots, p, v.Event.At)
		states[v.Event.LeadID] = r.State
		_ = enc.Encode(map[string]any{"event_id": v.Event.ID, "stage": r.State.Stage, "mode": "replay_no_network", "effects": r.Effects})
	}
	if e = scan.Err(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
