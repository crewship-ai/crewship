package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Real canonical SQL/auth policy and BOOTTIME fixture. This does not assert a
// negotiated Rust process: that boundary is exercised separately by live/actual
// tests. A missing continuity capability is an explicit baseline test failure,
// not a compile failure, fake grant or silent skip.
type continuityProofProvider interface {
	prepareHumanContinuity(context.Context, mooring.OwnerLease) (func() error, error)
}

type continuityRig struct {
	r         *pagesTransferRig
	s         *collaborationHumanRegistry
	owner     mooring.OwnerLease
	ctx       context.Context
	clock     *collaborationAdmissionTestClock
	admission collaborationHumanAdmission
}

func newContinuityRig(t *testing.T, write bool) *continuityRig {
	t.Helper()
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	_, admission, err := s.issue(ctx, owner, write)
	if err != nil {
		t.Fatal(err)
	}
	return &continuityRig{r, s, owner, ctx, clock, admission}
}
func (f *continuityRig) provider(t *testing.T) continuityProofProvider {
	t.Helper()
	p, ok := any(f.s).(continuityProofProvider)
	if !ok {
		t.Fatal("authenticated admission continuity is absent; captured five-minute admission cannot be refreshed")
	}
	return p
}
func (f *continuityRig) bound(fn func(context.Context) error) error {
	r := httptest.NewRequest("POST", "/reauthorize", nil).WithContext(f.ctx)
	return f.s.runHumanOperation(r, f.admission.participant, fn)
}
func (f *continuityRig) advance(t *testing.T, duration time.Duration) {
	t.Helper()
	// Model the existing owner heartbeat while advancing only the test clock.
	// Keep its original60s lease; do not lengthen either production/test leases.
	for duration > 0 {
		step := min(duration, 20*time.Second)
		f.clock.now += step
		var err error
		f.owner, err = f.s.owners.Renew(f.ctx, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		duration -= step
	}
}
func (f *continuityRig) check(t *testing.T, write bool) error {
	t.Helper()
	return f.bound(func(ctx context.Context) error {
		tx, err := f.r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = f.s.checkCaptured(ctx, tx, f.admission, write, 0)
		return err
	})
}

func TestCollaborationHumanContinuityAcrossRepeatedHorizons(t *testing.T) {
	f := newContinuityRig(t, true)
	p := f.provider(t)
	original := f.admission
	for i := 0; i < 4; i++ {
		f.advance(t, 4*time.Minute)
		if err := f.bound(func(ctx context.Context) error {
			publish, err := p.prepareHumanContinuity(ctx, f.owner)
			if err != nil {
				return err
			}
			return publish()
		}); err != nil {
			t.Fatal("continuous authorization failed", i, err)
		}
		f.s.mu.Lock()
		f.s.sweepLocked(f.clock.boot, f.clock.now)
		_, retained := f.s.admissions[original.participant]
		f.s.mu.Unlock()
		if !retained {
			t.Fatal("original live participant swept after refresh")
		}
		if err := f.check(t, true); err != nil {
			t.Fatal("original in-flight author lost its unchanged binding", err)
		}
		if f.admission.source != original.source || f.admission.authorVector != original.authorVector || f.admission.participant != original.participant || f.admission.session != original.session || f.admission.deadline != original.deadline {
			t.Fatal("immutable origin/identity/initial horizon changed")
		}
	}
	if f.clock.now <= original.deadline {
		t.Fatal("test did not cross original horizon")
	}
}

func TestCollaborationHumanContinuityRevocationDuringRefresh(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "revoked_then_restored"}[restore], func(t *testing.T) {
			f := newContinuityRig(t, true)
			p := f.provider(t)
			if err := f.bound(func(ctx context.Context) error {
				publish, err := p.prepareHumanContinuity(ctx, f.owner)
				if err != nil {
					t.Fatal(err)
				}
				// SQL preparation has finished. Revoke in the commit→publish gap;
				// restoring the row must not restore its original authority epoch.
				mustExec(t, f.r.db, `UPDATE user_sessions SET revoked_at='2026-10-26T00:00:00Z' WHERE id='admission-source'`)
				if restore {
					mustExec(t, f.r.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id='admission-source'`)
				}
				if err := publish(); err == nil {
					t.Fatal("revoked original continuity was published")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.check(t, true); err == nil {
				t.Fatal("revoked original regained write")
			}
		})
	}
}

func TestCollaborationHumanContinuityDowngradeAndReader(t *testing.T) {
	t.Run("downgrade during proof", func(t *testing.T) {
		f := newContinuityRig(t, true)
		p := f.provider(t)
		if err := f.bound(func(ctx context.Context) error {
			publish, err := p.prepareHumanContinuity(ctx, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.r.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.r.wsID, f.r.userID)
			if err := publish(); err == nil {
				t.Fatal("downgraded writer refreshed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.check(t, true); err == nil {
			t.Fatal("old writer survived downgrade")
		}
	})
	t.Run("reader stays reader", func(t *testing.T) {
		f := newContinuityRig(t, false)
		p := f.provider(t)
		f.advance(t, 4*time.Minute)
		if err := f.bound(func(ctx context.Context) error {
			publish, err := p.prepareHumanContinuity(ctx, f.owner)
			if err != nil {
				return err
			}
			return publish()
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.check(t, false); err != nil {
			t.Fatal(err)
		}
		if err := f.check(t, true); err == nil {
			t.Fatal("reader acquired write through refresh")
		}
	})
}

func TestCollaborationHumanContinuityRefusesExpiredAndLateProof(t *testing.T) {
	for _, afterPrepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "already_expired", true: "expires_before_publish"}[afterPrepare], func(t *testing.T) {
			f := newContinuityRig(t, true)
			p := f.provider(t)
			if err := f.bound(func(ctx context.Context) error {
				var publish func() error
				if afterPrepare {
					var err error
					publish, err = p.prepareHumanContinuity(ctx, f.owner)
					if err != nil {
						t.Fatal(err)
					}
				}
				f.advance(t, 5*time.Minute+time.Second)
				if !afterPrepare {
					if _, err := p.prepareHumanContinuity(ctx, f.owner); err == nil {
						t.Fatal("expired admission revived")
					}
				} else if err := publish(); err == nil {
					t.Fatal("late proof revived expired admission")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := f.check(t, true); err == nil {
				t.Fatal("expired original can write")
			}
		})
	}
}

func TestCollaborationHumanContinuityBindsFreshIdentityAndScope(t *testing.T) {
	f := newContinuityRig(t, true)
	p := f.provider(t)
	if err := f.bound(func(ctx context.Context) error {
		source := *auth.OriginalCredentialFromContext(ctx)
		source.ID = "another-authenticated-session"
		if _, err := p.prepareHumanContinuity(auth.WithOriginalCredential(ctx, source), f.owner); err == nil {
			t.Fatal("refresh adopted a different credential")
		}
		foreign := f.owner
		foreign.Key.ArtifactID = "another-document"
		if _, err := p.prepareHumanContinuity(ctx, foreign); err == nil {
			t.Fatal("refresh selected another document")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationHumanContinuityCannotEscapeOperationOrPublishTwice(t *testing.T) {
	f := newContinuityRig(t, true)
	p := f.provider(t)
	if _, err := p.prepareHumanContinuity(f.ctx, f.owner); err == nil {
		t.Fatal("unbound request minted a continuity proof")
	}
	var escaped func() error
	if err := f.bound(func(ctx context.Context) error {
		var err error
		escaped, err = p.prepareHumanContinuity(ctx, f.owner)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := escaped(); err == nil {
		t.Fatal("proof escaped its original request operation")
	}
	if err := f.bound(func(ctx context.Context) error {
		publish, err := p.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			return err
		}
		if err = publish(); err != nil {
			return err
		}
		if err := publish(); err == nil {
			t.Fatal("one proof published twice")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCollaborationHumanContinuityCancelledProofDoesNotAdvanceHorizon(t *testing.T) {
	f := newContinuityRig(t, true)
	p := f.provider(t)
	if err := f.bound(func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		publish, err := p.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := publish(); err == nil {
			t.Fatal("cancelled proof published")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.advance(t, 5*time.Minute+time.Second)
	if err := f.check(t, true); err == nil {
		t.Fatal("failed request advanced authorization")
	}
}

func TestCollaborationHumanContinuityKeepsOriginalCredentialMonotonicCap(t *testing.T) {
	f := newContinuityRig(t, true)
	u := &AuthUser{ID: f.r.userID, SessionID: "admission-source", CredentialExpiresAt: time.Now().Add(30 * time.Second)}
	source, err := captureOriginalHumanCredential(f.ctx, f.r.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	f.ctx = auth.WithOriginalCredential(context.WithValue(f.ctx, ctxUser, u), *source)
	_, f.admission, err = f.s.issue(f.ctx, f.owner, true)
	if err != nil {
		t.Fatal(err)
	}
	p := f.provider(t)
	f.advance(t, 15*time.Second)
	if err := f.bound(func(ctx context.Context) error {
		publish, err := p.prepareHumanContinuity(ctx, f.owner)
		if err != nil {
			return err
		}
		return publish()
	}); err != nil {
		t.Fatal(err)
	}
	// BOOTTIME has advanced while the wall-clock fixture has not. A refresh
	// must not translate the original absolute expiry into a later BOOTTIME cap.
	f.advance(t, 20*time.Second)
	if err := f.check(t, true); err == nil {
		t.Fatal("refresh extended original credential's monotonic horizon")
	}
}

func TestCollaborationHumanContinuityRenewsOriginalSQLDeliveryGate(t *testing.T) {
	f := newContinuityRig(t, true)
	p := f.provider(t)
	ticket, admission, err := f.s.issue(f.ctx, f.owner, true)
	if err != nil {
		t.Fatal(err)
	}
	f.admission = admission
	joined, err := f.s.join(f.ctx, f.s.runtime, f.owner, ticket, "original-join")
	if err != nil {
		t.Fatal(err)
	}
	originalGate := joined.gate.GateID
	// This is the real SQL DeliveryStore/authority closure retaining its original
	// copied admission. Clock and native runtime are fixtures, not a live Rust peer.
	for second := 1; second <= 16*60; second++ {
		f.clock.now += time.Second
		if second%20 == 0 {
			f.owner, err = f.s.owners.Renew(f.ctx, f.owner)
			if err != nil {
				t.Fatal(err)
			}
		}
		if second%240 == 0 {
			if err = f.bound(func(ctx context.Context) error {
				publish, e := p.prepareHumanContinuity(ctx, f.owner)
				if e != nil {
					return e
				}
				return publish()
			}); err != nil {
				t.Fatal(second, err)
			}
		}
		grant, e := joined.delivery.Renew(f.ctx, f.owner, originalGate, fmt.Sprintf("renew-%d", second))
		if e != nil || grant.GateID != originalGate || grant.DurationMilliseconds != 5000 {
			t.Fatalf("original gate at %ds: %+v %v", second, grant, e)
		}
	}
	if err = f.check(t, true); err != nil {
		t.Fatal("original submission author after repeated delivery renewals", err)
	}
}

func TestCollaborationHumanContinuityExpiredPeerCannotRenewOrAffectFreshPeer(t *testing.T) {
	f := newContinuityRig(t, true)
	expired := f.admission
	f.advance(t, 4*time.Minute)
	if err := f.bound(func(ctx context.Context) error {
		publish, e := f.provider(t).prepareHumanContinuity(ctx, f.owner)
		if e != nil {
			return e
		}
		return publish()
	}); err != nil {
		t.Fatal(err)
	}
	// A separate original peer received no reauthorization. Retain its own pointer,
	// never overwrite it with the refreshed participant or adopt a reader identity.
	_, fresh, err := f.s.issue(f.ctx, f.owner, true)
	if err != nil {
		t.Fatal(err)
	}
	f.admission = fresh
	f.advance(t, 4*time.Minute)
	if err = f.bound(func(ctx context.Context) error {
		publish, e := f.provider(t).prepareHumanContinuity(ctx, f.owner)
		if e != nil {
			return e
		}
		return publish()
	}); err != nil {
		t.Fatal(err)
	}
	f.advance(t, time.Minute+time.Second)
	tx, err := f.r.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.checkCaptured(f.ctx, tx, expired, true, 5*time.Second)
	tx.Rollback()
	if !errors.Is(err, errCollaborationAdmissionHorizon) {
		t.Fatal("expired peer not a proven local denial", err)
	}
	if err = f.check(t, true); err != nil {
		t.Fatal("unaffected refreshed peer lost authority", err)
	}
}
