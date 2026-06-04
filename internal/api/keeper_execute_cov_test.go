package api

// Coverage-focused tests for the keeper execute / credential-grant
// endpoint plus the remaining reachable branches in GetRequest, the
// Keeper status handler, and the Keeper audit-log handler.
//
// These mirror the setup in keeper_security_test.go / keeper_execute_test.go
// (seedKeeperFixture, mockEvaluator, mockSecretGetter, mockContainerExec)
// and only add the branches those files don't already exercise:
//   - HandleExecute: invalid JSON, missing-credential validation, null byte,
//     bad env_var, credential_name resolution (hit + miss), agent-not-found,
//     workspace/crew boundary, credential-not-found, ESCALATE path,
//     env_var derivation from the agent_credentials assignment, and a
//     DB-error 500 via a closed DB (fault injection).
//   - GetRequest: missing requestId (400) and the success readback path.
//   - KeeperStatusHandler.Status: unauthenticated (401) + authenticated
//     success with DB-backed counts.
//   - KeeperLogHandler.List: unauthenticated (401), forbidden (403),
//     missing-workspace (400), and the success path with one audit row.
//
// SKIPPED: Ollama/LLM-network branches (probeOllama network probe in
// Status, and the gatekeeper.Evaluate LLM round-trip) — these reach the
// network and are exercised elsewhere with a fake provider (kp2Provider).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/keeper"
)

// --- HandleExecute validation branches -------------------------------------

// TestCovKEExecute_InvalidJSON covers the readJSON failure → 400.
func TestCovKEExecute_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_MissingCredentialIdentifier covers the branch where
// neither credential_id nor credential_name is supplied → 400.
func TestCovKEExecute_MissingCredentialIdentifier(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: "agent1",
		RequestingCrewID:  "crew1",
		WorkspaceID:       "ws1",
		Intent:            "I need to list PRs now",
		Command:           "gh pr list",
		ContainerID:       "container1",
		// no CredentialID, no CredentialName
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no credential identifier, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_NullByteInCommand covers the null-byte rejection → 400.
func TestCovKEExecute_NullByteInCommand(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: "agent1",
		RequestingCrewID:  "crew1",
		WorkspaceID:       "ws1",
		CredentialID:      "cred1",
		Intent:            "I need to run a command",
		Command:           "gh pr list\x00rm -rf /",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for null byte in command, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_InvalidEnvVar covers the env_var format rejection → 400.
func TestCovKEExecute_InvalidEnvVar(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: "agent1",
		RequestingCrewID:  "crew1",
		WorkspaceID:       "ws1",
		CredentialID:      "cred1",
		Intent:            "I need to run a command",
		Command:           "echo hello",
		EnvVar:            "1BAD-NAME", // invalid: starts with digit, has dash
		ContainerID:       "container1",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid env_var, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_CredentialNameNotFound covers credential_name resolution
// when no matching agent_credentials row exists → 404.
func TestCovKEExecute_CredentialNameNotFound(t *testing.T) {
	db := setupTestDB(t)
	seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: "agent1",
		RequestingCrewID:  "crew1",
		WorkspaceID:       "ws1",
		CredentialName:    "NONEXISTENT_ENV",
		Intent:            "I need to run a command",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unresolvable credential_name, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_AgentNotFound covers the agent lookup ErrNoRows → 401.
func TestCovKEExecute_AgentNotFound(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, _, credID := seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: "ghost-agent",
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to run a command",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unknown agent, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_WorkspaceBoundary covers the workspace-boundary 403.
func TestCovKEExecute_WorkspaceBoundary(t *testing.T) {
	db := setupTestDB(t)
	_, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       "some-other-ws",
		CredentialID:      credID,
		Intent:            "I need to run a command",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 workspace boundary, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_CrewBoundary covers the crew-boundary 403.
func TestCovKEExecute_CrewBoundary(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  "different-crew",
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to run a command",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 crew boundary, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_CredentialNotFound covers the credential metadata lookup
// ErrNoRows → 404 (agent valid, credential id bogus).
func TestCovKEExecute_CredentialNotFound(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      "no-such-credential",
		Intent:            "I need to run a command",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 credential not found, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEExecute_Escalate covers the non-ALLOW (ESCALATE) branch: audit
// updated, no exec, decision returned. mockEvaluator returns ESCALATE.
func TestCovKEExecute_Escalate(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionEscalate),
		Reason:    "L3 credential needs operator sign-off",
		RiskScore: 7,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, newTestLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "secret"}}).
		WithContainer(&mockContainerExec{output: "should-not-run", exitCode: 0})

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh to deploy the release",
		Command:           "ssh prod uptime",
		ContainerID:       "test-container",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionEscalate {
		t.Errorf("expected ESCALATE, got %s", result.Decision)
	}
	if result.Output != "" {
		t.Errorf("expected no output on ESCALATE, got %q", result.Output)
	}

	// Audit row should record ESCALATE.
	var decision string
	if err := db.QueryRowContext(context.Background(),
		`SELECT decision FROM keeper_requests WHERE id = ?`, result.RequestID).Scan(&decision); err != nil {
		t.Fatalf("audit row not found: %v", err)
	}
	if decision != string(keeper.DecisionEscalate) {
		t.Errorf("expected ESCALATE in audit, got %q", decision)
	}
}

// TestCovKEExecute_EnvVarFromAssignment covers the branch where env_var is
// empty in the body and the handler derives it from an agent_credentials
// assignment row (preferred over the credential-name fallback). The
// gatekeeper allows so the full ALLOW path runs, and a broadcaster is wired
// so the BroadcastKeeperEvent call on the ALLOW branch is exercised too.
func TestCovKEExecute_EnvVarFromAssignment(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// The fixture already assigns this credential to the agent; override the
	// env var name so it is derived from the assignment (UNIQUE(agent_id,
	// credential_id) blocks a second insert).
	execOrFatal(t, db,
		`UPDATE agent_credentials SET env_var_name = ? WHERE agent_id = ? AND credential_id = ?`,
		"DEPLOY_TOKEN", agentID, credID)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 2,
	}}
	bc := &covKEBroadcaster{}
	const secret = "supersecret-xyz"
	h := NewKeeperHandler(db, "internal-token", gk, newTestLogger()).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: secret}}).
		WithContainer(&mockContainerExec{output: "done", exitCode: 0, execID: "exec-env"}).
		WithBroadcaster(bc)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh to run the deploy",
		Command:           "printenv DEPLOY_TOKEN",
		ContainerID:       "test-container",
		// EnvVar deliberately empty → derived from assignment
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !bc.called {
		t.Error("expected broadcaster to be called on ALLOW")
	}
}

