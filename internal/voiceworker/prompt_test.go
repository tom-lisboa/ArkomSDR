package voiceworker

import (
	"arkom/bia/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestReminderHasSharedFieldsAndReadOnlyTools(t *testing.T) {
	n := int64(0)
	start := time.Now().Add(time.Hour)
	m := domain.MeetingContext{Status: "available", Customer: domain.Customer{Company: "Acme", Name: "Ana", Revenue: &n, MeetingDate: &start, TimeZone: "America/Cuiaba"}, Booking: &domain.Booking{Start: start}, Now: time.Now()}
	prompt := reminderInstructions(m, nil)
	for _, want := range []string{`"nome_empresa":"Acme"`, `"nome_pessoa":"Ana"`, `"faturamento":0`, `"data_reuniao":`, `get_meeting`, `record_reminder_note`} {
		if !strings.Contains(prompt, want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "Você NÃO tem acesso à agenda") {
		t.Fatal("unresolved template or wrong restriction")
	}
	for _, v := range toolDefinitions() {
		name := v.(map[string]any)["name"]
		if name == "book_slot" || name == "list_slots" {
			t.Fatal(name)
		}
	}
	voicemail := voicemailInstructions(m)
	if strings.Contains(voicemail, "Acme") || strings.Contains(voicemail, "faturamento") {
		t.Fatal("private briefing in voicemail")
	}
}
