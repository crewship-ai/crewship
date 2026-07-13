package api

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// revokeOnEvalEvaluator ALLOWs, but first revokes the named credential — a
// stand-in for an operator or the OAuth refresh worker flipping the credential
// to REVOKED during the seconds-long gatekeeper LLM round-trip.
type revokeOnEvalEvaluator struct {
	db     *sql.DB
	credID string
}

func (e *revokeOnEvalEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	_, _ = e.db.Exec(`UPDATE credentials SET status = 'REVOKED' WHERE id = ?`, e.credID)
	return keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2}, nil
}

// TestSecKeeper_Execute_MidFlightRevocation_FailsClosed is the #1068 regression.
// keeper_execute re-validates credential status + assignment AFTER the
// gatekeeper round-trip and fails closed — but every existing ALLOW test keeps
// the credential ACTIVE the whole time, so that post-eval branch (active at
// first lookup, revoked mid-flight → 404, Exec NOT called, secret NOT injected)
// was asserted-but-untested. The credential here is ACTIVE at the first
// metadata lookup and only revoked inside Evaluate, so ONLY the re-validation
// gate can catch it.
func TestSecKeeper_Execute_MidFlightRevocation_FailsClosed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &revokeOnEvalEvaluator{db: db, credID: credID}
	ctr := &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-x"}
	secrets := &mockSecretGetter{secrets: map[string]string{credID: "ghp_should_not_leak"}}
	h := newKeeperHandlerWithGK(t, db, gk).WithSecrets(secrets).WithContainer(ctr)

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
		t.Fatalf("status = %d body=%s; want 404 (a credential revoked mid-flight must fail closed)",
			w.Code, w.Body.String())
	}
	// The container must NEVER have been touched — the secret was not injected.
	if ctr.lastExecContainerID != "" {
		t.Errorf("SECRET INJECTED: container Exec ran (%q) after a mid-flight revocation", ctr.lastExecContainerID)
	}
}

// TestSecKeeper_Execute_MidFlightUnassignment_FailsClosed is the assignment
// twin of #1068: the credential stays ACTIVE but its agent_credentials
// assignment is removed mid-flight (the re-validation JOINs on assignment too).
func TestSecKeeper_Execute_MidFlightUnassignment_FailsClosed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &unassignOnEvalEvaluator{db: db, agentID: agentID, credID: credID}
	ctr := &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-x"}
	secrets := &mockSecretGetter{secrets: map[string]string{credID: "ghp_should_not_leak"}}
	h := newKeeperHandlerWithGK(t, db, gk).WithSecrets(secrets).WithContainer(ctr)

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
		t.Fatalf("status = %d body=%s; want 404 (a credential unassigned mid-flight must fail closed)",
			w.Code, w.Body.String())
	}
	if ctr.lastExecContainerID != "" {
		t.Errorf("SECRET INJECTED: container Exec ran (%q) after a mid-flight unassignment", ctr.lastExecContainerID)
	}
}

type unassignOnEvalEvaluator struct {
	db      *sql.DB
	agentID string
	credID  string
}

func (e *unassignOnEvalEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	_, _ = e.db.Exec(`DELETE FROM agent_credentials WHERE agent_id = ? AND credential_id = ?`, e.agentID, e.credID)
	return keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2}, nil
}