// TestCovKEExecute_DBError500 covers the non-ErrNoRows error path on the
// agent lookup: closing the DB makes QueryRowContext return a driver error,
// which must surface as 500 (not 401/404).
func TestCovKEExecute_DBError500(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	// Fault injection: close the DB so the next query errors.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to run a command now",
		Command:           "echo hello",
		ContainerID:       "container1",
	})

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on DB error, got %d: %s", w.Code, w.Body.String())
	}
}

// --- GetRequest branches ----------------------------------------------------

// TestCovKEGetRequest_MissingID covers the empty requestId → 400.
func TestCovKEGetRequest_MissingID(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "internal-token", nil, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/keeper/request/", nil)
	req.SetPathValue("requestId", "")
	w := httptest.NewRecorder()
	h.GetRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing requestId, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEGetRequest_Success covers the readback success path after a real
// request is persisted by HandleRequest.
func TestCovKEGetRequest_Success(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "ok",
		RiskScore: 3,
	}}
	h := NewKeeperHandler(db, "internal-token", gk, newTestLogger())

	cw := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh for the deployment task",
	})
	if cw.Code != http.StatusOK {
		t.Fatalf("seed request failed: %d %s", cw.Code, cw.Body.String())
	}
	var created keeper.RequestResult
	if err := json.Unmarshal(cw.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/keeper/request/"+created.RequestID, nil)
	req.SetPathValue("requestId", created.RequestID)
	w := httptest.NewRecorder()
	h.GetRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var row struct {
		ID                string `json:"id"`
		RequestingAgentID string `json:"requesting_agent_id"`
		CredentialID      string `json:"credential_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if row.ID != created.RequestID {
		t.Errorf("expected id %q, got %q", created.RequestID, row.ID)
	}
	if row.RequestingAgentID != agentID || row.CredentialID != credID {
		t.Errorf("readback mismatch: agent=%q cred=%q", row.RequestingAgentID, row.CredentialID)
	}
}

// --- KeeperStatusHandler.Status branches ------------------------------------

// TestCovKEStatus_Unauthenticated covers the nil-user → 401 branch.
func TestCovKEStatus_Unauthenticated(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperStatusHandler(db, nil, nil, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/keeper", nil)
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKEStatus_Success covers the authenticated path: gatekeeper_configured
// reflects a non-nil evaluator, cfg fields populate the response, and the DB
// count queries run. cfg.Enabled is false so the Ollama network probe is
// skipped (SKIPPED branch).
func TestCovKEStatus_Success(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// Seed two audit rows (one ALLOW, one DENY) so the count queries return
	// non-zero and exercise their scan paths.
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	rh := NewKeeperHandler(db, "internal-token", gk, newTestLogger())
	_ = doKeeperRequest(rh, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "I need prod-ssh for the deployment",
	})
	denyH := NewKeeperHandler(db, "internal-token", nil, newTestLogger()) // no gk → DENY
	_ = doKeeperRequest(denyH, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "I need prod-ssh for another task",
	})

	cfg := &config.KeeperConfig{Enabled: false, Model: "claude-haiku-4-5"}
	h := NewKeeperStatusHandler(db, cfg, gk, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/keeper", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	w := httptest.NewRecorder()
	h.Status(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		GatekeeperSet bool `json:"gatekeeper_configured"`
		TotalRequests int  `json:"total_requests"`
		AllowCount    int  `json:"allow_count"`
		DenyCount     int  `json:"deny_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.GatekeeperSet {
		t.Error("expected gatekeeper_configured=true")
	}
	if resp.TotalRequests != 2 {
		t.Errorf("expected total_requests=2, got %d", resp.TotalRequests)
	}
	if resp.AllowCount != 1 || resp.DenyCount != 1 {
		t.Errorf("expected allow=1 deny=1, got allow=%d deny=%d", resp.AllowCount, resp.DenyCount)
	}
}

