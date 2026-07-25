package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// leaseTestLogger keeps the keeper handlers quiet in tests.
func leaseTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// enableAutoLease opts the workspace into auto-issuance with the given TTL.
func enableAutoLease(t *testing.T, db *sql.DB, wsID string, ttl int) {
	t.Helper()
	if err := governance.Upsert(context.Background(), db, wsID,
		governance.Settings{DenyNotifyMinRisk: 7, AutoLeaseSeconds: ttl}, ""); err != nil {
		t.Fatalf("enable auto-lease: %v", err)
	}
}

// setSecurityLevel raises the fixture credential to an L3/L4 tier — auto-lease
// only applies to Keeper-mediated credentials, never to the boot-delivered
// L1/L2 self-service tier.
func setSecurityLevel(t *testing.T, db *sql.DB, credID string, level int) {
	t.Helper()
	if _, err := db.Exec(`UPDATE credentials SET security_level = ? WHERE id = ?`, level, credID); err != nil {
		t.Fatalf("set security_level: %v", err)
	}
}

// readGrantLease returns the grant's lease state for assertions.
func readGrantLease(t *testing.T, db *sql.DB, agentID, credID string) (expiresAt, source, requestID string) {
	t.Helper()
	var e, s, r sql.NullString
	if err := db.QueryRow(
		`SELECT expires_at, lease_source, lease_request_id FROM agent_credentials
		  WHERE agent_id = ? AND credential_id = ?`, agentID, credID).Scan(&e, &s, &r); err != nil {
		t.Fatalf("read grant lease: %v", err)
	}
	return e.String, s.String, r.String
}

func allowEvaluator() *mockEvaluator {
	return &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
}

// TestKeeperRequest_AllowMintsLease is the core of #1373's second increment: an
// ALLOW on an L3 credential must convert the STANDING grant into a short-lived
// lease. Before this change the grant merely carried and enforced a lease that
// nothing ever minted, so every grant stayed standing forever.
func TestKeeperRequest_AllowMintsLease(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))
	enableAutoLease(t, db, wsID, 900)

	// Precondition: the fixture grant is standing.
	if exp, _, _ := readGrantLease(t, db, agentID, credID); exp != "" {
		t.Fatalf("fixture grant should start standing, got expires_at=%q", exp)
	}

	h := NewKeeperHandler(db, "internal-token", allowEvaluator(), leaseTestLogger())
	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "deploy",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("keeper request: got %d: %s", w.Code, w.Body.String())
	}

	expiresAt, source, reqID := readGrantLease(t, db, agentID, credID)
	if expiresAt == "" {
		t.Fatal("ALLOW did not mint a lease: expires_at is still NULL (standing grant)")
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("lease expires_at %q is not RFC3339: %v", expiresAt, err)
	}
	// 900s TTL — allow generous slack for slow CI, but pin the order of
	// magnitude so a unit-mixup (ms vs s) fails loudly.
	if d := time.Until(exp); d <= 10*time.Minute || d > 16*time.Minute {
		t.Errorf("lease expires in %v, want ~15m", d)
	}
	if source != leaseSourceKeeperAllow {
		t.Errorf("lease_source = %q, want %q", source, leaseSourceKeeperAllow)
	}
	if reqID == "" {
		t.Error("lease_request_id is empty — the lease must link back to the authorising decision")
	}
}

// TestKeeperRequest_AutoLeaseOffLeavesGrantStanding pins the opt-in contract. A
// workspace that has not configured auto_lease_seconds must behave exactly as it
// did before: ALLOW, no lease, grant stays standing.
func TestKeeperRequest_AutoLeaseOffLeavesGrantStanding(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))
	// Deliberately NOT enabling auto-lease.

	h := NewKeeperHandler(db, "internal-token", allowEvaluator(), leaseTestLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: got %d: %s", w.Code, w.Body.String())
	}

	if exp, src, _ := readGrantLease(t, db, agentID, credID); exp != "" || src != "" {
		t.Fatalf("auto-lease is off but grant was leased: expires_at=%q source=%q", exp, src)
	}
}

// TestKeeperRequest_L2CredentialNotLeased guards the boot-delivered
// self-service tier. L1/L2 credentials are handed to the agent for the whole run
// (env vars, /secrets files, the sidecar credstore); expiring one mid-run would
// break the agent's own work rather than contain an attacker.
func TestKeeperRequest_L2CredentialNotLeased(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL2))
	enableAutoLease(t, db, wsID, 900)

	h := NewKeeperHandler(db, "internal-token", allowEvaluator(), leaseTestLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: got %d: %s", w.Code, w.Body.String())
	}

	if exp, _, _ := readGrantLease(t, db, agentID, credID); exp != "" {
		t.Fatalf("an L2 credential must not be auto-leased, got expires_at=%q", exp)
	}
}

// TestKeeperRequest_DenyDoesNotMintLease: only an approval issues a lease. A
// DENY that quietly leased the grant would be a confusing half-grant.
func TestKeeperRequest_DenyDoesNotMintLease(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))
	enableAutoLease(t, db, wsID, 900)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionDeny),
		Reason:    "nope",
		RiskScore: 9,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, leaseTestLogger())
	if w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "deploy",
	}); w.Code != http.StatusOK {
		t.Fatalf("keeper request: got %d: %s", w.Code, w.Body.String())
	}

	if exp, _, _ := readGrantLease(t, db, agentID, credID); exp != "" {
		t.Fatalf("a DENY must not mint a lease, got expires_at=%q", exp)
	}
}

