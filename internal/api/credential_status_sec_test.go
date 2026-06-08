package api

// Security regression: a credential whose status is no longer ACTIVE
// (EXPIRED / ERROR / REVOKED / RATE_LIMITED — set by the OAuth refresh
// worker or UpdateCredentialStatus) must be treated as unavailable at the
// keeper boundary. Before the fix the keeper execute/request credential
// lookups filtered only on deleted_at, so a revoked-but-not-deleted
// credential was still injectable until process restart.
//
// These tests drive the real handlers through the existing keeper harness
// (seedKeeperFixture / doKeeperExecute / doKeeperRequest) and assert that
// a non-ACTIVE credential takes the same not-found path the handler already
// uses, while an ACTIVE credential continues to work.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// setCredentialStatus flips the status column for a seeded credential.
func setCredentialStatus(t *testing.T, db *sql.DB, credID, status string) {
	t.Helper()
	execOrFatal(t, db, `UPDATE credentials SET status = ? WHERE id = ?`, status, credID)
}

// allowingExecuteHandler builds a keeper handler that would ALLOW (gatekeeper
// + secrets + container wired) so the only thing standing between the agent
// and the secret is the credential-status gate under test.
func allowingExecuteHandler(t *testing.T, db *sql.DB, credID, secret string) *KeeperHandler {
	t.Helper()
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	ctr := &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-status"}
	secrets := &mockSecretGetter{secrets: map[string]string{credID: secret}}
	return newKeeperHandlerWithGK(t, db, gk).WithSecrets(secrets).WithContainer(ctr)
}

// TestSecCredStatus_ExecuteExpired_NotInjected is the RED test: an EXPIRED
// credential assigned to the agent must NOT be injected — the execute handler
// must return 404 "credential not found" and never call the container.
func TestSecCredStatus_ExecuteExpired_NotInjected(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setCredentialStatus(t, db, credID, "EXPIRED")

	h := allowingExecuteHandler(t, db, credID, "ghp_should_not_leak")

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to list the open pull requests",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for EXPIRED credential, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSecCredStatus_ExecuteError_NotInjected — same for an ERROR-status
// credential (set by the validator after a failed health check).
func TestSecCredStatus_ExecuteError_NotInjected(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setCredentialStatus(t, db, credID, "ERROR")

	h := allowingExecuteHandler(t, db, credID, "ghp_should_not_leak")

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to list the open pull requests",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for ERROR credential, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSecCredStatus_ExecuteRevokedByName_NotInjected verifies the
// credential_name resolution path is gated too — naming a revoked credential
// must not resolve to its id.
func TestSecCredStatus_ExecuteRevokedByName_NotInjected(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setCredentialStatus(t, db, credID, "REVOKED")

	h := allowingExecuteHandler(t, db, credID, "ghp_should_not_leak")

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialName:    "PROD_SSH", // env_var_name from the fixture assignment
		Intent:            "I need to list the open pull requests",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for REVOKED credential resolved by name, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSecCredStatus_ExecuteActive_Works is the positive control: an ACTIVE
// credential still flows through to ALLOW.
func TestSecCredStatus_ExecuteActive_Works(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	// status defaults to ACTIVE from the schema; assert the happy path.

	h := allowingExecuteHandler(t, db, credID, "ghp_active_secret")

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to list the open pull requests",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for ACTIVE credential, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW for ACTIVE credential, got %s", result.Decision)
	}
}

// TestSecCredStatus_RequestExpired_NotEvaluated verifies the /keeper/request
// metadata lookup is gated: a non-ACTIVE credential must return 404 before the
// gatekeeper ever evaluates it.
func TestSecCredStatus_RequestExpired_NotEvaluated(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	setCredentialStatus(t, db, credID, "EXPIRED")

	gk := &capturingEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need the production database password",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for EXPIRED credential on /request, got %d: %s", w.Code, w.Body.String())
	}
	if gk.called {
		t.Error("gatekeeper must NOT be evaluated for a non-ACTIVE credential")
	}
}

// TestSecCredStatus_RequestActive_Works is the positive control for /request.
func TestSecCredStatus_RequestActive_Works(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need the production database password",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for ACTIVE credential on /request, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW for ACTIVE credential, got %s", result.Decision)
	}
}
