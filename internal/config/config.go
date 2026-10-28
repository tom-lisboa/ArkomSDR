package config

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/store"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Window                             store.WindowPolicy
	MessagingProvider, TestPhone       string
	TestFormToken                      string
	Voice                              Voice
	RedisURL, MediaModel               string
	Address, DSN, Token, Mode          string
	OpenAIKey, OpenAIModel, AudioModel string
	MediaHosts                         []string
	Policy                             domain.Policy
	Workers                            int
	PerLeadDaily, GlobalHourly         int
	GatewayURL, GatewayToken           string
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func Load() (Config, error) {
	c := Config{RedisURL: env("BIA_REDIS_URL", "redis://127.0.0.1:6389/0"), MediaModel: os.Getenv("OPENAI_MEDIA_MODEL"), Address: env("BIA_ADDR", "127.0.0.1:8080"), DSN: os.Getenv("BIA_DATABASE_URL"), Token: os.Getenv("BIA_API_TOKEN"), Mode: env("BIA_MODE", "shadow"), OpenAIKey: os.Getenv("OPENAI_API_KEY"), OpenAIModel: os.Getenv("OPENAI_MODEL"), AudioModel: os.Getenv("OPENAI_AUDIO_MODEL"), GatewayURL: os.Getenv("BIA_GATEWAY_URL"), GatewayToken: os.Getenv("BIA_GATEWAY_TOKEN"), Workers: 4}
	c.PerLeadDaily = 15
	c.GlobalHourly = 60
	for key, target := range map[string]*int{"BIA_PER_LEAD_DAILY": &c.PerLeadDaily, "BIA_GLOBAL_HOURLY": &c.GlobalHourly} {
		if raw := os.Getenv(key); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 1 || n > 1000 {
				return c, errors.New("invalid " + key)
			}
			*target = n
		}
	}
	c.Window = store.DefaultWindowPolicy()
	for key, target := range map[string]*time.Duration{"BIA_CONTEXT_QUIET": &c.Window.Quiet, "BIA_CONTEXT_MEDIA_QUIET": &c.Window.MediaQuiet, "BIA_CONTEXT_MEDIA_SETTLE": &c.Window.MediaSettle, "BIA_CONTEXT_MAX_WAIT": &c.Window.MaxWait, "BIA_CONTEXT_MEDIA_TIMEOUT": &c.Window.MediaTimeout} {
		if raw := os.Getenv(key); raw != "" {
			v, e := time.ParseDuration(raw)
			if e != nil || v < time.Second || v > 5*time.Minute {
				return c, errors.New("invalid " + key)
			}
			*target = v
		}
	}
	if c.Window.MaxWait < c.Window.Quiet || c.Window.MaxWait < c.Window.MediaQuiet {
		return c, errors.New("context max wait must cover quiet intervals")
	}
	c.MessagingProvider = env("BIA_MESSAGING_PROVIDER", "gateway")
	c.TestPhone = os.Getenv("BIA_TEST_PHONE")
	c.TestFormToken = os.Getenv("BIA_TEST_FORM_TOKEN")
	if c.TestFormToken != "" && len(c.TestFormToken) < 32 {
		return c, errors.New("test form token must have at least 32 characters")
	}
	if c.MessagingProvider != "gateway" && c.MessagingProvider != "twilio" {
		return c, errors.New("invalid BIA_MESSAGING_PROVIDER")
	}
	if c.TestPhone != "" {
		if p, e := domain.NormalizePhone(c.TestPhone); e != nil || p != c.TestPhone {
			return c, errors.New("BIA_TEST_PHONE must be E.164")
		}
	}
	for _, host := range strings.Split(os.Getenv("BIA_MEDIA_HOSTS"), ",") {
		if h := strings.TrimSpace(host); h != "" {
			c.MediaHosts = append(c.MediaHosts, h)
		}
	}
	c.Policy = domain.Policy{Brand: env("BIA_BRAND", "Grupo Bilhon"), SchedulingURL: env("BIA_SCHEDULING_URL", "https://example.invalid/agendar"), TimeZone: env("BIA_TIMEZONE", "America/Cuiaba"), QuietStart: 8, QuietEnd: 20, FollowupAfter: 2 * time.Hour, Reminders: []time.Duration{24 * time.Hour, 2 * time.Hour, 15 * time.Minute}}
	c.Policy.TestPhone = c.TestPhone
	c.Policy.DisableFollowups = os.Getenv("BIA_DISABLE_FOLLOWUPS") == "true"
	if c.MessagingProvider == "twilio" && (os.Getenv("BIA_SCHEDULING_URL") == "" || c.Policy.SchedulingURL == "https://example.invalid/agendar") {
		c.Policy.SchedulingURL = ""
	}
	if c.DSN == "" {
		return c, errors.New("BIA_DATABASE_URL is required")
	}
	if len(c.Token) < 32 {
		return c, errors.New("BIA_API_TOKEN must have at least 32 characters")
	}
	if c.Mode != "shadow" && c.Mode != "live" {
		return c, errors.New("BIA_MODE must be shadow or live")
	}
	if _, e := time.LoadLocation(c.Policy.TimeZone); e != nil {
		return c, errors.New("invalid BIA_TIMEZONE")
	}
	if c.Policy.SchedulingURL != "" && !domain.ValidHTTPS(c.Policy.SchedulingURL) {
		return c, errors.New("BIA_SCHEDULING_URL must be HTTPS")
	}
	if c.OpenAIKey != "" && c.OpenAIModel == "" {
		return c, errors.New("set OPENAI_MODEL explicitly when using OPENAI_API_KEY")
	}
	if c.Mode == "live" && c.MessagingProvider == "gateway" && (c.GatewayURL == "" || c.GatewayToken == "" || c.Policy.SchedulingURL == "https://example.invalid/agendar") {
		return c, errors.New("live mode requires a verified messaging gateway and a real scheduling URL")
	}
	if c.Mode == "live" && c.OpenAIKey == "" {
		return c, errors.New("live mode requires OPENAI_API_KEY and OPENAI_MODEL")
	}
	if c.GatewayURL != "" && !domain.ValidHTTPS(c.GatewayURL) {
		return c, errors.New("BIA_GATEWAY_URL must be HTTPS")
	}
	if v := os.Getenv("BIA_WORKERS"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 8 {
			return c, errors.New("BIA_WORKERS must be 1..8")
		}
		c.Workers = n
	}
	v, e := loadVoice(c.Mode, c.OpenAIKey)
	if e != nil {
		return c, e
	}
	c.Voice = v
	if c.MessagingProvider == "twilio" && (v.Account == "" || v.AuthToken == "" || v.Sender == "" || v.PublicURL == "") {
		return c, errors.New("Twilio messaging requires credentials, sender and public URL")
	}
	if c.MessagingProvider == "twilio" {
		if e := validateTwilioEndpoint(v); e != nil {
			return c, e
		}
	}
	return c, nil
}
