package config

import (
	"arkom/bia/internal/domain"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Voice struct {
	Address, PublicURL, Account, AuthToken, Sender, ContentSID, Model, Voice, TranscriptionModel string
	Enabled, AutoRequest, ApprovedOutsideSession                                                 bool
	MaxDuration                                                                                  time.Duration
}

func validateTwilioEndpoint(v Voice) error {
	u, e := url.Parse(v.PublicURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("Twilio public URL must be an HTTPS origin")
	}
	phone, e := domain.NormalizePhone(v.Sender)
	if e != nil || phone != v.Sender || !strings.HasPrefix(v.Account, "AC") || len(v.Account) != 34 || v.AuthToken == "" {
		return errors.New("invalid Twilio sender or credentials")
	}
	return nil
}

func loadVoice(mode, key string) (Voice, error) {
	v := Voice{Address: env("BIA_VOICE_ADDR", "127.0.0.1:8081"), PublicURL: strings.TrimRight(os.Getenv("BIA_VOICE_PUBLIC_URL"), "/"), Account: os.Getenv("TWILIO_ACCOUNT_SID"), AuthToken: os.Getenv("TWILIO_AUTH_TOKEN"), Sender: os.Getenv("TWILIO_WHATSAPP_FROM"), ContentSID: os.Getenv("TWILIO_CALL_PERMISSION_CONTENT_SID"), Model: os.Getenv("OPENAI_REALTIME_MODEL"), Voice: env("OPENAI_REALTIME_VOICE", "marin"), TranscriptionModel: os.Getenv("OPENAI_REALTIME_TRANSCRIPTION_MODEL"), Enabled: os.Getenv("BIA_VOICE_ENABLED") == "true", AutoRequest: os.Getenv("BIA_VOICE_AUTO_REQUEST") == "true", ApprovedOutsideSession: os.Getenv("BIA_VOICE_APPROVED_OUTSIDE_SESSION") == "true", MaxDuration: 5 * time.Minute}
	if v.Enabled {
		if mode != "live" || key == "" || v.Model == "" || v.TranscriptionModel == "" || v.AuthToken == "" || !strings.HasPrefix(v.Account, "AC") || len(v.Account) != 34 || !strings.HasPrefix(v.ContentSID, "HX") || len(v.ContentSID) != 34 {
			return v, errors.New("voice requires live mode, OpenAI realtime models, and Twilio credentials/template")
		}
		if _, e := domain.NormalizePhone(v.Sender); e != nil {
			return v, errors.New("TWILIO_WHATSAPP_FROM must be E.164 without whatsapp prefix")
		}
		u, e := url.Parse(v.PublicURL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return v, errors.New("BIA_VOICE_PUBLIC_URL must be a public HTTPS origin without a path")
		}
	}
	return v, nil
}
