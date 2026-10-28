package config

import (
	"strings"
	"testing"
)

func TestPreparationFlagSurvivesPolicyInitialization(t *testing.T) {
	for k, v := range map[string]string{
		"BIA_MODE": "shadow", "BIA_DATABASE_URL": "postgres://unused", "BIA_API_TOKEN": strings.Repeat("x", 32),
		"BIA_MESSAGING_PROVIDER": "twilio", "BIA_MEETING_PREPARATION_ENABLED": "true",
		"TWILIO_PREPARATION_CONTENT_SID": "HX" + strings.Repeat("1", 32), "TWILIO_ACCOUNT_SID": "AC" + strings.Repeat("2", 32),
		"TWILIO_AUTH_TOKEN": "test", "TWILIO_WHATSAPP_FROM": "+5511936246321", "BIA_VOICE_PUBLIC_URL": "https://example.com",
	} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil || !c.Policy.MeetingPreparation {
		t.Fatal(c.Policy.MeetingPreparation, err)
	}
	t.Setenv("TWILIO_PREPARATION_CONTENT_SID", "")
	if _, err = Load(); err == nil {
		t.Fatal("preparation enabled without template")
	}
}
