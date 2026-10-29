package config

import (
	"arkom/bia/internal/casemedia"
	"arkom/bia/internal/domain"
	"arkom/bia/internal/providers"
	"arkom/bia/internal/remindermedia"
	"arkom/bia/internal/store"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	QuickReplies                                  providers.QuickReplyConfig
	MeetingStartContentSID                        string
	Specialists                                   bool
	DecisionsModel                                string
	IntroVideoEnabled                             bool
	IntroVideoContentSID                          string
	IntroVideo                                    casemedia.Asset
	PreparationContentSID                         string
	SMSFrom                                       string
	Availability                                  providers.DailyAvailability
	FormAutoContact                               bool
	FormContentSID                                string
	FormContextContentSID, FormContextQuestionSID string
	BookedContentSID, BookedQuestionSID           string
	ReminderAudio                                 remindermedia.Asset
	Window                                        store.WindowPolicy
	MessagingProvider, TestPhone                  string
	TestFormToken                                 string
	Voice                                         Voice
	RedisURL, MediaModel                          string
	Address, DSN, Token, Mode                     string
	OpenAIKey, OpenAIModel, AudioModel            string
	MediaHosts                                    []string
	Policy                                        domain.Policy
	Workers                                       int
	PerLeadDaily, GlobalHourly                    int
	GatewayURL, GatewayToken                      string
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func Load() (Config, error) {
	c := Config{RedisURL: env("BIA_REDIS_URL", "redis://127.0.0.1:6389/0"), MediaModel: os.Getenv("OPENAI_MEDIA_MODEL"), Address: env("BIA_ADDR", "127.0.0.1:8080"), DSN: os.Getenv("BIA_DATABASE_URL"), Token: os.Getenv("BIA_API_TOKEN"), Mode: env("BIA_MODE", "shadow"), OpenAIKey: os.Getenv("OPENAI_API_KEY"), OpenAIModel: os.Getenv("OPENAI_MODEL"), AudioModel: os.Getenv("OPENAI_AUDIO_MODEL"), GatewayURL: os.Getenv("BIA_GATEWAY_URL"), GatewayToken: os.Getenv("BIA_GATEWAY_TOKEN"), Workers: 4}
	c.Specialists = os.Getenv("BIA_SPECIALISTS_ENABLED") == "true"
	c.DecisionsModel = env("OPENAI_DECISIONS_MODEL", "gpt-6-luna")
	if c.Specialists && (c.OpenAIKey == "" || c.DecisionsModel != "gpt-6-luna") {
		return c, errors.New("specialists require OPENAI_API_KEY and OPENAI_DECISIONS_MODEL=gpt-6-luna")
	}
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
	c.FormAutoContact = os.Getenv("BIA_FORM_AUTO_CONTACT") == "true"
	c.QuickReplies.Enabled = os.Getenv("BIA_QUICK_REPLIES_ENABLED") == "true"
	for key, target := range map[string]*string{
		"TWILIO_QUICK_REPLY_1_SID":            &c.QuickReplies.Session1,
		"TWILIO_QUICK_REPLY_2_SID":            &c.QuickReplies.Session2,
		"TWILIO_QUICK_REPLY_3_SID":            &c.QuickReplies.Session3,
		"TWILIO_FORM_BUTTONS_SID":             &c.QuickReplies.Form,
		"TWILIO_CONTEXT_BUTTONS_SID":          &c.QuickReplies.Context,
		"TWILIO_CONTEXT_QUESTION_BUTTONS_SID": &c.QuickReplies.ContextQuestion,
		"TWILIO_PREPARATION_BUTTONS_SID":      &c.QuickReplies.Preparation,
		"TWILIO_MEETING_START_BUTTONS_SID":    &c.QuickReplies.MeetingStart,
	} {
		*target = os.Getenv(key)
		if *target != "" && (len(*target) != 34 || !strings.HasPrefix(*target, "HX")) {
			return c, errors.New("invalid " + key)
		}
		if c.QuickReplies.Enabled && *target == "" {
			return c, errors.New("quick replies require " + key)
		}
	}
	if c.QuickReplies.Enabled && c.MessagingProvider != "twilio" {
		return c, errors.New("quick replies require Twilio messaging")
	}
	c.FormContentSID = os.Getenv("TWILIO_FORM_CONTACT_CONTENT_SID")
	c.FormContextContentSID = os.Getenv("TWILIO_FORM_CONTEXT_CONTENT_SID")
	c.FormContextQuestionSID = os.Getenv("TWILIO_FORM_CONTEXT_QUESTION_CONTENT_SID")
	c.PreparationContentSID = os.Getenv("TWILIO_PREPARATION_CONTENT_SID")
	c.MeetingStartContentSID = os.Getenv("TWILIO_MEETING_START_CONTENT_SID")
	c.BookedContentSID = os.Getenv("TWILIO_BOOKED_CONTACT_CONTENT_SID")
	c.BookedQuestionSID = os.Getenv("TWILIO_BOOKED_QUESTION_CONTENT_SID")
	for _, sid := range []string{c.BookedContentSID, c.BookedQuestionSID, c.PreparationContentSID, c.MeetingStartContentSID, c.FormContextContentSID, c.FormContextQuestionSID} {
		if sid != "" && (len(sid) != 34 || !strings.HasPrefix(sid, "HX")) {
			return c, errors.New("invalid booked template SID")
		}
	}
	if c.FormContentSID != "" && (len(c.FormContentSID) != 34 || !strings.HasPrefix(c.FormContentSID, "HX")) {
		return c, errors.New("invalid TWILIO_FORM_CONTACT_CONTENT_SID")
	}
	if c.FormAutoContact && c.MessagingProvider != "twilio" {
		return c, errors.New("automatic public form contact requires Twilio messaging")
	}
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
	c.Availability = providers.DailyAvailability{Enabled: os.Getenv("BIA_LOCAL_CALENDAR_ENABLED") == "true", TimeZone: env("BIA_LOCAL_CALENDAR_TIMEZONE", c.Policy.TimeZone), Windows: env("BIA_LOCAL_CALENDAR_WINDOWS", "08:00-12:00,14:00-20:00"), DurationMinutes: 45, Days: 14, CloserID: 1, MeetingURL: os.Getenv("BIA_LOCAL_CALENDAR_MEETING_URL")}
	for key, target := range map[string]*int{"BIA_LOCAL_CALENDAR_DURATION_MINUTES": &c.Availability.DurationMinutes, "BIA_LOCAL_CALENDAR_DAYS": &c.Availability.Days} {
		if raw := os.Getenv(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return c, errors.New("invalid " + key)
			}
			*target = n
		}
	}
	if raw := os.Getenv("BIA_LOCAL_CALENDAR_CLOSER_ID"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return c, errors.New("invalid BIA_LOCAL_CALENDAR_CLOSER_ID")
		}
		c.Availability.CloserID = id
	}
	if err := c.Availability.Validate(); err != nil {
		return c, err
	}
	c.Policy.DisableFollowups = os.Getenv("BIA_DISABLE_FOLLOWUPS") == "true"
	c.Policy.MeetingPreparation = os.Getenv("BIA_MEETING_PREPARATION_ENABLED") == "true"
	c.Policy.MeetingReminders = os.Getenv("BIA_MEETING_REMINDERS_ENABLED") == "true"
	c.Policy.MeetingStart = os.Getenv("BIA_MEETING_START_ENABLED") == "true"
	c.Policy.SMSReminders = os.Getenv("BIA_SMS_REMINDERS_ENABLED") == "true"
	c.SMSFrom = os.Getenv("TWILIO_SMS_FROM")
	if c.SMSFrom != "" {
		if normalized, err := domain.NormalizePhone(c.SMSFrom); err != nil || normalized != c.SMSFrom {
			return c, errors.New("TWILIO_SMS_FROM must be E.164")
		}
	}
	c.ReminderAudio = remindermedia.Asset{Directory: env("BIA_REMINDER_AUDIO_DIR", ".local/media/reminders"), SHA256: os.Getenv("BIA_REMINDER_AUDIO_SHA256"), PublicURL: os.Getenv("BIA_VOICE_PUBLIC_URL")}
	c.IntroVideoEnabled = os.Getenv("BIA_INTRO_VIDEO_ENABLED") == "true"
	c.IntroVideoContentSID = os.Getenv("TWILIO_INTRO_VIDEO_CONTENT_SID")
	c.IntroVideo = casemedia.Asset{Directory: env("BIA_INTRO_VIDEO_DIR", ".local/media/cases"), SHA256: os.Getenv("BIA_INTRO_VIDEO_SHA256"), PublicURL: os.Getenv("BIA_VOICE_PUBLIC_URL")}
	if c.IntroVideo.SHA256 != "" && !remindermedia.ValidHash(c.IntroVideo.SHA256) {
		return c, errors.New("invalid BIA_INTRO_VIDEO_SHA256")
	}
	if c.IntroVideoContentSID != "" && (len(c.IntroVideoContentSID) != 34 || !strings.HasPrefix(c.IntroVideoContentSID, "HX")) {
		return c, errors.New("invalid TWILIO_INTRO_VIDEO_CONTENT_SID")
	}
	if c.ReminderAudio.SHA256 != "" && !remindermedia.ValidHash(c.ReminderAudio.SHA256) {
		return c, errors.New("invalid BIA_REMINDER_AUDIO_SHA256")
	}
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
	if c.IntroVideoEnabled && (c.MessagingProvider != "twilio" || c.IntroVideoContentSID == "" || !c.IntroVideo.Ready()) {
		return c, errors.New("intro video requires Twilio, a verified MP4 and its approved template")
	}
	c.Policy.WhatsAppPhone = v.Sender
	if c.Policy.MeetingStart && (c.MessagingProvider != "twilio" || c.MeetingStartContentSID == "" || c.TestPhone != "") {
		return c, errors.New("meeting start requires Twilio, its approved template and unrestricted recipient configuration")
	}
	if c.Policy.MeetingPreparation && (c.MessagingProvider != "twilio" || c.PreparationContentSID == "" || c.TestPhone != "") {
		return c, errors.New("meeting preparation requires Twilio, an approved preparation template and unrestricted recipient configuration")
	}
	if c.Policy.SMSReminders && (c.SMSFrom == "" || v.Account == "" || v.AuthToken == "" || c.TestPhone != "") {
		return c, errors.New("SMS reminders require a configured SMS sender, Twilio credentials and unrestricted recipient configuration")
	}
	if c.Policy.MeetingReminders && (c.TestPhone != "" || c.MessagingProvider != "twilio" || !v.Enabled || c.ReminderAudio.SHA256 == "" || !c.ReminderAudio.Ready()) {
		return c, errors.New("meeting reminders require Twilio voice, a verified local OGG/Opus audio file and unrestricted recipient configuration")
	}
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
