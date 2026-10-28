package domain

import (
	"testing"
	"time"
)

func TestCustomerContextUsesCurrentMeetingAndPreservesZero(t *testing.T) {
	zero := int64(0)
	s := State{Profile: Form{Name: "Ana", Company: "Acme", Revenue: &zero, TimeZone: "America/Cuiaba"}}
	original := time.Date(2026, 10, 29, 2, 0, 0, 0, time.UTC)
	b := &Booking{Start: original}
	c := CustomerContext(s, b, Policy{})
	if c.Name != "Ana" || c.Company != "Acme" || c.Revenue == nil || *c.Revenue != 0 || c.MeetingDate.Day() != 28 || c.MeetingDate.Hour() != 22 {
		t.Fatal(c)
	}
	next := original.Add(24 * time.Hour)
	b.Start = next
	c = CustomerContext(s, b, Policy{})
	if !c.MeetingDate.Equal(next) {
		t.Fatal("stale date")
	}
	s.Profile.Revenue = nil
	c = CustomerContext(s, nil, Policy{})
	if c.Revenue != nil || c.MeetingDate != nil {
		t.Fatal("unknown fields invented")
	}
}
func TestFormEnrichmentPreservesCompanyAndRevenue(t *testing.T) {
	zero := int64(0)
	now := time.Now()
	s := State{ID: "lead", Profile: Form{Name: "Ana", Phone: "+5565999999900", Company: "Acme", Revenue: &zero}}
	f := Form{Name: "Ana", Phone: s.Profile.Phone, Consent: true}
	r := Reduce(s, Event{ID: "update", Kind: "form.submitted", At: now, Form: &f}, Decision{}, nil, Policy{}, now)
	if r.State.Profile.Company != "Acme" || r.State.Profile.Revenue == nil || *r.State.Profile.Revenue != 0 {
		t.Fatal(r.State.Profile)
	}
}
