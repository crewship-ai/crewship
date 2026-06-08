package api

// Security regression for the status-check -> secret-injection TOCTOU: the
// credential is ACTIVE at the metadata lookup (so the gate passes), but is
// revoked/expired WHILE the gatekeeper evaluates (an LLM round-trip can take
// seconds). The post-evaluate re-check in keeper_execute must catch the flip
// and deny — the secret must never reach the container.

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// flipStatusEvaluator ALLOWs, but flips the credential's status as a side
// effect of Evaluate — simulating a revoke landing in the window between the
// metadata lookup and secret injection.
type flipStatusEvaluator struct {
	db     *sql.DB
	credID string
	status string
}

func (e *flipStatusEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	_, _ = e.db.Exec(`UPDATE credentials SET status = ? WHERE id = ?`, e.status, e.credID)
	return keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2}, nil
}

func TestSecCredRace_RevokedDuringEvaluate_NotInjected(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &flipStatusEvaluator{db: db, credID: credID, status: "EXPIRED"}
	ctr := &mockContainerExec{output: "LEAKED-SECRET-OUTPUT", exitCode: 0, execID: "exec-race"}
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
		t.Fatalf("credential revoked during evaluate must be denied 404; got %d body=%s", w.Code, w.Body.String())
	}
	// The container must never have run (no exec output reaches the response).
	if strings.Contains(w.Body.String(), "LEAKED-SECRET-OUTPUT") {
		t.Fatal("container exec output leaked despite the credential being revoked mid-flight")
	}
	// And the DB confirms the flip landed before injection.
	var status string
	if err := db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, credID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "EXPIRED" {
		t.Fatalf("precondition: status=%q, want EXPIRED (flip should have run)", status)
	}
}
