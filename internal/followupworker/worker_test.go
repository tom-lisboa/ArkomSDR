package followupworker

import (
	"arkom/bia/internal/domain"
	"testing"
	"time"
)

func TestGeneralTestingKeepsScheduledOutreachDisabled(t *testing.T) {
	now := time.Now()
	p := domain.Policy{DisableFollowups: true, QuietStart: 0, QuietEnd: 24}
	if _, why := NextContact(p, domain.State{}, domain.Effect{}, now); why != "scheduled_outreach_disabled_during_test" {
		t.Fatal(why)
	}
	p.DisableFollowups = false
	if _, why := NextContact(p, domain.State{}, domain.Effect{}, now); why != "" {
		t.Fatal(why)
	}
}
