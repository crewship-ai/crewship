package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /api/v1/admin/keeper/requests/{requestId}/events — the API↔CLI-parity
// surface for the append-only keeper decision ledger (#1369).

func doKeeperEvents(t *testing.T, h *KeeperLogHandler, userID, wsID, role, requestID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("requestId", requestID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, role))
	rr := httptest.NewRecorder()
	h.ListEvents(rr, req)
	return rr
}

// seedLedgerRows writes a two-transition history the way the handlers do.
func seedLedgerRows(t *testing.T, h *KeeperLogHandler, wsID, agentID, crewID, credID, requestID string) {
	t.Helper()
	execOrFatal(t, h.db, `
		INSERT INTO keeper_request_events
			(id, request_id, workspace_id, seq, state, request_type, requesting_agent_id,
			 requesting_crew_id, credential_id, intent, command, actor_type, actor_id, recorded_at)
		VALUES ('kre1', ?, ?, 1, 'PENDING', 'execute', ?, ?, ?, 'deploy', 'gh pr list', 'agent', ?, '2026-07-25T10:00:00Z')`,
		requestID, wsID, agentID, crewID, credID, agentID)
	execOrFatal(t, h.db, `
		INSERT INTO keeper_request_events
			(id, request_id, workspace_id, seq, state, request_type, requesting_agent_id,
			 requesting_crew_id, credential_id, intent, command, reason, risk_score, exit_code,
			 actor_type, actor_id, recorded_at)
		VALUES ('kre2', ?, ?, 2, 'ALLOW', 'execute', ?, ?, ?, 'deploy', 'gh pr list', 'looks fine', 2, 0,
			'keeper', 'keeper', '2026-07-25T10:00:05Z')`,
		requestID, wsID, agentID, crewID, credID)
}

func TestKeeperEvents_ReturnsTransitionsInOrder(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperLogHandler(db, newTestLogger())
	userID := "test-user-id"
	seedLedgerRows(t, h, wsID, agentID, crewID, credID, "kr_1")

	rr := doKeeperEvents(t, h, userID, wsID, "ADMIN", "kr_1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var events []keeperRequestEventEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d (%+v), want 2", len(events), events)
	}
	if events[0].Seq != 1 || events[0].State != "PENDING" {
		t.Errorf("event 1 = seq %d %q, want 1 PENDING", events[0].Seq, events[0].State)
	}
	if events[1].Seq != 2 || events[1].State != "ALLOW" {
		t.Errorf("event 2 = seq %d %q, want 2 ALLOW", events[1].Seq, events[1].State)
	}
	// Names are joined so the CLI/UI does not have to resolve ids itself.
	if events[0].AgentName == "" {
		t.Error("agent_name not resolved")
	}
	if events[1].ExitCode == nil || *events[1].ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", events[1].ExitCode)
	}
	// A PENDING carries no risk score — omitted, not 0.
	if events[0].RiskScore != nil {
		t.Errorf("PENDING risk_score = %v, want omitted", *events[0].RiskScore)
	}
}

// TestKeeperEvents_ForeignWorkspaceReturnsEmpty: the endpoint must not become an
// id-enumeration oracle. A request from another workspace looks exactly like one
// that does not exist.
func TestKeeperEvents_ForeignWorkspaceReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperLogHandler(db, newTestLogger())
	seedLedgerRows(t, h, wsID, agentID, crewID, credID, "kr_1")

	// Another workspace asking for a request id it does not own.
	rr := doKeeperEvents(t, h, "test-user-id", "ws-not-mine", "ADMIN", "kr_1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an empty list (no existence leak)", rr.Code)
	}
	var events []keeperRequestEventEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("leaked %d events across the workspace boundary", len(events))
	}

	// And an id that genuinely does not exist behaves identically.
	rr = doKeeperEvents(t, h, "test-user-id", wsID, "ADMIN", "kr_nope")
	if rr.Code != http.StatusOK || rr.Body.String() == "" {
		t.Fatalf("unknown id: status %d body=%s, want 200 []", rr.Code, rr.Body.String())
	}
}

// TestKeeperEvents_RequiresAdmin: the keeper decision history is a security log.
func TestKeeperEvents_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, _, _ := seedKeeperFixture(t, db)
	h := NewKeeperLogHandler(db, newTestLogger())

	for _, role := range []string{"MEMBER", "VIEWER", "MANAGER"} {
		if rr := doKeeperEvents(t, h, "test-user-id", wsID, role, "kr_1"); rr.Code != http.StatusForbidden {
			t.Errorf("role %s: status %d, want 403", role, rr.Code)
		}
	}
	for _, role := range []string{"ADMIN", "OWNER"} {
		if rr := doKeeperEvents(t, h, "test-user-id", wsID, role, "kr_1"); rr.Code != http.StatusOK {
			t.Errorf("role %s: status %d, want 200", role, rr.Code)
		}
	}
}

// TestKeeperEvents_RequiresRequestID guards the route contract.
func TestKeeperEvents_RequiresRequestID(t *testing.T) {
	db := setupTestDB(t)
	wsID, _, _, _ := seedKeeperFixture(t, db)
	h := NewKeeperLogHandler(db, newTestLogger())

	if rr := doKeeperEvents(t, h, "test-user-id", wsID, "ADMIN", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("empty request id: status %d, want 400", rr.Code)
	}
}
