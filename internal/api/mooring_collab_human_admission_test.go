package api

import (
	"context"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type collaborationAdmissionTestClock struct {
	boot string
	now  time.Duration
}

func (c *collaborationAdmissionTestClock) Now() (string, time.Duration, error) {
	return c.boot, c.now, nil
}

// Real canonical policy and owner SQL; the empty runtime identity below is a
// registry unit-test fixture, NOT proof of a negotiated Rust process. The real
// constructor must reject it, as checked before bypassing that constructor.
func collaborationAdmissionFixture(t *testing.T) (*pagesTransferRig, *collaborationHumanRegistry, mooring.OwnerLease, context.Context, *collaborationAdmissionTestClock) {
	t.Helper()
	// Ownership transactions require the production FULL handle, not the
	// ordinary read-oriented fixture's NORMAL synchronous setting.
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { full.Close() })
	r := pagesTransferSetupWithDB(t, full)
	r.seedPage(t, "admission-page", "admission-page", "Document", r.userID, "")
	mustExec(t, r.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('admission-page',?,'wordcraft','0','e30=')`, r.wsID)
	mustExec(t, r.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('admission-source',?,?)`, r.userID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
	u := &AuthUser{ID: r.userID, SessionID: "admission-source", CredentialExpiresAt: time.Now().Add(time.Hour)}
	source, err := captureOriginalHumanCredential(t.Context(), r.db, u, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	u.OriginalCredential = source
	ctx := auth.WithOriginalCredential(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), *source)
	clock := &collaborationAdmissionTestClock{boot: "admission-test-boot", now: time.Second}
	owners := mooring.NewOwnerStore(r.db, true, clock, time.Minute)
	scope := mooring.SessionKey{WorkspaceID: r.wsID, ArtifactID: "admission-page", ApplicationID: "wordcraft"}
	owner, err := owners.Acquire(ctx, scope, "admission-test-owner")
	if err != nil {
		t.Fatal(err)
	}
	pages := NewPageHandler(r.db, nil, newTestLogger())
	fence, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	ctx = collaborationIngressTestContext(t, fence, ctx)
	runtime := &mooring.Runtime{}
	if _, err := newCollaborationHumanRegistry(true, pages, owners, runtime, clock, fence, scope, mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.1.0", Engine: "0.10.0"}, 4); err == nil {
		t.Fatal("forged unnegotiated runtime accepted")
	}
	if _, err := newCollaborationCausalHumanRegistry(true, pages, owners, runtime, clock, fence, scope, mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}, 4); err == nil {
		t.Fatal("copied runtime gained causal capability")
	}
	if _, err := newCollaborationHumanRegistryPolicy(true, pages, owners, runtime, clock, fence, scope, mooring.RuntimeVersions{}, 4, "unknown"); err == nil {
		t.Fatal("unknown preparation policy accepted")
	}
	lifecycle, err := collaborationHumanLifecycleStamp(fence)
	if err != nil {
		t.Fatal(err)
	}
	registry := &collaborationHumanRegistry{lifecycle: lifecycle, bindingID: "unit-runtime-binding", fence: fence, pages: pages, owners: owners, runtime: runtime, clock: clock, scope: scope, sessionID: "admission-document-session", limit: 4, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}
	return r, registry, owner, ctx, clock
}

