package httpapi

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"

	"arkom/bia/internal/domain"
)

// Validation diagnostics contain field names and a correlation hash, never the
// request body, phone, profile, capability URL, or credentials.
func testValidationError(w http.ResponseWriter, r *http.Request, code string, fields ...string) {
	message := "Confira os campos informados antes de enviar novamente."
	switch code {
	case "phone_must_be_e164":
		message = "Informe o telefone com DDI e DDD, como +55 seguido do número completo."
	case "test_contact_consent_required":
		message = "Marque a autorização de contato pelo WhatsApp para iniciar o teste."
	case "SMS consent requires its version and the original mobile number":
		message = "Para receber SMS no Brasil, informe o celular completo com DDD e o nono dígito 9."
	case "invalid profile":
		message = "Confira nome, empresa, objetivo e e-mail: um campo está vazio ou ultrapassou o limite."
	case "invalid timezone", "invalid_timezone":
		message = "Selecione um fuso horário válido, como America/Cuiaba."
	case "revenue cannot be negative":
		message = "O faturamento deve ser zero ou um número positivo."
	case "invalid_date":
		message = "Use uma data válida no formato AAAA-MM-DD."
	case "invalid_slot_id":
		message = "Selecione novamente um horário retornado pela agenda."
	case "valid_idempotency_key_required":
		message = "O formulário precisa enviar uma chave de identificação válida para esta submissão."
	case "valid_submission_slot_and_idempotency_key_required":
		message = "Confira a identificação da submissão e selecione novamente um horário disponível."
	case "valid_phone_and_submission_required":
		message = "Confira o telefone e a identificação da submissão do formulário."
	case "explicit_sms_consent_and_idempotency_key_required":
		message = "Envie a preferência de SMS como true ou false e a identificação desta alteração."
	case "valid_submission_and_idempotency_key_required":
		message = "Confira a identificação da submissão e deste disparo."
	}
	ref := fmt.Sprintf("%x", sha256.Sum256([]byte(r.Header.Get("Idempotency-Key"))))[:12]
	if fields == nil {
		fields = []string{}
	}
	slog.Warn("test_form_validation_rejected", "status", 422, "code", code, "fields", fields, "request_ref", ref)
	write(w, 422, map[string]any{"error": code, "message": message, "campos": fields, "request_ref": ref, "retryable": false, "next_action": "correct_fields"})
}

func formValidationFields(code string, f domain.Form) []string {
	switch code {
	case "invalid profile":
		fields := []string{}
		if f.Name == "" || len(f.Name) > 120 {
			fields = append(fields, "name")
		}
		if len(f.Company) > 200 {
			fields = append(fields, "company")
		}
		if len(f.Goal) > 2000 {
			fields = append(fields, "goal")
		}
		if len(f.Email) > 254 {
			fields = append(fields, "email")
		}
		return fields
	case "invalid timezone":
		return []string{"timezone"}
	case "revenue cannot be negative":
		return []string{"revenue"}
	case "SMS consent requires its version and the original mobile number", "invalid form phone or lead_id":
		return []string{"phone"}
	default:
		return nil
	}
}
