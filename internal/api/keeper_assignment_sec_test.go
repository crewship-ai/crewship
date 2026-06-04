package api

// Security regression tests for the keeper credential_id-direct path.
//
// VULN (HIGH): the credential_name path JOINs agent_credentials scoped to the
// requesting agent, but the credential_id-direct path previously dropped that
// JOIN and only checked workspace+crew. Any agent could therefore name a PEER
// agent's credential_id in the same workspace and have Keeper evaluate (and,
// on ALLOW, inject) a credential it was never assigned. The execute handler
// additionally fell back to a derived env-var name when no agent_credentials
// row existed, silently proceeding instead of denying.
//
// These tests pin the fix: the credential_id-direct path must require an
// agent_credentials assignment binding the requesting agent. A peer's
// credential resolves to the same 404 as a non-existent one (no existence
// leak); a properly-assigned credential still flows through to evaluation.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// TestSecKeeperRequest_PeerCredentialID_Denied is the core RED test for the
// request handler: agent A (the fixture agent) names a credential assigned only
// to a different agent B in the same workspace. Expect 404 — no row from the
// assignment-scoped lookup, indistinguishable from a missing credential.
func TestSecKeeperRequest_PeerCredentialID_Denied(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)

	userID := "test-user-id" // already seeded by seedKeeperFixture

	// Peer agent B in the same crew + workspace.
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES ('peer-agent-b', ?, ?, 'PeerBot', 'peer-bot')`, crewID, wsID)

	// Credential B, assigned ONLY to agent B.
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('peer-cred-b', ?, 'peer-secret', 'SECRET', 2, 'v1:aW52YWxpZA==', ?)`, wsID, userID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('peer-ac-b', 'peer-agent-b', 'peer-cred-b', 'PEER_SECRET', 0)`)

	// Evaluator would ALLOW if reached — proves the denial happens at the
	// assignment boundary, not in the gatekeeper.
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "would allow",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	// Agent A directly names agent B's credential_id.
	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      "peer-cred-b",
		Intent:            "I want to use a peer agent's credential to deploy",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("peer-credential escalation: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestSecKeeperRequest_AssignedCredentialID_Allowed is the positive control for
// the request handler: the fixture agent owns its credential, so the
// credential_id-direct path must still reach the gatekeeper and return ALLOW.
func TestSecKeeperRequest_AssignedCredentialID_Allowed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "assigned credential, intent matches",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh to deploy the release",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("assigned credential: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW for assigned credential, got %s", result.Decision)
	}
}

// TestSecKeeperExecute_PeerCredentialID_Denied is the core RED test for the
// execute handler: agent A names agent B's credential_id directly. The handler
// must deny at the assignment boundary (404) and never reach gatekeeper / exec.
func TestSecKeeperExecute_PeerCredentialID_Denied(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)

	userID := "test-user-id" // already seeded by seedKeeperFixture

	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES ('peer-agent-b', ?, ?, 'PeerBot', 'peer-bot')`, crewID, wsID)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('peer-cred-b', ?, 'peer-secret', 'SECRET', 2, 'v1:aW52YWxpZA==', ?)`, wsID, userID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES ('peer-ac-b', 'peer-agent-b', 'peer-cred-b', 'PEER_SECRET', 0)`)

	// Spy container so we can assert exec is never reached.
	execCalled := false
	spyCtr := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "leak", exitCode: 0},
		execCalled:        &execCalled,
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "would allow",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{"peer-cred-b": "peer-plaintext"}}).
		WithContainer(spyCtr)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      "peer-cred-b",
		Intent:            "I want to run a command with a peer's credential",
		Command:           "printenv PEER_SECRET",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("peer-credential execute escalation: expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if execCalled {
		t.Error("container Exec must not run for an unassigned (peer) credential")
	}
}

// TestSecKeeperExecute_AssignedCredentialID_Allowed is the positive control for
// the execute handler: the fixture agent owns its credential, so the
// credential_id-direct path runs through ALLOW and executes the command.
func TestSecKeeperExecute_AssignedCredentialID_Allowed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "assigned credential, intent matches",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(&mockContainerExec{output: "ok", exitCode: 0, execID: "exec-sec"})

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh to run the deploy",
		Command:           "printenv PROD_SSH",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("assigned credential execute: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW for assigned credential, got %s", result.Decision)
	}
}
