package quickreply

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"arkom/bia/internal/domain"
)

func TestCadenceButtonsMatchApprovedAssetAndResolveEveryChoice(t *testing.T) {
	now := time.Now().UTC()
	k := Signer{Key: strings.Repeat("test-key", 5)}
	for purpose := range domain.ProactivePatterns {
		s := domain.State{ID: "lead", CreatedAt: now, Profile: domain.Form{Consent: true}}
		if domain.IsMeetingCheckin(purpose) {
			s.Booking = &domain.Booking{ID: "meeting", Start: now.Add(time.Hour), End: now.Add(105 * time.Minute)}
		}
		buttons := k.Options(s, "", purpose, domain.Policy{}, now)
		raw, err := os.ReadFile("../../deploy/cadence/" + purpose + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var asset struct {
			Types map[string]struct{ Actions []struct{ Title string } } `json:"types"`
		}
		if err := json.Unmarshal(raw, &asset); err != nil {
			t.Fatal(err)
		}
		actions := asset.Types["twilio/quick-reply"].Actions
		if len(buttons) != 3 || len(buttons) != len(actions) {
			t.Fatal("missing buttons", purpose)
		}
		for i, b := range buttons {
			if b.Title != actions[i].Title || len(b.Payload) > 200 {
				t.Fatal("button mismatch", purpose, b)
			}
			e := domain.Event{ButtonPayload: b.Payload, At: now}
			d, handled := k.Resolve(s, &e, domain.Policy{}, now)
			want := []string{"offer_slots", "reply", "pause"}[i]
			if domain.IsMeetingCheckin(purpose) {
				want = []string{"confirm", "offer_slots", "reply"}[i]
			}
			if !handled || d.Action != want || d.Reason == "quick_reply_stale" {
				t.Fatal("choice not resolved", purpose, i, d)
			}
		}
	}
}

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