func TestCollaborationHumanTicketIsSingleUseAndPreservesOriginalPolicy(t *testing.T) {
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	ticket, admission, err := s.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 64 || admission.participant == "" || admission.source != *auth.OriginalCredentialFromContext(ctx) {
		t.Fatal("missing immutable admission")
	}
	var successes atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := s.consume(s.runtime, owner, ticket, "join-nonce"); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatal("ticket joined more or less than once", successes.Load())
	}
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	_, err = s.bindHumanRequest(request, admission.participant)
	if err != nil {
		t.Fatal(err)
	}
	check := func(write bool, want bool) {
		t.Helper()
		err := s.runHumanOperation(request, admission.participant, func(bound context.Context) error {
			tx, err := r.db.BeginTx(bound, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := s.owners.Check(bound, tx, owner); err != nil {
				return err
			}
			intent := "outcome"
			if write {
				intent = "write"
			}
			return s.authorizeBatchAtBinding(bound, tx, mooring.BatchAccess{Intent: intent, Scope: s.scope, Participant: admission.participant}, s.runtime, s.bindingID)
		})
		if (err == nil) != want {
			t.Fatalf("write=%v allowed=%v want=%v: %v", write, err == nil, want, err)
		}
	}
	check(false, true)
	check(true, false)
	// An unrelated recipient changes the delivery epoch, not this original
	// author vector. Reconnect retains the original author and source.
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('admission-page','user',?,'read',?)`, r.adminID, r.userID)
	check(false, true)
	_, fresh, err := s.issue(ctx, owner, false)
	if err != nil || fresh.participant == admission.participant || fresh.permission == admission.permission {
		t.Fatal("fresh policy silently reused old participant", fresh, err)
	}
	clock.now += 16 * time.Second
	if _, err := s.consume(s.runtime, owner, ticket, "another-join"); err == nil {
		t.Fatal("consumed ticket revived")
	}
	// A new process-local registry cannot recover capabilities from the SQL
	// checkpoint or owner lease alone.
	restarted := &collaborationHumanRegistry{runtime: s.runtime, clock: clock, admissions: make(map[string]collaborationHumanAdmission), tickets: make(map[string]collaborationHumanJoinTicket)}
	if _, err := restarted.consume(s.runtime, owner, ticket, "restart-join"); err == nil {
		t.Fatal("old ticket survived host restart")
	}
}

func TestCollaborationHumanAdmissionBindsCallerOwnerAndExpiry(t *testing.T) {
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	ticket, admission, err := s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"X-Internal-Token", actingAgentSlugHeader} {
		r := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		r.Header.Set(header, "agent-marker")
		if _, err := s.bindHumanRequest(r, admission.participant); err == nil {
			t.Fatal("agent header accepted", header)
		}
	}
	wrongUser := *UserFromContext(ctx)
	wrongUser.ID = r.adminID
	request := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(ctx, ctxUser, &wrongUser))
	if _, err := s.bindHumanRequest(request, admission.participant); err == nil {
		t.Fatal("another authenticated user borrowed admission")
	}
	wrongOwner := owner
	wrongOwner.Generation++
	if _, err := s.consume(s.runtime, wrongOwner, ticket, "wrong-owner"); err == nil {
		t.Fatal("owner generation changed")
	}
	if _, err := s.consume(s.runtime, owner, ticket, "retry-burned"); err == nil {
		t.Fatal("failed join did not burn ticket")
	}
	ticket, _, err = s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	clock.now += 16 * time.Second
	if _, err := s.consume(s.runtime, owner, ticket, "late-join"); err == nil {
		t.Fatal("expired join accepted")
	}
	clock.boot = "other-boot"
	if _, _, err := s.issue(ctx, owner, true); err == nil {
		t.Fatal("old boot owner accepted")
	}
}

func TestCollaborationHumanJoinRequiresCurrentSQLGateAndKeepsRevokedEvidence(t *testing.T) {
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	ticket, admission, err := s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := s.join(t.Context(), s.runtime, owner, ticket, "first-join")
	if err != nil || joined.gate.GateID == "" || joined.gate.PermissionVersion != admission.permission || joined.gate.OwnerGeneration != owner.Generation {
		t.Fatal("current SQL join", joined.gate, err)
	}
	clock.now += time.Second
	if _, err := joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "first-renew"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, r.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('admission-page','user',?,'read',?)`, r.adminID, r.userID)
	if _, err := joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "after-policy-change"); err == nil {
		t.Fatal("old epoch renewed")
	}
	var closed bool
	if err := r.db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&closed); err != nil || !closed {
		t.Fatal("canonical policy did not close gate", closed, err)
	}
	// Discarding expired in-memory admissions must not discard SQL barrier
	// membership or imply that a disconnected runtime acknowledged closure.
	clock.now += 6 * time.Minute
	s.mu.Lock()
	s.sweepLocked(clock.boot, clock.now)
	s.mu.Unlock()
	var retained int
	if err := r.db.QueryRow(`SELECT count(*) FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("gate evidence discarded", retained, err)
	}
}

func TestCollaborationHumanJoinCannotOutliveKnownOwnerHorizon(t *testing.T) {
	r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
	// Owner was acquired at1s for60s. It is still valid, but cannot cover the
	// required2s delivery grant. No timing sleeps or weakened owner check.
	clock.now += 58*time.Second + 500*time.Millisecond
	ticket, _, err := s.issue(ctx, owner, true)
	if err != nil {
		t.Fatal("owner should still be current", err)
	}
	if _, err := s.join(t.Context(), s.runtime, owner, ticket, "near-owner-expiry"); err == nil {
		t.Fatal("delivery outlived known owner horizon")
	}
	var gates int
	if err := r.db.QueryRow(`SELECT count(*) FROM mooring_delivery_gates`).Scan(&gates); err != nil || gates != 0 {
		t.Fatal("denied join persisted gate", gates, err)
	}
}

func TestCollaborationHumanOriginalIngressDoesNotRefreshAfterRestore(t *testing.T) {
	_, s, owner, ctx, _ := collaborationAdmissionFixture(t)
	ticket, admission, err := s.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	s.fence.SuspendBeforeRestore()
	if err := s.fence.ResumeAfterRestore(); err != nil {
		t.Fatal(err)
	}
	// Even byte-identical canonical SQL and the same authenticated source do
	// not turn a request already waiting at restore into a fresh admission.
	if _, _, err := s.issue(ctx, owner, false); err == nil {
		t.Fatal("old authenticated ingress refreshed")
	}
	if _, err := s.consume(s.runtime, owner, ticket, "after-restore"); err == nil {
		t.Fatal("old ticket revived")
	}
	fresh := collaborationIngressTestContext(t, s.fence, ctx)
	req := httptest.NewRequest("POST", "/", nil).WithContext(fresh)
	if _, err := s.bindHumanRequest(req, admission.participant); err == nil {
		t.Fatal("old participant adopted fresh ingress")
	}
	if _, _, err := s.issue(fresh, owner, false); err != nil {
		t.Fatal("freshly authenticated request refused", err)
	}
}

func TestCollaborationHumanStoreRequiresCompleteOperationLease(t *testing.T) {
	r, s, owner, ctx, _ := collaborationAdmissionFixture(t)
	_, admission, err := s.issue(ctx, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	bound, err := s.bindHumanRequest(request, admission.participant)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return s.authorizeBatchAtBinding(ctx, tx, mooring.BatchAccess{Intent: "outcome", Scope: s.scope, Participant: admission.participant}, s.runtime, s.bindingID)
	}
	if err := check(bound); err == nil {
		t.Fatal("loose caller binding bypassed complete-operation fence")
	}
	var escaped context.Context
	err = s.runHumanOperation(request, admission.participant, func(held context.Context) error {
		escaped = held
		return check(held)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(escaped); err == nil {
		t.Fatal("operation context remained active after response")
	}
	s.fence.SuspendBeforeRestore()
	if err := s.fence.ResumeAfterRestore(); err != nil {
		t.Fatal(err)
	}
	reached := false
	if err := s.runHumanOperation(request, admission.participant, func(context.Context) error { reached = true; return nil }); err == nil || reached {
		t.Fatal("old ingress reached restored store")
	}
}

func TestCollaborationHumanDeliveryDefaultFiveSeconds(t *testing.T) {
	_, registry, owner, ctx, _ := collaborationAdmissionFixture(t)
	ticket, _, err := registry.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := registry.join(t.Context(), registry.runtime, owner, ticket, "default-five-second-join")
	if err != nil {
		t.Fatal(err)
	}
	if joined.gate.DurationMilliseconds != 5000 {
		t.Fatalf("default delivery lease: got %dms, want 5000ms", joined.gate.DurationMilliseconds)
	}
}

func TestCollaborationHumanConfiguredLeaseUsesExactSQLHorizon(t *testing.T) {
	rig, registry, owner, ctx, clock := collaborationAdmissionFixture(t)
	registry.deliveryTTL = 3 * time.Second
	ticket, _, err := registry.issue(ctx, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := registry.join(t.Context(), registry.runtime, owner, ticket, "configured-three-second-join")
	if err != nil {
		t.Fatal(err)
	}
	var expires int64
	if err = rig.db.QueryRow(`SELECT expires_ns FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if joined.gate.DurationMilliseconds != 3000 || expires != int64(clock.now+3*time.Second) {
		t.Fatal("configured SQL horizon", joined.gate, expires)
	}
	clock.now += 3 * time.Second
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "expired-configured-gate"); err == nil {
		t.Fatal("expired configured gate revived")
	}
}
