package quickreply

import (
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
)

func TestNoReplyButtonsPauseAndCannotPauseAReplacementBooking(t *testing.T) {
	now := time.Now().UTC()
	s := domain.State{ID: "lead", CreatedAt: now, Profile: domain.Form{Consent: true}}
	k := Signer{Key: strings.Repeat("test-key", 5)}
	buttons := k.Options(s, "", "no_reply_15m", domain.Policy{}, now)
	if len(buttons) != 3 || buttons[2].Title != "Agora não" {
		t.Fatal(buttons)
	}
	e := domain.Event{ButtonPayload: buttons[2].Payload, At: now}
	d, handled := k.Resolve(s, &e, domain.Policy{}, now)
	if !handled || d.Action != "pause" {
		t.Fatal(d)
	}
	s.Booking = &domain.Booking{ID: "new-booking", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
	e = domain.Event{ButtonPayload: buttons[2].Payload, At: now}
	d, handled = k.Resolve(s, &e, domain.Policy{}, now)
	if !handled || d.Action == "pause" {
		t.Fatal("old commercial button affected new booking", d)
	}
}
