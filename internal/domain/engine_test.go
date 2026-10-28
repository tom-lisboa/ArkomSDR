package domain

import (
	"strings"
	"testing"
	"time"
)

func fixture() (State, Policy, time.Time) {
	now := time.Date(2026, 10, 27, 17, 0, 0, 0, time.UTC)
	p := Policy{Brand: "Grupo Bilhon", SchedulingURL: "https://example.com/agendar", TimeZone: "America/Cuiaba", QuietStart: 8, QuietEnd: 20, FollowupAfter: 2 * time.Hour, Reminders: []time.Duration{24 * time.Hour, 2 * time.Hour, 15 * time.Minute}}
	s := State{ID: "lead_test", Owner: "go", CreatedAt: now, Profile: Form{Name: "Pessoa Exemplo", Phone: "+5565999999999", Consent: true, TimeZone: "America/Cuiaba", Channel: "gateway"}}
	return s, p, now
}
func count(r Result, kind, purpose string) int {
	n := 0
	for _, e := range r.Effects {
		if e.Kind == kind && (purpose == "" || e.Purpose == purpose) {
			n++
		}
	}
	return n
}
func TestZeroRevenueReceivesImmediateContact(t *testing.T) {
	_, p, now := fixture()
	zero := int64(0)
	for _, revenue := range []*int64{nil, &zero} {
		f := Form{Name: "Pessoa", Phone: "+5565999999999", Consent: true, Revenue: revenue}
		r := Reduce(State{}, Event{ID: "form-1", LeadID: "lead", Kind: "form.submitted", At: now, Form: &f}, Decision{}, nil, p, now)
		if count(r, "send", "welcome") != 1 {
			t.Fatal("zero/missing revenue must receive welcome")
		}
		if !r.Effects[0].Due.Equal(now) {
			t.Fatal("first contact is not immediate")
		}
		if strings.Contains(r.Effects[0].Text, "grupo do WhatsApp") {
			t.Fatal("incorrect routing")
		}
		if count(Reduce(r.State, Event{ID: "form-2", Kind: "form.submitted", Form: &f}, Decision{}, nil, p, now), "send", "") != 0 {
			t.Fatal("re-registration sent duplicate welcome")
		}
	}
}
func TestConsentAndOptOutArePreserved(t *testing.T) {
	s, p, now := fixture()
	s.OptedOut = true
	f := s.Profile
	r := Reduce(s, Event{ID: "newform", Kind: "form.submitted", Form: &f, At: now}, Decision{}, nil, p, now)
	if !r.State.OptedOut || count(r, "send", "") != 0 {
		t.Fatal("re-registration undid opt-out")
	}
	f.Consent = false
	r = Reduce(State{}, Event{ID: "no-consent", Kind: "form.submitted", Form: &f, At: now}, Decision{}, nil, p, now)
	if count(r, "send", "") != 0 {
		t.Fatal("proactive contact without consent")
	}
}
func TestGuardCases(t *testing.T) {
	s, p, now := fixture()
	s.Booking = &Booking{ID: "booking", Provider: "local", Start: now.Add(48 * time.Hour), End: now.Add(49 * time.Hour), CloserID: 7, MeetingURL: "https://meet.example/real"}
	tests := []struct {
		name, text            string
		d                     Decision
		wantKind              string
		wantHuman, wantOptout bool
	}{
		{"explicit_stop", "não me mande mais mensagens", Decision{Action: "book"}, "crm", false, true},
		{"not_cancel", "não quero cancelar a reunião", Decision{Action: "cancel"}, "send", false, false},
		{"late_not_cancel", "vou chegar atrasado", Decision{Action: "cancel"}, "send", false, false},
		{"explicit_cancel", "quero cancelar a reunião", Decision{}, "cancel", false, false},
		{"confirmation", "confirmo minha presença", Decision{}, "send", false, false},
		{"invented_booking", "olá", Decision{Action: "reply", Message: "Reservei sua reunião para amanhã"}, "crm", true, false},
		{"invented_link", "olá", Decision{Action: "reply", Message: "Veja https://evil.example"}, "crm", true, false},
		{"automatic_reply", "Mensagem automática: agradece seu contato", Decision{Action: "reply", Message: "Oi"}, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Reduce(s, Event{ID: "e", Kind: "message.received", At: now, Text: tt.text}, tt.d, nil, p, now)
			if r.State.Human != tt.wantHuman || r.State.OptedOut != tt.wantOptout {
				t.Fatalf("unexpected state: %+v", r.State)
			}
			if tt.wantKind != "" && count(r, tt.wantKind, "") == 0 {
				t.Fatalf("expected %s: %+v", tt.wantKind, r)
			}
			if tt.wantKind == "" && len(r.Effects) != 0 {
				t.Fatal("bot loop")
			}
			if tt.name == "late_not_cancel" && count(r, "cancel", "") != 0 {
				t.Fatal("cancelled on lateness")
			}
		})
	}
}
func TestBookingRequiresOfferedSlotAndSameCloser(t *testing.T) {
	s, p, now := fixture()
	slot := Slot{ID: "S1", Provider: "local", CloserID: 9, Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)}
	s.Offered = []Slot{slot}
	s.Booking = &Booking{ID: "old", Provider: "local", CloserID: 7, Start: now.Add(time.Hour)}
	r := Reduce(s, Event{ID: "select", Kind: "message.received", At: now, SelectedSlotID: "S1"}, Decision{}, []Slot{slot}, p, now)
	if count(r, "book", "") != 0 || !r.State.Human {
		t.Fatal("changed closer")
	}
	s.Booking = nil
	r = Reduce(s, Event{ID: "select", Kind: "message.received", At: now, SelectedSlotID: "S1"}, Decision{}, []Slot{slot}, p, now)
	if count(r, "book", "") != 1 || count(r, "send", "") != 0 {
		t.Fatal("booking must not confirm before provider success")
	}
	r = Reduce(s, Event{ID: "fake", Kind: "message.received", At: now, Text: "oi"}, Decision{Action: "book", SlotID: "S1", Evidence: "oi"}, nil, p, now)
	if count(r, "book", "") != 0 {
		t.Fatal("hallucinated selection")
	}
}
func TestReschedulingInvalidatesOldRemindersEvenSameUID(t *testing.T) {
	s, p, now := fixture()
	b := Booking{ID: "same", Provider: "local", Start: now.Add(48 * time.Hour), End: now.Add(49 * time.Hour)}
	r := Reduce(s, Event{ID: "booked", Kind: "booking.created", Booking: &b, At: now}, Decision{}, nil, p, now)
	if count(r, "send", "reminder") != 3 {
		t.Fatal("missing reminder plan")
	}
	var reminder Effect
	for _, fx := range r.Effects {
		if fx.Purpose == "reminder" {
			reminder = fx
			break
		}
	}
	newBooking := b
	newBooking.Start = b.Start.Add(24 * time.Hour)
	r2 := Reduce(r.State, Event{ID: "moved", Kind: "booking.rescheduled", Booking: &newBooking, At: now.Add(time.Minute)}, Decision{}, nil, p, now)
	if ok, _ := Deliverable(r2.State, reminder, reminder.Due); ok {
		t.Fatal("old time reminder remained valid")
	}
	if count(Reduce(r2.State, Event{ID: "duplicate-booking", Kind: "booking.created", Booking: &newBooking, At: now.Add(2 * time.Minute)}, Decision{}, nil, p, now), "send", "") != 0 {
		t.Fatal("duplicate booking scheduled reminders")
	}
}
func TestSendTimeGuards(t *testing.T) {
	s, _, now := fixture()
	s.Booking = &Booking{ID: "b", Start: now.Add(time.Hour)}
	fx := Effect{Kind: "send", Purpose: "reminder", BookingID: "b", Expires: now.Add(time.Hour)}
	for _, state := range []State{func() State { x := s; x.OptedOut = true; return x }(), func() State { x := s; x.Human = true; return x }(), func() State { x := s; x.Paused = true; return x }(), func() State { x := s; x.Booking = nil; return x }()} {
		if ok, _ := Deliverable(state, fx, now); ok {
			t.Fatal("stale/blocked reminder sent")
		}
	}
	if ok, _ := Deliverable(s, fx, now.Add(2*time.Hour)); ok {
		t.Fatal("expired reminder sent")
	}
}
func TestNoShowRequiresEvidenceAndAllowsCorrection(t *testing.T) {
	s, p, now := fixture()
	s.Booking = &Booking{ID: "b", Start: now.Add(-time.Hour), End: now.Add(-15 * time.Minute)}
	r := Reduce(s, Event{ID: "noshow", Kind: "attendance.no_show", BookingID: "other", At: now}, Decision{}, nil, p, now)
	if count(r, "send", "recovery") != 0 {
		t.Fatal("wrong booking")
	}
	r = Reduce(s, Event{ID: "noshow", Kind: "attendance.no_show", BookingID: "b", At: now}, Decision{}, nil, p, now)
	if count(r, "send", "recovery") != 1 {
		t.Fatal("no recovery")
	}
	var recovery Effect
	for _, fx := range r.Effects {
		if fx.Purpose == "recovery" {
			recovery = fx
		}
	}
	r2 := Reduce(r.State, Event{ID: "corrected", Kind: "attendance.attended", BookingID: "b", At: now.Add(time.Minute)}, Decision{}, nil, p, now)
	if ok, _ := Deliverable(r2.State, recovery, now.Add(time.Minute)); ok {
		t.Fatal("recovery after confirmed attendance")
	}
}
func TestPreferenceDoesNotInventAvailability(t *testing.T) {
	s, p, now := fixture()
	r := Reduce(s, Event{ID: "pref", Kind: "schedule.preference", Preference: "sexta de manhã", At: now}, Decision{}, nil, p, now)
	if r.State.SchedulePreference == "" || count(r, "crm", "availability_requested") != 1 || count(r, "book", "") != 0 {
		t.Fatal("preference not recorded independently")
	}
}
func TestTimeZoneAndQuietHours(t *testing.T) {
	s, p, _ := fixture()
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	next := p.NextContact(now, s)
	if next.In(p.Zone(s)).Hour() != 8 || next.In(p.Zone(s)).Day() != 28 {
		t.Fatal(next)
	}
	s.Profile.TimeZone = "America/Sao_Paulo"
	if p.Zone(s).String() != "America/Sao_Paulo" {
		t.Fatal("ignored lead timezone")
	}
}
func TestNewMessageInvalidatesPendingResponse(t *testing.T) {
	s, _, now := fixture()
	s.LastInbound = now.Add(time.Second)
	if ok, _ := Deliverable(s, Effect{Kind: "send", Purpose: "reply", TriggerAt: now, Expires: now.Add(time.Hour)}, now.Add(2*time.Second)); ok {
		t.Fatal("sent old response after new message")
	}
}

func TestContactWindowOverrideOnlyForExplicitTestPhone(t *testing.T) {
	p := Policy{TimeZone: "America/Cuiaba", QuietStart: 8, QuietEnd: 20, TestPhone: "+5565999999900"}
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	test := State{Profile: Form{Phone: p.TestPhone}}
	if !p.NextContact(now, test).Equal(now) {
		t.Fatal("authorized test delayed")
	}
	other := State{Profile: Form{Phone: "+5565999999902"}}
	if !p.NextContact(now, other).After(now) {
		t.Fatal("quiet hours bypassed for another contact")
	}
}
