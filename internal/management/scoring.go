package management

import (
	"fmt"
	"strings"
	"time"
)

type Signal struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Points    int    `json:"points"`
	condition string
}

// One rule set builds both the SQL filters and the explanation returned by Go.
var signals = []Signal{
	{"profile", "Nome e telefone informados", 5, `state#>>'{profile,name}'<>'' AND state#>>'{profile,phone}'<>''`},
	{"goal", "Objetivo informado", 5, `btrim(COALESCE(state#>>'{profile,goal}',''))<>''`},
	{"replied", "Cliente respondeu", 20, `inbound_count>0`},
	{"recent_24h", "Resposta nas últimas 24 horas", 15, `last_reply>=$1::timestamptz-interval '24 hours' AND last_reply<=$1`},
	{"recent_7d", "Resposta entre um e sete dias", 8, `last_reply>=$1::timestamptz-interval '7 days' AND last_reply<$1::timestamptz-interval '24 hours'`},
	{"engaged", "Três ou mais mensagens recebidas", 10, `inbound_count>=3`},
	{"booked", "Reunião futura ou em andamento", 25, `COALESCE(state#>>'{booking,outcome}','')='' AND (state#>>'{booking,end}')::timestamptz>$1`},
	{"confirmed", "Presença confirmada para a reserva atual", 15, `COALESCE(state#>>'{booking,outcome}','')='' AND (state#>>'{booking,end}')::timestamptz>$1 AND state#>>'{booking,confirmed_at}' IS NOT NULL`},
	{"attended", "Último resultado registrado: compareceu", 20, `last_outcome='attended'`},
	{"open_objection", "Objeção ainda em aberto", -10, `state#>>'{dialogue,objection,status}' IN ('open','clarifying')`},
	{"no_show", "Último resultado registrado: faltou", -20, `last_outcome='no_show'`},
}

type Score struct {
	Value         int       `json:"value"`
	Temperature   string    `json:"temperature"`
	Version       string    `json:"version"`
	ComputedAt    time.Time `json:"computed_at"`
	Signals       []Signal  `json:"signals"`
	IsProbability bool      `json:"is_probability"`
	Blocked       bool      `json:"blocked"`
}

func evaluate(facts map[string]bool, now time.Time) Score {
	out := Score{Version: "engagement-v1", ComputedAt: now, Signals: []Signal{}}
	for _, rule := range signals {
		if facts[rule.Key] {
			out.Value += rule.Points
			out.Signals = append(out.Signals, rule)
		}
	}
	out.Value = max(0, min(100, out.Value))
	out.Blocked = facts["opted_out"]
	if out.Blocked {
		out.Value = 0
		out.Signals = append(out.Signals, Signal{Key: "opted_out", Label: "Contato pediu saída; pontuação zerada"})
	}
	out.Temperature = "cold"
	if out.Value >= 70 {
		out.Temperature = "hot"
	} else if out.Value >= 40 {
		out.Temperature = "warm"
	}
	return out
}
func scoreExpressions() (string, string) {
	parts := []string{}
	facts := []string{}
	for _, r := range signals {
		parts = append(parts, fmt.Sprintf("CASE WHEN %s THEN %d ELSE 0 END", r.condition, r.Points))
		facts = append(facts, fmt.Sprintf("'%s',COALESCE((%s),false)", r.Key, r.condition))
	}
	opted := `COALESCE((state->>'opted_out')::boolean,false)`
	return "CASE WHEN " + opted + " THEN 0 ELSE GREATEST(0,LEAST(100," + strings.Join(parts, "+") + ")) END", "jsonb_build_object(" + strings.Join(facts, ",") + ",'opted_out'," + opted + ")"
}
