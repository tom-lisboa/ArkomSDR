package providers

import (
	"context"
	"net/url"
	"time"

	"arkom/bia/internal/domain"
)

// SendVideo waits for the opening to be delivered before publishing the case.
// It shares the media ledger, so a lost POST response is never sent twice.
func (t *TwilioMessaging) SendVideo(ctx context.Context, key string, s domain.State, mediaURL, openingID string, now time.Time) (Receipt, error) {
	if t.Pool == nil || !domain.ValidHTTPS(mediaURL) || openingID == "" {
		return Receipt{}, &Failure{Code: "intro_video_invalid_request"}
	}
	var id, phone string
	err := t.Pool.QueryRow(ctx, `SELECT COALESCE(sid,''),phone FROM bia_go.twilio_messages WHERE id=$1`, openingID).Scan(&id, &phone)
	if err != nil || id == "" {
		return Receipt{}, &Deferred{Code: "intro_video_awaiting_opening", After: 5 * time.Second}
	}
	if phone != s.Profile.Phone {
		return Receipt{}, &Failure{Code: "intro_video_opening_mismatch"}
	}
	m, err := t.Fetch(ctx, id)
	if err != nil {
		return Receipt{}, &Deferred{Code: "intro_video_opening_status_unavailable", After: 10 * time.Second}
	}
	if m.SID != id || m.From != "whatsapp:"+t.Sender || m.To != "whatsapp:"+s.Profile.Phone {
		return Receipt{}, &Failure{Code: "intro_video_opening_mismatch"}
	}
	switch m.Status {
	case "delivered", "read":
	case "failed", "undelivered", "canceled":
		return Receipt{}, &Failure{Code: "intro_video_opening_not_delivered"}
	default:
		return Receipt{}, &Deferred{Code: "intro_video_awaiting_opening_delivery", After: 5 * time.Second}
	}
	form := url.Values{"MediaUrl": {mediaURL}}
	if !messageWindowOpen(s, now) {
		if err := t.templateReady(ctx, t.IntroVideoContentSID); err != nil {
			return Receipt{}, err
		}
		form = url.Values{"ContentSid": {t.IntroVideoContentSID}}
	}
	return t.send(ctx, key, s, "video\x00"+mediaURL, form, now)
}