// --- KeeperLogHandler.List branches -----------------------------------------

// TestCovKELog_Unauthenticated covers the nil-user → 401 branch.
func TestCovKELog_Unauthenticated(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperLogHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/keeper/requests", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKELog_Forbidden covers the canRole("manage") gate → 403 for a
// non-manager role.
func TestCovKELog_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperLogHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/keeper/requests", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "u1"}), "ws1", "VIEWER"))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for VIEWER, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKELog_MissingWorkspace covers the empty-workspace → 400 branch
// (manager role but no workspace context).
func TestCovKELog_MissingWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperLogHandler(db, newTestLogger())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/keeper/requests", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "u1"}), "", "OWNER"))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing workspace, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCovKELog_Success covers the full query/scan path with one persisted
// audit row, plus limit/offset query-param parsing.
func TestCovKELog_Success(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}}
	rh := NewKeeperHandler(db, "internal-token", gk, newTestLogger())
	_ = doKeeperRequest(rh, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "I need prod-ssh for the deployment",
	})

	h := NewKeeperLogHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/keeper/requests?limit=10&offset=0", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "u1"}), wsID, "OWNER"))
	w := httptest.NewRecorder()
	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d: %s", len(entries), w.Body.String())
	}
	if got, _ := entries[0]["agent_id"].(string); got != agentID {
		t.Errorf("expected agent_id %q, got %q", agentID, got)
	}
}

// --- test doubles -----------------------------------------------------------

// covKEBroadcaster records whether BroadcastKeeperEvent fired.
type covKEBroadcaster struct {
	called bool
}

func (b *covKEBroadcaster) BroadcastKeeperEvent(_ string, _ map[string]any) {
	b.called = true
}
