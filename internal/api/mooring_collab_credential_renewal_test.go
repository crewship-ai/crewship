package api

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Same SQL session and revocation epoch, separately authenticated JWT horizons.
// BOOTTIME is controlled; this is policy qualification, not a browser/native run.
func renewedCredentialRig(t *testing.T) *continuityRig {
	t.Helper()
	f := newContinuityRig(t, true)
	setContinuityCredential(t, f, 30*time.Second)
	var err error
	_, f.admission, err = f.s.issue(f.ctx, f.owner, true)
	if err != nil {
		t.Fatal(err)
	}
	f.advance(t, 15*time.Second)
	setContinuityCredential(t, f, time.Hour)
	return f
}
func setContinuityCredential(t *testing.T, f *continuityRig, ttl time.Duration) {
	t.Helper()
	u := &AuthUser{ID: f.r.userID, SessionID: "admission-source", CredentialExpiresAt: time.Now().Add(ttl)}
	source, err := captureOriginalHumanCredential(f.ctx, f.r.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	f.ctx = auth.WithOriginalCredential(context.WithValue(f.ctx, ctxUser, u), *source)
}
func TestCollaborationHumanRenewedCredentialCrossesOriginalExpiry(t *testing.T) {
	f := renewedCredentialRig(t)
	original := f.admission.source
	if err := f.bound(func(ctx context.Context) error {
		publish, err := f.s.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			return err
		}
		return publish()
	}); err != nil {
		t.Fatal("fresh same-session authentication cannot renew", err)
	}
	f.advance(t, 30*time.Second)
	if err := f.check(t, true); err != nil {
		t.Fatal("original JWT expired despite authenticated renewal", err)
	}
	if original != f.admission.source {
		t.Fatal("original attribution changed")
	}
}
func TestCollaborationHumanRenewedCredentialRejectsRevocationAndRoleRemoval(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`,
		`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`,
	} {
		t.Run(mutation, func(t *testing.T) {
			f := renewedCredentialRig(t)
			old := f.admission.currentDeadline()
			if err := f.bound(func(ctx context.Context) error {
				publish, err := f.s.prepareHumanContinuity(ctx, f.owner)
				if err != nil {
					t.Fatal("fresh renewal preparation", err)
				}
				if mutation[0] == 'D' {
					mustExec(t, f.r.db, mutation, f.r.wsID, f.r.userID)
				} else {
					mustExec(t, f.r.db, mutation)
				}
				if err = publish(); err == nil {
					t.Fatal("revoked authority renewed")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if f.admission.currentDeadline() != old {
				t.Fatal("denied renewal changed horizon")
			}
			if f.check(t, true) == nil {
				t.Fatal("revoked original write allowed")
			}
		})
	}
}
func TestCollaborationHumanRenewedCredentialDoesNotReviveLateProof(t *testing.T) {
	f := renewedCredentialRig(t)
	if err := f.bound(func(ctx context.Context) error {
		publish, err := f.s.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		f.advance(t, 30*time.Second)
		if publish() == nil {
			t.Fatal("late response revived expired original")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationHumanRenewedCredentialKeepsSealedInflightWrite(t *testing.T) {
	f := renewedCredentialRig(t)
	registry, err := newCollaborationSubmissionRegistry(f.s)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := f.s.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	change := submissionTestChange(t, f.s.scope, "pending-renewal", "", f.admission.participant)
	if err := f.bound(func(ctx context.Context) error {
		return registry.run(ctx, binding, func(ctx context.Context) error { return registry.sealHuman(ctx, []mooring.BatchChange{change}) })
	}); err != nil {
		t.Fatal(err)
	}
	authority := registry.bind(f.owner, binding)
	if err := f.bound(func(ctx context.Context) error {
		publish, err := f.s.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			return err
		}
		return publish()
	}); err != nil {
		t.Fatal(err)
	}
	f.advance(t, 30*time.Second)
	check := func() error {
		return registry.run(f.ctx, binding, func(ctx context.Context) error {
			tx, err := f.r.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return authority(ctx, tx, submissionTestAccess(f.owner, change, mooring.SessionCurrentWrite))
		})
	}
	if err := check(); err != nil {
		t.Fatal("sealed original lost current write after renewal", err)
	}
	mustExec(t, f.r.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID)
	if err := check(); err == nil {
		t.Fatal("renewed in-flight write bypassed removal")
	}
}

func TestCollaborationHumanRenewedCredentialSurvivesOriginalWallExpiry(t *testing.T) {
	f := newContinuityRig(t, true)
	setContinuityCredential(t, f, 2*time.Second)
	var err error
	_, f.admission, err = f.s.issue(f.ctx, f.owner, true)
	if err != nil {
		t.Fatal(err)
	}
	expiry, _ := f.admission.source.Expiry()
	setContinuityCredential(t, f, time.Hour)
	if err := f.bound(func(ctx context.Context) error {
		publish, err := f.s.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			return err
		}
		return publish()
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(expiry) + 20*time.Millisecond)
	if err := f.check(t, true); err != nil {
		t.Fatal("renewed admission rejected solely for original wall expiry", err)
	}
}
