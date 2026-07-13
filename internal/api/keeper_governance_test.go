package api

// keeper_governance_test.go — issue #1001 M0: the workspace watchdog
// governance endpoints (GET/PUT /api/v1/admin/keeper/governance) and the
// governance-driven HandleRequest behavior: security-contact targeting,
// realtime inbox push, and the high-risk DENY notify.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

func doGovernanceReq(t *testing.T, h *KeeperGovernanceHandler, method, body, wsID, userID string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/v1/admin/keeper/governance", nil)
	} else {
		req = httptest.NewRequest(method, "/api/v1/admin/keeper/governance", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		h.Get(rr, req)
	default:
		h.Put(rr, req)
	}
	return rr
}

func decodeGovernance(t *testing.T, raw []byte) keeperGovernanceResponse {
	t.Helper()
	var res keeperGovernanceResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode governance response: %v (%s)", err, raw)
	}
	return res
}

func TestKeeperGovernance_GetUnconfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := decodeGovernance(t, rr.Body.Bytes())
	if res.Configured || res.Enabled {
		t.Fatalf("unconfigured workspace = %+v, want configured=false enabled=false", res)
	}
	if res.DenyNotifyMinRisk != governance.DefaultDenyNotifyMinRisk {
		t.Fatalf("default risk = %d, want %d", res.DenyNotifyMinRisk, governance.DefaultDenyNotifyMinRisk)
	}
}

func TestKeeperGovernance_PutThenGet(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	body := `{"enabled": true, "security_contact_user_id": "` + userID + `", "deny_notify_min_risk": 6}`
	rr := doGovernanceReq(t, h, http.MethodPut, body, wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rr.Code, rr.Body.String())
	}

	rr = doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	res := decodeGovernance(t, rr.Body.Bytes())
	if !res.Configured || !res.Enabled || res.SecurityContactUserID != userID || res.DenyNotifyMinRisk != 6 {
		t.Fatalf("round-trip = %+v", res)
	}
}

func TestKeeperGovernance_PutRejectsNonMemberContact(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"enabled": true, "security_contact_user_id": "ghost-user"}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not a member") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_PutRejectsNonAdminContact(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO users (id, email) VALUES ('viewer-u', 'viewer@example.com')`)
	execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-viewer', ?, 'viewer-u', 'VIEWER')`, wsID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut,
		`{"enabled": true, "security_contact_user_id": "viewer-u"}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "OWNER or ADMIN") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestKeeperGovernance_PutRejectsRiskOutOfRange(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodPut, `{"enabled": true, "deny_notify_min_risk": 11}`, wsID, userID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// ---- HandleRequest governance behavior ----

// governanceEnable writes an explicit enabled row for the fixture workspace.
func governanceEnable(t *testing.T, db *sql.DB, wsID, contact string, minRisk int) {
	t.Helper()
	err := governance.Upsert(context.Background(), db, wsID, governance.Settings{
		Enabled:               true,
		SecurityContactUserID: contact,
		DenyNotifyMinRisk:     minRisk,
	}, "")
	if err != nil {
		t.Fatalf("governance enable: %v", err)
	}
}

// TestCovKReq_Escalate_TargetsSecurityContactAndPushesInbox — with a
// configured security contact, the ESCALATE inbox item targets that user
// and the broadcaster gets both the inbox invalidation and the user ping.
func TestKeeperGovernance_EscalateTargetsContactAndPushes(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	// The fixture user is the workspace OWNER — a valid security contact.
	var ownerID string
	if err := db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id = ?`, wsID).Scan(&ownerID); err != nil {
		t.Fatalf("fixture owner: %v", err)
	}
	governanceEnable(t, db, wsID, ownerID, 7)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionEscalate), Reason: "needs human", RiskScore: 7,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "drop the production database",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())

	// Superset targeting: the contact is highlighted via target_user_id AND
	// the MANAGER fanout is kept, so managers still see it as a fallback.
	var targetUser, targetRole string
	if err := db.QueryRow(`SELECT COALESCE(target_user_id, ''), COALESCE(target_role, '') FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).Scan(&targetUser, &targetRole); err != nil {
		t.Fatalf("inbox item: %v", err)
	}
	if targetUser != ownerID {
		t.Errorf("target_user_id = %q, want security contact %q", targetUser, ownerID)
	}
	if targetRole != "MANAGER" {
		t.Errorf("target_role = %q, want MANAGER kept as fallback", targetRole)
	}
	if len(bc.inboxUpdated) != 1 {
		t.Fatalf("inbox.updated broadcasts = %d, want 1", len(bc.inboxUpdated))
	}
}

// Without any governance row the legacy behavior holds: role-targeted item,
// but the realtime inbox push still fires (it is a bug fix, not a feature
// gate — every other inbox producer already pushes).
func TestKeeperGovernance_EscalateWithoutRowStillPushes(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionEscalate), Reason: "needs human", RiskScore: 7,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "drop the production database",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())

	var targetUser, targetRole string
	if err := db.QueryRow(`SELECT COALESCE(target_user_id, ''), COALESCE(target_role, '') FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).Scan(&targetUser, &targetRole); err != nil {
		t.Fatalf("inbox item: %v", err)
	}
	if targetUser != "" || targetRole != "MANAGER" {
		t.Errorf("target = (%q, %q), want legacy ('', MANAGER)", targetUser, targetRole)
	}
	if len(bc.inboxUpdated) != 1 {
		t.Fatalf("inbox.updated broadcasts = %d, want 1", len(bc.inboxUpdated))
	}
}

// High-risk DENY lands in the inbox only for workspaces that opted in.
func TestKeeperGovernance_HighRiskDenyNotifiesWhenEnabled(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	var ownerID string
	if err := db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id = ?`, wsID).Scan(&ownerID); err != nil {
		t.Fatalf("fixture owner: %v", err)
	}
	governanceEnable(t, db, wsID, ownerID, 7)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "credential probing", RiskScore: 9,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "read all the secrets",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())

	var blocking int
	var targetUser, targetRole string
	if err := db.QueryRow(`SELECT blocking, COALESCE(target_user_id, ''), COALESCE(target_role, '') FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).Scan(&blocking, &targetUser, &targetRole); err != nil {
		t.Fatalf("DENY inbox item not written: %v", err)
	}
	if blocking != 0 {
		t.Errorf("DENY notify must be non-blocking (informational), got blocking=%d", blocking)
	}
	if targetUser != ownerID || targetRole != "MANAGER" {
		t.Errorf("target = (%q, %q), want contact %q + MANAGER fallback", targetUser, targetRole, ownerID)
	}
	if len(bc.inboxUpdated) != 1 {
		t.Fatalf("inbox.updated broadcasts = %d, want 1", len(bc.inboxUpdated))
	}
}

func TestKeeperGovernance_HighRiskDenySilentWhenNotOptedIn(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "credential probing", RiskScore: 9,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "read all the secrets",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("DENY without governance opt-in wrote %d inbox items, want 0", n)
	}
}

// Below-threshold DENY stays out of the inbox even when opted in.
func TestKeeperGovernance_LowRiskDenySilent(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	governanceEnable(t, db, wsID, "", 7)

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "not needed for task", RiskScore: 4,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "just curious",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKReqDecode(t, rr.Body.Bytes())

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items
		WHERE workspace_id = ? AND source_id = ?`, wsID, res.RequestID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("low-risk DENY wrote %d inbox items, want 0", n)
	}
}
