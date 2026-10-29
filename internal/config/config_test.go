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

func TestSpecialistsAreExplicitAndRequireSupportedDecisionsModel(t *testing.T) {
	for k, v := range map[string]string{"BIA_MODE": "shadow", "BIA_DATABASE_URL": "postgres://unused", "BIA_API_TOKEN": strings.Repeat("x", 32), "BIA_MESSAGING_PROVIDER": "gateway", "OPENAI_API_KEY": "test", "OPENAI_MODEL": "test-model", "BIA_SPECIALISTS_ENABLED": "true", "OPENAI_DECISIONS_MODEL": "gpt-6-luna"} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil || !c.Specialists || c.DecisionsModel != "gpt-6-luna" {
		t.Fatal(c.Specialists, c.DecisionsModel, err)
	}
	t.Setenv("OPENAI_DECISIONS_MODEL", "unsupported")
	if _, err = Load(); err == nil {
		t.Fatal("enabled unsupported classifier")
	}
	t.Setenv("BIA_SPECIALISTS_ENABLED", "false")
	t.Setenv("OPENAI_API_KEY", "")
	if c, err = Load(); err != nil || c.Specialists {
		t.Fatal("legacy configuration requires Decisions", err)
	}
}

func TestMeetingStartRequiresItsOwnTemplateButNotVoiceCadence(t *testing.T) {
	for k, v := range map[string]string{"BIA_MODE": "shadow", "BIA_DATABASE_URL": "postgres://unused", "BIA_API_TOKEN": strings.Repeat("x", 32), "BIA_MESSAGING_PROVIDER": "twilio", "BIA_MEETING_START_ENABLED": "true", "TWILIO_MEETING_START_CONTENT_SID": "HX" + strings.Repeat("a", 32), "TWILIO_ACCOUNT_SID": "AC" + strings.Repeat("b", 32), "TWILIO_AUTH_TOKEN": "test", "TWILIO_WHATSAPP_FROM": "+5511936246321", "BIA_VOICE_PUBLIC_URL": "https://example.com", "BIA_MEETING_REMINDERS_ENABLED": "false"} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil || !c.Policy.MeetingStart || c.Policy.MeetingReminders {
		t.Fatal(c.Policy, err)
	}
	t.Setenv("TWILIO_MEETING_START_CONTENT_SID", "")
	if _, err = Load(); err == nil {
		t.Fatal("enabled T0 without template")
	}
}

func TestQuickRepliesRequireCompleteTemplateSet(t *testing.T) {
	for k, v := range map[string]string{"BIA_MODE": "shadow", "BIA_DATABASE_URL": "postgres://unused", "BIA_API_TOKEN": strings.Repeat("x", 32), "BIA_MESSAGING_PROVIDER": "twilio", "BIA_QUICK_REPLIES_ENABLED": "true", "TWILIO_ACCOUNT_SID": "AC" + strings.Repeat("b", 32), "TWILIO_AUTH_TOKEN": "test", "TWILIO_WHATSAPP_FROM": "+5511936246321", "BIA_VOICE_PUBLIC_URL": "https://example.com"} {
		t.Setenv(k, v)
	}
	keys := []string{"TWILIO_QUICK_REPLY_1_SID", "TWILIO_QUICK_REPLY_2_SID", "TWILIO_QUICK_REPLY_3_SID", "TWILIO_FORM_BUTTONS_SID", "TWILIO_CONTEXT_BUTTONS_SID", "TWILIO_CONTEXT_QUESTION_BUTTONS_SID", "TWILIO_PREPARATION_BUTTONS_SID", "TWILIO_MEETING_START_BUTTONS_SID"}
	for _, key := range keys {
		t.Setenv(key, "HX"+strings.Repeat("c", 32))
	}
	c, err := Load()
	if err != nil || !c.QuickReplies.Enabled {
		t.Fatal(c.QuickReplies, err)
	}
	for _, key := range keys {
		t.Setenv(key, "")
		if _, err = Load(); err == nil {
			t.Fatal("enabled incomplete templates", key)
		}
		t.Setenv(key, "HX"+strings.Repeat("c", 32))
	}
}

func TestManagementOriginsMustBeExactAndExplicit(t *testing.T) {
	for k, v := range map[string]string{"BIA_MODE": "shadow", "BIA_DATABASE_URL": "postgres://unused", "BIA_API_TOKEN": strings.Repeat("x", 32), "BIA_MESSAGING_PROVIDER": "gateway", "BIA_MANAGEMENT_ENABLED": "true", "BIA_MANAGEMENT_ORIGINS": "https://staff.example,http://localhost:3000"} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil || !c.ManagementEnabled || len(c.ManagementOrigins) != 2 {
		t.Fatal(c.ManagementOrigins, err)
	}
	for _, origin := range []string{"*", "https://staff.example/path", "https://staff.example?x=y", "http://localhost:3000/other", "http://staff.example"} {
		t.Setenv("BIA_MANAGEMENT_ORIGINS", origin)
		if _, err = Load(); err == nil {
			t.Fatal("unsafe/misconfigured origin accepted", origin)
		}
	}
}
