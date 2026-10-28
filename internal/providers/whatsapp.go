package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

type ClickMax struct {
	URL, Token, ChannelID string
	Client                *http.Client
}

func (c *ClickMax) Send(ctx context.Context, key string, s domain.State, text, purpose string, now time.Time) (Receipt, error) {
	if s.LastInbound.IsZero() || now.Sub(s.LastInbound) >= 23*time.Hour {
		return Receipt{}, &Failure{Code: "clickmax_requires_approved_template"}
	}
	if s.Profile.ProviderLeadID == "" {
		return Receipt{}, &Failure{Code: "clickmax_lead_unresolved"}
	}
	if len([]rune(text)) > 1000 {
		return Receipt{}, &Failure{Code: "message_too_long"}
	}
	ph := strings.TrimPrefix(s.Profile.Phone, "+55")
	if !strings.HasPrefix(s.Profile.Phone, "+55") || (len(ph) != 10 && len(ph) != 11) {
		return Receipt{}, &Failure{Code: "clickmax_phone_unsupported"}
	}
	if len(ph) == 11 {
		ph = ph[:2] + ph[3:]
	}
	h := sha256.Sum256([]byte(key))
	turn := "bia-go-" + hex.EncodeToString(h[:16])
	var out struct {
		Resultado string  `json:"resultado"`
		Motivo    string  `json:"motivo"`
		IDs       []int64 `json:"ids"`
	}
	e := JSON(ctx, c.Client, "POST", c.URL, map[string]string{"Authorization": "Bearer " + c.Token}, map[string]any{"canal": "cm:" + c.ChannelID, "telefone": ph, "cm_lead_id": s.Profile.ProviderLeadID, "turno_id": turn, "tipo": "texto", "bolhas": []string{text}}, &out, true)
	if e != nil {
		return Receipt{}, e
	}
	if out.Resultado == "enviado" || (out.Resultado == "negado" && out.Motivo == "ja_processada") {
		return Receipt{ID: turn, Status: "sent"}, nil
	}
	return Receipt{}, &Failure{Code: "clickmax_" + safeCode(out.Motivo), Retryable: out.Resultado == "erro"}
}

type DataCrazy struct {
	URL, Token, TenantID string
	Client               *http.Client
}

func (d *DataCrazy) Send(ctx context.Context, key string, s domain.State, text, purpose string, now time.Time) (Receipt, error) {
	if s.Profile.ConversationID == "" {
		return Receipt{}, &Failure{Code: "datacrazy_conversation_unresolved"}
	}
	if s.LastInbound.IsZero() || now.Sub(s.LastInbound) >= 23*time.Hour {
		return Receipt{}, &Failure{Code: "datacrazy_requires_approved_template"}
	}
	var out struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	e := JSON(ctx, d.Client, "POST", d.URL, map[string]string{"Authorization": "Bearer " + d.Token, "X-Tenant-Id": d.TenantID, "Accept": "application/json"}, map[string]any{"jsonrpc": "2.0", "id": key, "method": "tools/call", "params": map[string]any{"name": "conversation_send_message", "arguments": map[string]any{"id": s.Profile.ConversationID, "body": text}}}, &out, false)
	if e != nil {
		return Receipt{}, e
	}
	if len(out.Error) > 0 || out.Result.IsError {
		return Receipt{}, &Failure{Code: "datacrazy_rpc_error", Ambiguous: true}
	}
	for _, content := range out.Result.Content {
		var r struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(content.Text), &r) == nil && r.ID != "" && (r.Status == "SENT" || r.Status == "DELIVERED" || r.Status == "READ") {
			return Receipt{ID: r.ID, Status: "sent"}, nil
		}
	}
	return Receipt{}, &Failure{Code: "datacrazy_delivery_unconfirmed", Ambiguous: true}
}

func safeCode(v string) string {
	if v == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, c := range v {
		if c >= 'a' && c <= 'z' || c == '_' {
			b.WriteRune(c)
		}
		if b.Len() > 60 {
			break
		}
	}
	return b.String()
}

type Router map[string]Messenger

func (r Router) Send(ctx context.Context, k string, s domain.State, t, p string, n time.Time) (Receipt, error) {
	m, ok := r[s.Profile.Channel]
	if !ok {
		return Receipt{}, &Failure{Code: "channel_not_configured"}
	}
	return m.Send(ctx, k, s, t, p, n)
}
