package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// TestKeeperHandleExecute_ExpiredLease_Refused proves that once the
// agent_credentials grant carries an expires_at in the past, /keeper/execute
// refuses to inject the secret even on a gatekeeper ALLOW — the lease has
// lapsed and the container command must never run. Fail-closed: a captured
// lease is unusable after its TTL.
func TestKeeperHandleExecute_ExpiredLease_Refused(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// Lease expired one minute ago.
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE agent_credentials SET expires_at = ? WHERE credential_id = ? AND agent_id = ?`,
		past, credID, agentID); err != nil {
		t.Fatalf("set expired lease: %v", err)
	}

	execCalled := false
	spyCtr := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "secret-output", exitCode: 0, execID: "e1"},
		execCalled:        &execCalled,
	}
	// ALLOW — the decision layer would permit; the lease gate must still refuse.
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewKeeperHandler(db, "internal-token", gk, logger).
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

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for expired lease, got %d: %s", w.Code, w.Body.String())
	}
	if execCalled {
		t.Error("expected container Exec NOT to be called for an expired lease")
	}
}

// TestKeeperHandleExecute_ValidLease_Honored proves that a grant whose
// expires_at is in the future is still honored: the secret is injected and the
// command runs.
func TestKeeperHandleExecute_ValidLease_Honored(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE agent_credentials SET expires_at = ? WHERE credential_id = ? AND agent_id = ?`,
		future, credID, agentID); err != nil {
		t.Fatalf("set valid lease: %v", err)
	}

	execCalled := false
	spyCtr := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "done", exitCode: 0, execID: "e1"},
		execCalled:        &execCalled,
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewKeeperHandler(db, "internal-token", gk, logger).
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
		t.Fatalf("expected 200 for valid lease, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW, got %s", result.Decision)
	}
	if !execCalled {
		t.Error("expected container Exec to be called for a valid lease")
	}
}
