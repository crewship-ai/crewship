package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/keeper"
)

// seedActiveCredentialAfterBoot inserts an ACTIVE credential with a REAL
// ciphertext and assigns it to agentID, WITHOUT putting it in any secrets
// store — the on-disk state of a credential that became ACTIVE after the
// server booted (e.g. every credential from the #2376 ask→supply flow, or a
// handle-only credential whose type the boot store never loaded). Returns the
// credential id and its plaintext value.
func seedActiveCredentialAfterBoot(t *testing.T, db *sql.DB, wsID, agentID, userID, name, credType, envVar, plain string) (credID string) {
	t.Helper()
	enc, err := encryption.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt fixture credential: %v", err)
	}
	credID = "postboot-cred-" + name + "-" + wsID
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, status, encrypted_value, handle_only, created_by)
		 VALUES (?, ?, ?, ?, 2, 'ACTIVE', ?, 1, ?)`,
		credID, wsID, name, credType, enc, userID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, ?, 0)`,
		"postboot-ac-"+name+"-"+wsID, agentID, credID, envVar)
	return credID
}

// TestKeeperHandleExecute_StoreMiss_FallsBackToVault is the #2391 regression.
//
// The Keeper secrets store is loaded once at boot and never refreshed, so a
// credential supplied through the ask flow (always created after boot) is not
// in it. Because a handle-only credential's ONLY usage path is /keeper/execute,
// a store miss made the delivered grant unusable until the next restart. The
// handler must fall back to a live vault decrypt so an ALLOW still injects the
// value and runs the command.
func TestKeeperHandleExecute_StoreMiss_FallsBackToVault(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "postboot-crew-" + wsID
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Post Boot', 'post-boot')`,
		crewID, wsID)
	agentID := "postboot-agent-" + wsID
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES (?, ?, ?, 'Morgan', 'morgan')`,
		agentID, crewID, wsID)

	const secretValue = "s3cr3t-after-boot"
	const rawOutput = "connected with s3cr3t-after-boot and s3cr3t-after-boot must not leak"
	credID := seedActiveCredentialAfterBoot(t, db, wsID, agentID, userID,
		"REDIS_PASSWORD", "SECRET", "REDIS_PASSWORD", secretValue)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "task context matches intent",
		RiskScore: 2,
	}}
	ctr := &mockContainerExec{output: rawOutput, exitCode: 0, execID: "exec-postboot"}
	// The store does NOT contain this credential — exactly the post-boot state.
	emptyStore := &mockSecretGetter{secrets: map[string]string{}}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewKeeperHandler(db, "internal-token", gk, logger).
		WithSecrets(emptyStore).
		WithContainer(ctr)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "read the cache for the weekly report",
		Command:           "redis-cli ping",
		EnvVar:            "REDIS_PASSWORD",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Fatalf("expected ALLOW, got %s (reason %q)", result.Decision, result.Reason)
	}
	// The command actually ran: exit code and (scrubbed) output came back.
	if result.ExitCode != 0 {
		t.Errorf("expected exit_code 0 (command ran), got %d", result.ExitCode)
	}
	// The fallback-decrypted value was injected into the exec environment.
	wantEnv := "REDIS_PASSWORD=" + secretValue
	found := false
	for _, e := range ctr.lastExecEnv {
		if e == wantEnv {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected env %q injected into exec, got %v", wantEnv, ctr.lastExecEnv)
	}
	// And the value is still scrubbed from the returned output.
	if strings.Contains(result.Output, secretValue) {
		t.Errorf("output contains secret value %q — should have been scrubbed", secretValue)
	}
	if !strings.Contains(result.Output, "[REDACTED:keeper-secret]") {
		t.Errorf("expected [REDACTED:keeper-secret] in output, got %q", result.Output)
	}
}

// TestKeeperHandleExecute_StoreMiss_PendingSentinelRefused verifies the fallback
// never injects a PENDING/REQUESTED sentinel: if the row's decrypted body is the
// sentinel (a value that should be unreachable given the ACTIVE re-validation,
// but must fail closed if it ever is), the request is refused, not run.
func TestKeeperHandleExecute_StoreMiss_PendingSentinelRefused(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "sentinel-crew-" + wsID
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Sentinel', 'sentinel')`,
		crewID, wsID)
	agentID := "sentinel-agent-" + wsID
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES (?, ?, ?, 'Riley', 'riley')`,
		agentID, crewID, wsID)

	// ACTIVE row (so the metadata re-validation passes) whose ciphertext is the
	// REQUESTED sentinel rather than a real value.
	credID := seedActiveCredentialAfterBoot(t, db, wsID, agentID, userID,
		"GHOST", "SECRET", "GHOST", pendingSentinelRequested)

	execCalled := false
	spyCtr := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "", exitCode: 0},
		execCalled:        &execCalled,
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 1,
	}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewKeeperHandler(db, "internal-token", gk, logger).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{}}).
		WithContainer(spyCtr)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "read the cache for the weekly report",
		Command:           "redis-cli ping",
		EnvVar:            "GHOST",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a sentinel-bodied credential, got %d: %s", w.Code, w.Body.String())
	}
	if execCalled {
		t.Error("expected container Exec NOT to be called when the value is a pending sentinel")
	}
}