// TestKeeperExecute_AllowMintsLeaseWithoutSelfDenial covers the second minting
// site AND its most dangerous failure mode: the lease is written before the
// secret is injected, so a bug that minted an already-lapsed (or too-short)
// lease would make /keeper/execute deny the very request that authorised it.
// The request must still succeed and the grant must come out leased.
func TestKeeperExecute_AllowMintsLeaseWithoutSelfDenial(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))
	enableAutoLease(t, db, wsID, 900)

	execCalled := false
	spyCtr := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "done", exitCode: 0, execID: "e1"},
		execCalled:        &execCalled,
	}
	h := NewKeeperHandler(db, "internal-token", allowEvaluator(), leaseTestLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(spyCtr)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "list PRs",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("keeper execute: got %d: %s", w.Code, w.Body.String())
	}
	if !execCalled {
		t.Error("minting the lease must not block the ALLOW it accompanies")
	}
	expiresAt, source, _ := readGrantLease(t, db, agentID, credID)
	if expiresAt == "" {
		t.Fatal("/keeper/execute ALLOW did not mint a lease")
	}
	if source != leaseSourceKeeperAllow {
		t.Errorf("lease_source = %q, want %q", source, leaseSourceKeeperAllow)
	}
}

// TestIssueCredentialLease_NeverShortensLongerLease: an operator who ran
// `credential assign --ttl 7d` has made an explicit decision. A 15-minute
// auto-lease must not silently rewrite it — otherwise enabling the workspace
// knob would quietly override every hand-set TTL in the workspace.
func TestIssueCredentialLease_NeverShortensLongerLease(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))

	operatorExpiry := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE agent_credentials SET expires_at = ?, lease_source = ? WHERE agent_id = ? AND credential_id = ?`,
		operatorExpiry, leaseSourceManual, agentID, credID); err != nil {
		t.Fatalf("seed operator lease: %v", err)
	}

	_, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{}, leaseIssueInput{
		WorkspaceID:   wsID,
		CrewID:        crewID,
		AgentID:       agentID,
		CredentialID:  credID,
		SecurityLevel: int(keeper.SecurityLevelL3),
		Source:        leaseSourceKeeperAllow,
		TTLSeconds:    900,
	})
	if issued {
		t.Error("a 15m auto-lease must not be reported as issued over a 7d operator lease")
	}
	exp, src, _ := readGrantLease(t, db, agentID, credID)
	if exp != operatorExpiry {
		t.Errorf("expires_at = %q, want the operator's %q (longer lease must win)", exp, operatorExpiry)
	}
	if src != leaseSourceManual {
		t.Errorf("lease_source = %q, want %q — provenance must not be overwritten either", src, leaseSourceManual)
	}
}

// TestIssueCredentialLease_ExtendsShorterLease is the other half of the ordering
// rule: a live lease that is about to lapse gets refreshed by a fresh approval,
// which is what makes a rolling session-scoped lease usable at all.
func TestIssueCredentialLease_ExtendsShorterLease(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))

	soon := time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE agent_credentials SET expires_at = ? WHERE agent_id = ? AND credential_id = ?`,
		soon, agentID, credID); err != nil {
		t.Fatalf("seed short lease: %v", err)
	}

	newExpiry, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{}, leaseIssueInput{
		WorkspaceID:   wsID,
		CrewID:        crewID,
		AgentID:       agentID,
		CredentialID:  credID,
		SecurityLevel: int(keeper.SecurityLevelL3),
		Source:        leaseSourceKeeperAllow,
		TTLSeconds:    900,
	})
	if !issued {
		t.Fatal("a fresh approval must refresh a lease that is about to lapse")
	}
	if newExpiry <= soon {
		t.Errorf("refreshed expiry %q is not later than %q", newExpiry, soon)
	}
}

// TestIssueCredentialLease_NoGrantIsNotIssued: without an agent_credentials row
// there is nothing to lease, and the helper must report that rather than
// silently succeeding (which would make the caller log a lease that does not
// exist).
func TestIssueCredentialLease_NoGrantIsNotIssued(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	if _, err := db.Exec(`DELETE FROM agent_credentials WHERE agent_id = ?`, agentID); err != nil {
		t.Fatalf("drop grant: %v", err)
	}

	if _, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{}, leaseIssueInput{
		WorkspaceID:   wsID,
		CrewID:        crewID,
		AgentID:       agentID,
		CredentialID:  credID,
		SecurityLevel: int(keeper.SecurityLevelL3),
		Source:        leaseSourceKeeperAllow,
		TTLSeconds:    900,
	}); issued {
		t.Error("issued a lease for a credential the agent has no grant for")
	}
}

// TestIssueCredentialLease_WritesAuditRow pins the trail. A grant that starts
// expiring with no record is an unexplainable outage hours later.
func TestIssueCredentialLease_WritesAuditRow(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setSecurityLevel(t, db, credID, int(keeper.SecurityLevelL3))

	if _, issued := issueCredentialLease(context.Background(), db, leaseTestLogger(), noopEmitter{}, leaseIssueInput{
		WorkspaceID:   wsID,
		CrewID:        crewID,
		AgentID:       agentID,
		CredentialID:  credID,
		SecurityLevel: int(keeper.SecurityLevelL3),
		Source:        leaseSourceKeeperAllow,
		RequestID:     "req_1",
		TTLSeconds:    900,
	}); !issued {
		t.Fatal("expected the lease to be issued")
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ? AND event_type = ?`,
		credID, string(AuditEventLeased)).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("LEASED audit rows = %d, want 1", n)
	}
}
