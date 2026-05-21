package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// mockEvaluator is a test double for gatekeeper.Evaluator that returns a fixed response.
type mockEvaluator struct {
	resp keeper.GatekeeperResponse
}

func (m *mockEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	return m.resp, nil
}

func newKeeperHandler(t *testing.T, db *sql.DB) *KeeperHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewKeeperHandler(db, "internal-token", nil, logger)
}

func newKeeperHandlerWithGK(t *testing.T, db *sql.DB, gk gatekeeper.Evaluator) *KeeperHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewKeeperHandler(db, "internal-token", gk, logger)
}

// seedKeeperFixture creates a full workspace+crew+agent+credential for keeper tests.
func seedKeeperFixture(t *testing.T, db *sql.DB) (wsID, crewID, agentID, credID string) {
	t.Helper()
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)

	crewID = "security-crew-" + wsID
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Security Crew', 'security-crew')`,
		crewID, wsID)

	agentID = "security-agent-" + wsID
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES (?, ?, ?, 'SecurityBot', 'security-bot')`,
		agentID, crewID, wsID)

	credID = "security-cred-" + wsID
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES (?, ?, 'prod-ssh', 'SECRET', 2, 'v1:aW52YWxpZA==', ?)`,
		credID, wsID, userID)
	return
}

// doKeeperRequest posts a keeper request body and returns the recorder.
func doKeeperRequest(h *KeeperHandler, body keeperRequestBody) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)
	return w
}

// TestKeeper_CrossCrewSpoofing_Rejected verifies that an agent from crew-A cannot
// claim it belongs to crew-B to access crew-B-scoped logic.
func TestKeeper_CrossCrewSpoofing_Rejected(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Crew A and its agent
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-a', ?, 'Crew A', 'crew-a')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agent-a1', 'crew-a', ?, 'AgentA', 'agent-a')`, wsID)

	// Crew B (separate)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b', ?, 'Crew B', 'crew-b')`, wsID)

	// Credential in ws (accessible to crew-B's scope)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-b1', ?, 'crew-b-secret', 'SECRET', 2, 'v1:aW52YWxpZA==', ?)`, wsID, userID)

	h := newKeeperHandler(t, db)

	// Agent-A claims to be from crew-B — cross-crew spoofing attack
	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "agent-a1",
		RequestingCrewID:  "crew-b", // agent-a1 is actually in crew-a
		WorkspaceID:       wsID,
		CredentialID:      "cred-b1",
		Intent:            "I need the credential to deploy",
	})

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 (crew boundary violation), got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "crew boundary violation" {
		t.Errorf("expected 'crew boundary violation' error, got %q", resp["error"])
	}
}

// TestKeeper_CrossWorkspaceSpoofing_Rejected verifies that an agent from ws-1 cannot
// claim to operate in ws-2 to access ws-2 credentials.
func TestKeeper_CrossWorkspaceSpoofing_Rejected(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	// Two distinct workspaces
	ws1ID := "ws-security-1"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS1', 'ws1')`, ws1ID)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-ws1', ?, ?, 'OWNER')`, ws1ID, userID)

	ws2ID := "ws-security-2"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS2', 'ws2')`, ws2ID)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-ws2', ?, ?, 'OWNER')`, ws2ID, userID)

	// Agent in ws1
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-ws1', ?, 'WS1 Crew', 'ws1-crew')`, ws1ID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agent-ws1', 'crew-ws1', ?, 'WS1Bot', 'ws1-bot')`, ws1ID)

	// Credential in ws2
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-ws2', ?, 'ws2-secret', 'SECRET', 2, 'v1:aW52YWxpZA==', ?)`, ws2ID, userID)

	h := newKeeperHandler(t, db)

	// Agent from ws1 claims workspace ws2 — cross-workspace attack
	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "agent-ws1",
		RequestingCrewID:  "crew-ws1",
		WorkspaceID:       ws2ID, // agent is actually in ws1
		CredentialID:      "cred-ws2",
		Intent:            "I need the ws2 credential to deploy",
	})

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 (workspace boundary violation), got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "workspace boundary violation" {
		t.Errorf("expected 'workspace boundary violation' error, got %q", resp["error"])
	}
}

