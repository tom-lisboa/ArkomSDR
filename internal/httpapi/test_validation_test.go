package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"arkom/bia/internal/domain"
)

func TestFormValidationReturnsFieldAndLogsNoPersonalData(t *testing.T) {
	base := map[string]any{"name": "Pessoa privada", "company": "Empresa privada", "phone": "+5577999749058", "email": "privado@example.com", "goal": "Objetivo privado", "revenue": 0, "timezone": "America/Cuiaba", "consent": true}
	type tc struct {
		name, key, field, code string
		value                  any
	}
	cases := []tc{
		{"missing_name", "name", "name", "invalid profile", ""},
		{"long_company", "company", "company", "invalid profile", strings.Repeat("C", 201)},
		{"long_goal", "goal", "goal", "invalid profile", strings.Repeat("G", 2001)},
		{"long_email", "email", "email", "invalid profile", strings.Repeat("E", 255)},
		{"invalid_phone", "phone", "phone", "phone_must_be_e164", "123"},
		{"timezone", "timezone", "timezone", "invalid timezone", "Brazil/Unknown"},
		{"consent", "consent", "consent", "test_contact_consent_required", false},
		{"negative_revenue", "revenue", "revenue", "revenue cannot be negative", -1},
		{"missing_ninth_sms", "phone", "phone", "SMS consent requires its version and the original mobile number", "+557799749058"},
	}
	key := strings.Repeat("k", 48)
	srv := &Server{TestFormToken: key, Policy: domain.Policy{TimeZone: "America/Cuiaba"}}
	old := slog.Default()
	defer slog.SetDefault(old)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := map[string]any{}
			for k, v := range base {
				input[k] = v
			}
			input[c.key] = c.value
			if c.name == "missing_ninth_sms" {
				input["sms_consent"] = true
			}
			body, _ := json.Marshal(input)
			req := httptest.NewRequest("POST", "/webhooks/test-form/"+key, bytes.NewReader(body))
			req.Header.Set("Idempotency-Key", "private-submission-id")
			out := httptest.NewRecorder()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			srv.Handler().ServeHTTP(out, req)
			var response struct {
				Error     string   `json:"error"`
				Message   string   `json:"message"`
				Fields    []string `json:"campos"`
				Ref       string   `json:"request_ref"`
				Retryable bool     `json:"retryable"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || out.Code != 422 || response.Error != c.code || response.Message == "" || len(response.Ref) != 12 || response.Retryable {
				t.Fatal(out.Code, out.Body.String(), err)
			}
			if len(response.Fields) != 1 || response.Fields[0] != c.field {
				t.Fatal(response.Fields)
			}
			if !strings.Contains(logs.String(), "test_form_validation_rejected") {
				t.Fatal("missing diagnostic")
			}
			for _, private := range []string{key, "Pessoa privada", "Empresa privada", "Objetivo privado", "privado@example.com", "+5577999749058", "+557799749058", "private-submission-id"} {
				if strings.Contains(logs.String()+out.Body.String(), private) {
					t.Fatal("personal data in validation diagnostic")
				}
			}
		})
	}
}
