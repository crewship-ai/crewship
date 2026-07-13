package api

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"log/slog"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// #1060: keeper must run the credential-injected command as the SAME user the
// agent process actually runs as, resolved from the live container rather than
// a hardcoded "1001:1001" constant — and fail closed if that user can't be
// determined or would be privileged.

// TestKeeperExecute_ResolvesContainerUser proves the exec user is taken from
// the container's configured user, not a hardcoded constant: a container
// configured to run as 2000:2000 execs as 2000:2000.
func TestKeeperExecute_ResolvesContainerUser(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	ctr := &mockContainerExec{output: "ok", exitCode: 0, execID: "e1", userResult: "2000:2000"}
	secrets := &mockSecretGetter{secrets: map[string]string{credID: "s"}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewKeeperHandler(db, "internal-token", gk, logger).WithSecrets(secrets).WithContainer(ctr)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "I need to list PRs", Command: "id", ContainerID: "test-container",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ctr.lastExecUser != "2000:2000" {
		t.Errorf("exec user = %q, want 2000:2000 (resolved from container, not hardcoded)", ctr.lastExecUser)
	}
}

// TestKeeperExecute_UndeterminableUser_FailsClosed covers the three
// undeterminable/privileged cases: the command must NOT run.
func TestKeeperExecute_UndeterminableUser_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		ctr  *mockContainerExec
	}{
		{"inspect error", &mockContainerExec{output: "ok", userErr: errors.New("inspect boom")}},
		{"empty user", &mockContainerExec{output: "ok", userForceEmpty: true}},
		{"root user", &mockContainerExec{output: "ok", userResult: "0:0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
			gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
				Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
			}}
			secrets := &mockSecretGetter{secrets: map[string]string{credID: "s"}}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			h := NewKeeperHandler(db, "internal-token", gk, logger).WithSecrets(secrets).WithContainer(tc.ctr)

			w := doKeeperExecute(h, keeperExecuteBody{
				RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
				CredentialID: credID, Intent: "I need to list PRs", Command: "id", ContainerID: "test-container",
			})
			if w.Code == http.StatusOK {
				t.Errorf("expected fail-closed (non-200), got 200: %s", w.Body.String())
			}
			if tc.ctr.lastExecUser != "" {
				t.Errorf("command was executed (user=%q) — must fail closed before exec", tc.ctr.lastExecUser)
			}
		})
	}
}