// TestKeeper_NonExistentAgent_Rejected verifies that a request with a non-existent
// agent ID returns 401, not a 200 with empty agent context.
func TestKeeper_NonExistentAgent_Rejected(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-real', ?, 'some-secret', 'SECRET', 1, 'v1:aW52YWxpZA==', ?)`, wsID, userID)

	h := newKeeperHandler(t, db)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "agent-does-not-exist",
		RequestingCrewID:  "some-crew",
		WorkspaceID:       wsID,
		CredentialID:      "cred-real",
		Intent:            "I need this credential to do work",
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (agent not found), got %d: %s", w.Code, w.Body.String())
	}
}

// TestKeeper_DeletedAgent_Rejected verifies that soft-deleted agents cannot make
// keeper requests.
func TestKeeper_DeletedAgent_Rejected(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-del', ?, 'Del Crew', 'del-crew')`, wsID)
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, deleted_at)
		 VALUES ('agent-deleted', 'crew-del', ?, 'GhostBot', 'ghost-bot', ?)`,
		wsID, time.Now().UTC().Format(time.RFC3339))
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-del', ?, 'some-secret', 'SECRET', 1, 'v1:aW52YWxpZA==', ?)`, wsID, userID)

	h := newKeeperHandler(t, db)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "agent-deleted",
		RequestingCrewID:  "crew-del",
		WorkspaceID:       wsID,
		CredentialID:      "cred-del",
		Intent:            "I need this credential to do work",
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 (deleted agent), got %d: %s", w.Code, w.Body.String())
	}
}

