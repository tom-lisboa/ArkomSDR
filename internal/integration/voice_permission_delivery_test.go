package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkom/bia/internal/providers"
	"arkom/bia/internal/store"
	"arkom/bia/internal/voiceworker"
)

type deliveryVoice struct {
	fakeVoice
	delivery providers.TwilioMessage
	checks   int
}

func TestVoicePermissionCallbackWinsOverDeliveryPoll(t *testing.T) {
	db, repo, w, _, s := voiceFixture(t, "live")
	step(t, w, true)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET due_at=now() WHERE id='voice-one'`); err != nil {
		t.Fatal(err)
	}
	r, err := repo.ClaimPermission(ctx, "poll-lease")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Permission(ctx, "approval-during-poll", s.Profile.Phone, voiceSender, true, false, r.Created); err != nil {
		t.Fatal(err)
	}
	if err := repo.Defer(ctx, r, time.Now().Add(time.Minute)); !errors.Is(err, store.ErrLease) {
		t.Fatal("stale status check overwrote the approval", err)
	}
	voiceState(t, repo, "ready")
}

func TestDemoCallUsesExistingPermanentPermission(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "revoked"}[revoked], func(t *testing.T) {
			db, _, s := demoFixture(t)
			ctx := context.Background()
			_, err := db.Pool.Exec(ctx, `INSERT INTO bia_go.voice_requests(id,lead_id,mode,sender,purpose,state,permanent,permission_at,permission_sid,revoked_at) VALUES('old-call',$1,'live',$2,'confirm','completed',true,now(),'MM11111111111111111111111111111111',CASE WHEN $3 THEN now() END)`, s.ID, voiceSender, revoked)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = db.QueueTestReminder(ctx, "next-reminder", "demo-form", s.Profile.Phone, "call", "live", true, true, false)
			if revoked {
				if err == nil || err.Error() != "permission_request_limit" {
					t.Fatal("revoked permission reused", err)
				}
			} else if err != nil {
				t.Fatal("existing permission incorrectly requires another button", err)
			}
		})
	}
}

func (v *deliveryVoice) PermissionStatus(context.Context, string, string, string) (providers.TwilioMessage, error) {
	v.checks++
	return v.delivery, nil
}

func TestVoiceReconcilesQueuedPermissionDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, status, want string
		code               int
	}{{"permanent", "failed", "ready", 37010}, {"rejected", "undelivered", "failed", 63016}, {"pending", "sent", "awaiting_permission", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			db, repo, w, _, s := voiceFixture(t, "live")
			v := &deliveryVoice{delivery: providers.TwilioMessage{Status: tc.status}}
			if tc.code != 0 {
				v.delivery.ErrorCode = &tc.code
			}
			w.Provider = v
			ctx := context.Background()
			step(t, w, true)
			voiceState(t, repo, "awaiting_permission")
			if _, err := db.Pool.Exec(ctx, `UPDATE bia_go.voice_requests SET due_at=now() WHERE id='voice-one'`); err != nil {
				t.Fatal(err)
			}
			step(t, w, true)
			r := voiceState(t, repo, tc.want)
			if v.permissions != 1 || v.checks != 1 || v.dials != 0 {
				t.Fatalf("permission request repeated or premature call: %+v", v)
			}
			if tc.code != 37010 {
				if r.PermissionAt != nil || r.Permanent {
					t.Fatal("delivery did not authorize calling")
				}
				return
			}
			if !r.Permanent || r.PermissionAt == nil || r.PermissionExpires != nil {
				t.Fatal("permanent platform approval not retained", r)
			}
			step(t, w, true)
			r = voiceState(t, repo, "queued")
			if _, err := repo.Active(ctx, r); err != nil {
				t.Fatal("permanent permission blocked the voice session", err)
			}
			if err := repo.BeginStream(ctx, r, voiceCall); err != nil {
				t.Fatal("permanent permission blocked audio", err)
			}
			step(t, w, false)
			if v.dials != 1 {
				t.Fatal("duplicate call", v.dials)
			}
			if err := repo.Status(ctx, r.ID, voiceCall, "completed", 10); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Create(ctx, "voice-two", s.ID, "live", voiceSender, "confirm"); err != nil {
				t.Fatal(err)
			}
			next, err := repo.Get(ctx, "voice-two")
			if err != nil || next.State != "ready" || !next.Permanent {
				t.Fatal("next reminder lost permanent permission", next, err)
			}
			if err := repo.Permission(ctx, "revoke-permanent", s.Profile.Phone, voiceSender, false, false, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Active(ctx, r); !errors.Is(err, voiceworker.ErrBlocked) {
				t.Fatal("revocation ignored", err)
			}
			next, err = repo.Get(ctx, "voice-two")
			if err != nil || next.State != "rejected" {
				t.Fatal("pending reminder survived revocation", next, err)
			}
		})
	}
}
