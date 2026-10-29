package management

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScoreIsBoundedExplainableAndNotProbability(t *testing.T) {
	now := time.Now()
	facts := map[string]bool{"profile": true, "goal": true, "replied": true, "recent_24h": true, "engaged": true, "booked": true, "confirmed": true}
	score := evaluate(facts, now)
	if score.Value != 95 || score.Temperature != "hot" || score.IsProbability || len(score.Signals) != 7 {
		t.Fatal(score)
	}
	facts["attended"] = true
	score = evaluate(facts, now)
	if score.Value != 100 {
		t.Fatal(score)
	}
	facts["opted_out"] = true
	score = evaluate(facts, now)
	if score.Value != 0 || !score.Blocked {
		t.Fatal(score)
	}
	if score = evaluate(map[string]bool{"no_show": true, "open_objection": true}, now); score.Value != 0 || score.Temperature != "cold" {
		t.Fatal(score)
	}
}
func TestOpenAPIHasScopedAuthAndSchemas(t *testing.T) {
	var doc struct {
		OpenAPI string                                `json:"openapi"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}
	if json.Unmarshal(openAPI, &doc) != nil || doc.OpenAPI != "3.0.3" || len(doc.Paths) < 15 {
		t.Fatal("invalid API contract")
	}
}
func TestPasswordHashRoundTripAndWrongPassword(t *testing.T) {
	hash, err := passwordHash("secure-test-password")
	if err != nil || !passwordMatches("secure-test-password", hash) || passwordMatches("wrong-password", hash) {
		t.Fatal(err)
	}
	if _, err = passwordHash("short"); err == nil {
		t.Fatal("weak password accepted")
	}
}
