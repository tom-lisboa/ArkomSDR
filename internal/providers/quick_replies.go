package providers

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

// Dedicated approved templates cover business-initiated messages. Generic
// templates are used exclusively inside an inbound WhatsApp service window.
type QuickReplyConfig struct {
	Enabled                                                   bool
	Session1, Session2, Session3                              string
	Form, Context, ContextQuestion, Preparation, MeetingStart string
}

func (t *TwilioMessaging) sendButtons(ctx context.Context, key string, s domain.State, text, purpose string, now time.Time) (Receipt, bool, error) {
	if !t.QuickReplies.Enabled {
		return Receipt{}, false, nil
	}
	p := domain.Policy{TimeZone: t.TimeZone}
	buttons := t.Buttons.Options(s, text, purpose, p, now)
	if len(buttons) == 0 {
		return Receipt{}, false, nil
	}
	sid := ""
	variables := map[string]string{}
	if messageWindowOpen(s, now) {
		sid = map[int]string{1: t.QuickReplies.Session1, 2: t.QuickReplies.Session2, 3: t.QuickReplies.Session3}[len(buttons)]
		// ContentVariables must not contain control whitespace (Twilio 21656).
		// Approved templates retain their literal paragraph breaks.
		variables["1"] = strings.Join(strings.Fields(text), " ")
		for i, b := range buttons {
			variables[strconv.Itoa(2+i*2)] = b.Title
			variables[strconv.Itoa(3+i*2)] = b.Payload
		}
	} else {
		var o domain.Opening
		switch {
		case domain.IsProactive(purpose):
			o = domain.ProactiveOpening(s, p, purpose)
			sid = t.ProactiveContentSIDs[purpose]
		case purpose == "form_contact":
			o = domain.ContactOpening(s, p, now)
			sid = t.QuickReplies.Form
		case purpose == "form_context":
			o = domain.ContactContextOpening(s, p, now)
			sid = t.QuickReplies.Context
			if o.Kind == "booked_context_question" {
				sid = t.QuickReplies.ContextQuestion
			}
		case purpose == "meeting_preparation":
			o = domain.PreparationOpening(s)
			sid = t.QuickReplies.Preparation
		case purpose == "meeting_start":
			o = domain.MeetingStartOpening(s, p)
			sid = t.QuickReplies.MeetingStart
		default:
			return Receipt{}, false, nil
		}
		if o.Text != text {
			return Receipt{}, true, &Failure{Code: "button_template_text_mismatch"}
		}
		for k, v := range o.Variables {
			variables[k] = v
		}
		first := len(variables) + 1
		for i, b := range buttons {
			variables[strconv.Itoa(first+i)] = b.Payload
		}
		if err := t.templateReady(ctx, sid); err != nil {
			return Receipt{}, true, err
		}
	}
	if sid == "" {
		return Receipt{}, true, &Deferred{Code: "button_template_not_configured", After: time.Minute}
	}
	raw, _ := json.Marshal(variables)
	receipt, err := t.send(ctx, key, s, text, url.Values{"ContentSid": {sid}, "ContentVariables": {string(raw)}}, now)
	return receipt, true, err
}
