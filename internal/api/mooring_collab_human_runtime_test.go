package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCollaborationHumanReconnectRetainsOriginalAdmissionAndDeadline(t *testing.T) {
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	_, original, err := s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	clock.now += 10 * time.Second
	ticket, err := s.reconnect(context.Background(), binding, owner, original.participant)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := s.consume(binding.runtime, owner, ticket, "same-original-admission")
	if err != nil {
		t.Fatal(err)
	}
	if admission != original || s.hostSessionID() != original.session {
		t.Fatal("reconnect changed original identity, source, authority or deadline")
	}
	if _, err = s.consume(binding.runtime, owner, ticket, "same-ticket-again"); err == nil {
		t.Fatal("reconnect ticket reused")
	}
	mustExec(t, r.db, `UPDATE user_sessions SET revoked_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), original.source.ID)
	if _, err = s.reconnect(context.Background(), binding, owner, original.participant); err == nil {
		t.Fatal("reconnect revived revoked original source")
	}
}

func TestCollaborationHumanStoreBindingCannotFollowReplacement(t *testing.T) {
	r, s, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	check := func(want bool) {
		t.Helper()
		err := s.runHumanOperation(request, admission.participant, func(bound context.Context) error {
			tx, err := r.db.BeginTx(bound, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return old.authority(bound, tx, mooring.BatchAccess{Intent: "outcome", Scope: s.scope, Participant: admission.participant})
		})
		if (err == nil) != want {
			t.Fatalf("allowed=%v want=%v: %v", err == nil, want, err)
		}
	}
	check(true)
	// Unit fixture changes only the process-local binding, not canonical rights.
	// An old store must fail even though outcome/retry needs no preparation I/O.
	s.mu.Lock()
	s.bindingID = "new-unit-runtime-binding"
	s.mu.Unlock()
	check(false)
}

func TestCollaborationHumanReplacementMustDrainBeforeSQL(t *testing.T) {
	_, s, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := s.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	if err = s.runHumanOperation(request, admission.participant, func(context.Context) error {
		if s.runtimeMu.TryLock() {
			s.runtimeMu.Unlock()
			t.Fatal("replacement could overtake an active commit/response")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !s.runtimeMu.TryLock() {
		t.Fatal("operation lease leaked")
	}
	s.runtimeMu.Unlock()
	if _, err = s.replaceRuntime(ctx, &mooring.Runtime{}, owner); err == nil {
		t.Fatal("forged replacement Runtime accepted")
	}
}

func TestCollaborationHumanRegistryLifecycleCannotRefreshAfterRestore(t *testing.T) {
	_, s, _, _, _ := collaborationAdmissionFixture(t)
	before := s.lifecycle
	s.fence.SuspendBeforeRestore()
	if err := s.fence.ResumeAfterRestore(); err != nil {
		t.Fatal(err)
	}
	if release, err := s.fence.acquireOriginal(before); err == nil {
		release()
		t.Fatal("old registry lifecycle survived restore")
	}
	if s.lifecycle != before {
		t.Fatal("registry adopted fresh incarnation")
	}
}