// TestKeeper_AgentWrongWorkspace_Rejected verifies that an agent from ws-1 cannot
// make a request while claiming workspace ws-2.
func TestKeeper_AgentWrongWorkspace_Rejected(t *testing.T) {
	db := setupTestDB(t)
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	ws1ID := "ws-aww-1"
	ws2ID := "ws-aww-2"
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS AWW1', 'ws-aww-1')`, ws1ID)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('maww1', ?, ?, 'OWNER')`, ws1ID, userID)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS AWW2', 'ws-aww-2')`, ws2ID)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('maww2', ?, ?, 'OWNER')`, ws2ID, userID)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-aww', ?, 'AWW Crew', 'aww-crew')`, ws1ID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('agent-aww', 'crew-aww', ?, 'AWWBot', 'aww-bot')`, ws1ID)
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		 VALUES ('cred-aww', ?, 'aww-secret', 'SECRET', 1, 'v1:aW52YWxpZA==', ?)`, ws2ID, userID)

	h := newKeeperHandler(t, db)

	// Agent is in ws1, request claims ws2
	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "agent-aww",
		RequestingCrewID:  "crew-aww",
		WorkspaceID:       ws2ID,
		CredentialID:      "cred-aww",
		Intent:            "I need the cred to deploy to staging",
	})

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 (workspace boundary violation), got %d: %s", w.Code, w.Body.String())
	}
}

// TestKeeper_SecretCredentialRequested_Allowed verifies that a valid SECRET credential
// request goes through the normal gatekeeper evaluation path (not rejected at handler level).
func TestKeeper_SecretCredentialRequested_Allowed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// Use a mock evaluator that returns ALLOW
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "task context matches intent",
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
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Decision != keeper.DecisionAllow {
		t.Errorf("expected ALLOW, got %s", result.Decision)
	}
}

// TestKeeper_AuditRecord_CreatedOnRequest verifies that a keeper_requests row is
// created for every request, including the final decision.
func TestKeeper_AuditRecord_CreatedOnRequest(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionDeny),
		Reason:    "insufficient justification",
		RiskScore: 7,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh for the deployment",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result keeper.RequestResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.RequestID == "" {
		t.Fatal("expected non-empty request_id in response")
	}

	// Verify the audit row exists in keeper_requests
	var decision, agentIDFromDB, credIDFromDB string
	err := db.QueryRowContext(context.Background(),
		`SELECT decision, requesting_agent_id, credential_id FROM keeper_requests WHERE id = ?`,
		result.RequestID).Scan(&decision, &agentIDFromDB, &credIDFromDB)
	if err != nil {
		t.Fatalf("keeper_requests row not found: %v", err)
	}
	if decision != string(keeper.DecisionDeny) {
		t.Errorf("expected DENY in audit record, got %s", decision)
	}
	if agentIDFromDB != agentID {
		t.Errorf("expected agent_id %q, got %q", agentID, agentIDFromDB)
	}
	if credIDFromDB != credID {
		t.Errorf("expected cred_id %q, got %q", credID, credIDFromDB)
	}
}

// TestKeeper_RiskScore_ClampedToValidRange verifies that even if the evaluator
// returns an out-of-range risk score, the stored/returned value is valid.
func TestKeeper_RiskScore_ClampedToValidRange(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// Mock evaluator returning extreme risk score
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "test",
		RiskScore: 999, // way out of range
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh for the deployment task",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result keeper.RequestResult
	json.Unmarshal(w.Body.Bytes(), &result)

	// Verify the DB row has a clamped risk score
	var riskScore int
	err := db.QueryRowContext(context.Background(),
		`SELECT risk_score FROM keeper_requests WHERE id = ?`, result.RequestID).Scan(&riskScore)
	if err != nil {
		t.Fatalf("keeper_requests row not found: %v", err)
	}
	if riskScore > 10 || riskScore < 1 {
		t.Errorf("expected risk_score in [1,10], got %d", riskScore)
	}
}

// TestKeeper_EscalateDecision_CreatesInboxItem guards PR-Z Z.4: ESCALATE
// decisions used to land only in the journal, leaving operators with no
// actionable surface. After Z.4 every ESCALATE must write a blocking
// inbox_items row (kind='escalation') so the bell badge and inbox feed
// see it. F4 endpoints (Phase 2) will reuse the same plumbing for
// behavior / skill-review / memory-health / negative-learning escalations.
func TestKeeper_EscalateDecision_CreatesInboxItem(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionEscalate),
		Reason:    "L3 credential, agent context inadequate",
		RiskScore: 8,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh for the deployment",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Decision != keeper.DecisionEscalate {
		t.Fatalf("expected ESCALATE decision, got %s", result.Decision)
	}

	var (
		inboxKind       string
		inboxBlocking   int
		inboxWorkspace  string
		inboxState      string
		inboxTargetRole sql.NullString
		inboxPriority   string
		inboxBody       sql.NullString
	)
	err := db.QueryRowContext(context.Background(), `
		SELECT kind, blocking, workspace_id, state,
		       target_role, priority, body_md
		FROM inbox_items
		WHERE kind = 'escalation' AND source_id = ?`,
		result.RequestID,
	).Scan(&inboxKind, &inboxBlocking, &inboxWorkspace, &inboxState,
		&inboxTargetRole, &inboxPriority, &inboxBody)
	if err != nil {
		t.Fatalf("expected inbox_items row for ESCALATE request_id=%s: %v", result.RequestID, err)
	}
	if inboxKind != "escalation" {
		t.Errorf("expected kind=escalation, got %q", inboxKind)
	}
	if inboxBlocking != 1 {
		t.Errorf("expected blocking=1 for ESCALATE inbox item, got %d", inboxBlocking)
	}
	if inboxWorkspace != wsID {
		t.Errorf("expected workspace=%s, got %q", wsID, inboxWorkspace)
	}
	if inboxState != "unread" {
		t.Errorf("expected initial state=unread, got %q", inboxState)
	}
	// Lock the rest of the inbox contract so a future refactor can't
	// silently drop routing metadata that operators rely on for
	// triage. CodeRabbit caught these missing assertions on the first
	// review pass.
	if !inboxTargetRole.Valid || inboxTargetRole.String != "MANAGER" {
		t.Errorf("expected target_role=MANAGER, got %v", inboxTargetRole)
	}
	if inboxPriority != "high" {
		t.Errorf("expected priority=high (ESCALATE is operator-blocking), got %q", inboxPriority)
	}
	if !inboxBody.Valid || inboxBody.String != gk.resp.Reason {
		t.Errorf("expected body_md to propagate gatekeeper reason %q, got %v", gk.resp.Reason, inboxBody)
	}
}

// TestKeeper_AllowDecision_DoesNotCreateInboxItem ensures Z.4 only fires
// for ESCALATE: an ALLOW or DENY decision must not pollute the inbox.
// Operators expect the bell to surface things needing action, not every
// keeper decision.
func TestKeeper_AllowDecision_DoesNotCreateInboxItem(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "looks fine",
		RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need prod-ssh for the deployment",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var result keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM inbox_items WHERE source_id = ?`,
		result.RequestID,
	).Scan(&count); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 inbox rows for ALLOW decision, got %d", count)
	}
}
