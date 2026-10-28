package domain

import (
	"arkom/bia/internal/conversation"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Event struct {
	SMSConsent *bool  `json:"sms_consent,omitempty"`
	SMSPhone   string `json:"sms_phone,omitempty"`
	// CalendarUnavailable is internal execution context, never accepted from webhooks.
	CalendarUnavailable bool                      `json:"-"`
	ConversationID      string                    `json:"conversation_id,omitempty"`
	Attachments         []conversation.Attachment `json:"attachments,omitempty"`
	Context             *conversation.Window      `json:"context,omitempty"`
	ID                  string                    `json:"id"`
	Kind                string                    `json:"kind"`
	LeadID              string                    `json:"lead_id"`
	At                  time.Time                 `json:"at"`
	Form                *Form                     `json:"form,omitempty"`
	Text                string                    `json:"text,omitempty"`
	AudioURL            string                    `json:"audio_url,omitempty"`
	SelectedSlotID      string                    `json:"selected_slot_id,omitempty"`
	Booking             *Booking                  `json:"booking,omitempty"`
	BookingID           string                    `json:"booking_id,omitempty"`
	Owner               string                    `json:"owner,omitempty"`
	Reason              string                    `json:"reason,omitempty"`
	Preference          string                    `json:"preference,omitempty"`
}

// Form intentionally has no website or team-size field. Revenue is optional,
// and zero is a valid value, never a reason to silently discard the contact.
type Form struct {
	SMSConsent         bool   `json:"sms_consent,omitempty"`
	SMSPhone           string `json:"sms_phone,omitempty"`
	SMSConsentVersion  string `json:"sms_consent_version,omitempty"`
	Company            string `json:"company,omitempty"`
	Name               string `json:"name"`
	Phone              string `json:"phone"`
	Email              string `json:"email,omitempty"`
	Goal               string `json:"goal,omitempty"`
	Revenue            *int64 `json:"revenue,omitempty"`
	TimeZone           string `json:"timezone,omitempty"`
	Consent            bool   `json:"consent"`
	ConsentTextVersion string `json:"consent_text_version,omitempty"`
	Source             string `json:"source,omitempty"`
	Channel            string `json:"channel"`
	ConversationID     string `json:"conversation_id,omitempty"`
	ProviderLeadID     string `json:"provider_lead_id,omitempty"`
}

type Booking struct {
	ID          string     `json:"id"`
	Provider    string     `json:"provider"`
	Start       time.Time  `json:"start"`
	End         time.Time  `json:"end"`
	MeetingURL  string     `json:"meeting_url"`
	CloserID    int64      `json:"closer_id"`
	Group       bool       `json:"group"`
	SeatID      string     `json:"seat_id,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
}

type Slot struct {
	ID          string    `json:"id"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Provider    string    `json:"provider"`
	CloserID    int64     `json:"closer_id"`
	EventTypeID int64     `json:"event_type_id"`
	Group       bool      `json:"group"`
}

type Turn struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type State struct {
	LastPreparationAt  *time.Time `json:"last_preparation_at,omitempty"`
	LastFormBookingID  string     `json:"last_form_booking_id,omitempty"`
	ID                 string     `json:"id"`
	Profile            Form       `json:"profile"`
	Owner              string     `json:"owner"`
	Stage              string     `json:"stage"`
	CreatedAt          time.Time  `json:"created_at"`
	LastInbound        time.Time  `json:"last_inbound"`
	FirstContactAt     *time.Time `json:"first_contact_at,omitempty"`
	LastFormContactAt  *time.Time `json:"last_form_contact_at,omitempty"`
	OptedOut           bool       `json:"opted_out"`
	Paused             bool       `json:"paused"`
	Human              bool       `json:"human"`
	Booking            *Booking   `json:"booking,omitempty"`
	PreviousBooking    *Booking   `json:"previous_booking,omitempty"`
	Offered            []Slot     `json:"offered"`
	OfferVersion       string     `json:"offer_version"`
	History            []Turn     `json:"history"`
	LastFollowup       *time.Time `json:"last_followup,omitempty"`
	LastEventAt        time.Time  `json:"last_event_at"`
	LastBookingEventAt time.Time  `json:"last_booking_event_at"`
	LastAttendanceAt   time.Time  `json:"last_attendance_at"`
	Reasons            []string   `json:"reasons"`
	SchedulePreference string     `json:"schedule_preference,omitempty"`
}

type Decision struct {
	Action       string   `json:"action"`
	Message      string   `json:"message"`
	SlotID       string   `json:"slot_id"`
	OfferSlotIDs []string `json:"offer_slot_ids,omitempty"`
	Evidence     string   `json:"evidence"`
	Reason       string   `json:"reason"`
}

type Effect struct {
	ManualReminder      bool      `json:"manual_reminder,omitempty"`
	MediaURL            string    `json:"media_url,omitempty"`
	ReplyRoot           string    `json:"reply_root,omitempty"`
	ReplyPart           int       `json:"reply_part,omitempty"`
	RemainingParts      []string  `json:"remaining_parts,omitempty"`
	VoiceRequestID      string    `json:"voice_request_id,omitempty"`
	ConversationID      string    `json:"conversation_id,omitempty"`
	ConversationVersion int64     `json:"conversation_version,omitempty"`
	ID                  string    `json:"id"`
	LeadID              string    `json:"lead_id"`
	Kind                string    `json:"kind"`
	Purpose             string    `json:"purpose"`
	Text                string    `json:"text,omitempty"`
	Due                 time.Time `json:"due"`
	Expires             time.Time `json:"expires"`
	BookingID           string    `json:"booking_id,omitempty"`
	BookingStart        time.Time `json:"booking_start,omitempty"`
	TriggerAt           time.Time `json:"trigger_at,omitempty"`
	OfferVersion        string    `json:"offer_version,omitempty"`
	Slot                *Slot     `json:"slot,omitempty"`
	Booking             *Booking  `json:"booking,omitempty"`
	Reason              string    `json:"reason,omitempty"`
}

type Result struct {
	State    State
	Effects  []Effect
	Decision Decision
}

type Policy struct {
	MeetingPreparation bool
	SMSReminders       bool
	WhatsAppPhone      string
	MeetingReminders   bool
	DisableFollowups   bool
	TestPhone          string
	Brand              string
	SchedulingURL      string
	TimeZone           string
	QuietStart         int
	QuietEnd           int
	FollowupAfter      time.Duration
	Reminders          []time.Duration
}

func (p Policy) Zone(s State) *time.Location {
	z := s.Profile.TimeZone
	if z == "" {
		z = p.TimeZone
	}
	l, err := time.LoadLocation(z)
	if err != nil {
		return time.UTC
	}
	return l
}

func (p Policy) NextContact(now time.Time, s State) time.Time {
	if p.TestPhone != "" && s.Profile.Phone == p.TestPhone {
		return now
	}
	l := now.In(p.Zone(s))
	if l.Hour() >= p.QuietStart && l.Hour() < p.QuietEnd {
		return now
	}
	next := time.Date(l.Year(), l.Month(), l.Day(), p.QuietStart, 0, 0, 0, l.Location())
	if l.Hour() >= p.QuietEnd {
		next = next.AddDate(0, 0, 1)
	}
	return next.UTC()
}

var phoneRE = regexp.MustCompile(`^\+[1-9][0-9]{9,14}$`)
var idRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,160}$`)

func NormalizePhone(s string) (string, error) {
	s = strings.NewReplacer(" ", "", "(", "", ")", "", "-", "").Replace(s)
	if !phoneRE.MatchString(s) {
		return "", errors.New("phone must use E.164, for example +5565999999999")
	}
	return s, nil
}

func LeadKey(phone string) string {
	h := sha256.Sum256([]byte(phone))
	return "lead_" + hex.EncodeToString(h[:16])
}

func ValidID(s string) bool { return idRE.MatchString(s) }

func ValidateEvent(e Event) error {
	if e.Context != nil {
		return errors.New("context is an internal worker field")
	}
	if len(e.ConversationID) > 200 || len(e.Attachments) > 5 {
		return errors.New("invalid conversation or attachments")
	}
	seen := map[string]bool{}
	for _, a := range e.Attachments {
		if !ValidID(a.ID) || seen[a.ID] || !ValidHTTPS(a.URL) {
			return errors.New("invalid attachment")
		}
		seen[a.ID] = true
		switch a.Kind {
		case "audio", "image", "document":
		default:
			return errors.New("unsupported media kind")
		}
	}
	if !ValidID(e.ID) || !ValidID(e.LeadID) || e.At.IsZero() {
		return errors.New("id, lead_id and at are required")
	}
	if len(e.Text) > 16000 || len(e.Reason) > 1000 {
		return errors.New("event text too large")
	}
	switch e.Kind {
	case "form.submitted", "form.contact_requested":
		if e.Form == nil {
			return errors.New("form required")
		}
		f := e.Form
		ph, err := NormalizePhone(f.Phone)
		if err != nil || LeadKey(ph) != e.LeadID {
			return errors.New("invalid form phone or lead_id")
		}
		if strings.TrimSpace(f.Name) == "" || len(f.Name) > 120 || len(f.Company) > 200 || len(f.Goal) > 2000 || len(f.Email) > 254 {
			return errors.New("invalid profile")
		}
		if f.Channel != "clickmax" && f.Channel != "datacrazy" && f.Channel != "gateway" {
			return errors.New("unsupported channel")
		}
		if f.Consent && strings.TrimSpace(f.ConsentTextVersion) == "" {
			return errors.New("consent_text_version required")
		}
		if f.SMSConsent && (SMSDestination(*f) == "" || f.SMSConsentVersion == "") {
			return errors.New("SMS consent requires its version and the original mobile number")
		}
		if f.Revenue != nil && *f.Revenue < 0 {
			return errors.New("revenue cannot be negative")
		}
		if f.TimeZone != "" {
			if _, err := time.LoadLocation(f.TimeZone); err != nil {
				return errors.New("invalid timezone")
			}
		}
	case "message.received":
		if strings.TrimSpace(e.Text) == "" && e.AudioURL == "" && e.SelectedSlotID == "" && len(e.Attachments) == 0 {
			return errors.New("message is empty")
		}
	case "booking.created", "booking.rescheduled":
		if e.Booking == nil || e.Booking.ID == "" || e.Booking.Provider == "" || !e.Booking.End.After(e.Booking.Start) {
			return errors.New("valid booking required")
		}
		if !(e.Booking.Provider == "local" && e.Booking.MeetingURL == "") && !ValidHTTPS(e.Booking.MeetingURL) {
			return errors.New("confirmed booking requires an HTTPS meeting URL")
		}
	case "booking.cancelled", "attendance.attended", "attendance.no_show":
		if e.BookingID == "" {
			return errors.New("booking_id required")
		}
	case "schedule.preference":
		if strings.TrimSpace(e.Preference) == "" || len(e.Preference) > 500 {
			return errors.New("schedule preference required, max 500 bytes")
		}
	case "contact.sms_consent":
		if e.SMSConsent == nil {
			return errors.New("explicit SMS consent required")
		}
		if phone, err := NormalizePhone(e.SMSPhone); err != nil || phone != e.SMSPhone {
			return errors.New("invalid SMS phone")
		}
	case "contact.opt_out", "human.takeover", "human.release", "cadence.resume":
	case "ownership.changed":
		if e.Owner != "go" && e.Owner != "n8n" && e.Owner != "shadow" {
			return errors.New("invalid owner")
		}
	default:
		return errors.New("unsupported event kind")
	}
	return nil
}

func AppendTurn(s *State, role, text string, at time.Time) {
	s.History = append(s.History, Turn{Role: role, Text: text, At: at})
	if len(s.History) > 30 {
		s.History = s.History[len(s.History)-30:]
	}
}
