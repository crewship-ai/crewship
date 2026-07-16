package api

// Tests for #1209 — ephemeral-hire PENDING_REVIEW waitpoints must be
// visible (and approvable) through the approvals surface instead of
// living in a fully disconnected fourth HITL lifecycle.
//
// The read model: a guided-autonomy hire stages an agent row in
// PENDING_REVIEW plus a blocking inbox waitpoint. Those staged hires
// now appear in GET /api/v1/approvals as synthetic pending rows with
// kind=agent_hire (id = the agent id), are fetchable via
// GET /api/v1/approvals/{agentId}, and an approve decision delegates
// to the exact same state machine as POST /agents/{id}/approve-hire.
// Deny is rejected with a hint — a staged hire has no deny lifecycle
// today (it ghosts on TTL), and inventing one here would be a second
// state machine.

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// approvalsHireRig builds a guided-autonomy crew fixture plus both
// handlers, with the approvals handler wired to delegate hire
// approvals to the agents handler (mirrors router_orchestration.go).
func approvalsHireRig(t *testing.T) (*ApprovalsHandler, *AgentHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	agents := NewAgentHandler(db, logger)
	ah := NewApprovalsHandler(db, logger, noopEmitter{})
	ah.SetHireApprover(agents)
	return ah, agents, db, userID, wsID, crewID
}

func approvalsListRows(t *testing.T, ah *ApprovalsHandler, userID, wsID, query string) []map[string]any {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals"+query, nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	ah.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	return resp.Rows
}

// findRow returns the row whose "id" equals id, or nil.
func findRow(rows []map[string]any, id string) map[string]any {
	for _, r := range rows {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

// ── List ────────────────────────────────────────────────────────────────

func TestApprovals_List_IncludesPendingHire(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-list")
	// A real queue row alongside, to prove merging (not replacement).
	queueID := enqueueApproval(t, ah, wsID, userID, "real queue row")

	rows := approvalsListRows(t, ah, userID, wsID, "")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (queue row + staged hire)", len(rows))
	}
	if findRow(rows, queueID) == nil {
		t.Errorf("real approvals_queue row %s missing from merged list", queueID)
	}
	hire := findRow(rows, "a-hire-list")
	if hire == nil {
		t.Fatalf("staged hire a-hire-list not in approvals list; rows: %v", rows)
	}
	if hire["kind"] != "agent_hire" {
		t.Errorf("hire row kind = %v, want agent_hire", hire["kind"])
	}
	if hire["status"] != "pending" {
		t.Errorf("hire row status = %v, want pending", hire["status"])
	}
	if hire["agent_id"] != "a-hire-list" {
		t.Errorf("hire row agent_id = %v, want a-hire-list", hire["agent_id"])
	}
	payload, _ := hire["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("hire row payload missing: %v", hire)
	}
	decideVia, _ := payload["decide_via"].(string)
	if !strings.Contains(decideVia, "crewship hire approve a-hire-list") {
		t.Errorf("payload decide_via = %q, want a `crewship hire approve a-hire-list` hint", decideVia)
	}
}

func TestApprovals_List_StatusPending_IncludesHire_StatusApprovedExcludes(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-filter")

	pending := approvalsListRows(t, ah, userID, wsID, "?status=pending")
	if findRow(pending, "a-hire-filter") == nil {
		t.Errorf("staged hire missing from status=pending list")
	}
	approved := approvalsListRows(t, ah, userID, wsID, "?status=approved")
	if findRow(approved, "a-hire-filter") != nil {
		t.Errorf("staged hire must not appear under status=approved (hires only surface while pending)")
	}
}

func TestApprovals_List_HireScopedToWorkspace(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-scope")

	// A second workspace the caller is OWNER of; the hire belongs to
	// the first one and must not leak here.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed second workspace: %v", err)
	}
	rows := approvalsListRows(t, ah, userID, "ws-other", "")
	if findRow(rows, "a-hire-scope") != nil {
		t.Errorf("staged hire leaked into another workspace's approvals list")
	}
}

// ── Get ─────────────────────────────────────────────────────────────────

func TestApprovals_Get_PendingHire(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-get")

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals/a-hire-get", nil), userID, wsID, "OWNER")
	req.SetPathValue("id", "a-hire-get")
	rr := httptest.NewRecorder()
	ah.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if row["kind"] != "agent_hire" || row["status"] != "pending" || row["id"] != "a-hire-get" {
		t.Errorf("get returned %v, want kind=agent_hire status=pending id=a-hire-get", row)
	}
}

func TestApprovals_Get_HireCrossWorkspace404(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-xws")
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed second workspace: %v", err)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals/a-hire-xws", nil), userID, "ws-other", "OWNER")
	req.SetPathValue("id", "a-hire-xws")
	rr := httptest.NewRecorder()
	ah.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace hire get status = %d, want 404", rr.Code)
	}
}

func TestApprovals_Get_ApprovedHireGone404(t *testing.T) {
	ah, agents, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-done")
	if rr := postApproveHire(t, agents, userID, wsID, "OWNER", "a-hire-done"); rr.Code != http.StatusOK {
		t.Fatalf("approve-hire status = %d, want 200", rr.Code)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals/a-hire-done", nil), userID, wsID, "OWNER")
	req.SetPathValue("id", "a-hire-done")
	rr := httptest.NewRecorder()
	ah.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("approved hire get status = %d, want 404 (only pending hires surface)", rr.Code)
	}
}

// ── Decide ──────────────────────────────────────────────────────────────

func postApprovalsDecide(t *testing.T, ah *ApprovalsHandler, userID, wsID, role, id, status string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"status":"` + status + `"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/approvals/"+id+"/decide", body), userID, wsID, role)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	ah.Decide(rr, req)
	return rr
}

func TestApprovals_Decide_ApprovesPendingHire(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-decide")

	rr := postApprovalsDecide(t, ah, userID, wsID, "OWNER", "a-hire-decide", "approved")
	if rr.Code != http.StatusOK {
		t.Fatalf("decide status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status    string `json:"status"`
		DecidedBy string `json:"decided_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal decide: %v", err)
	}
	if out.Status != "approved" || out.DecidedBy != userID {
		t.Errorf("decide response = %+v, want status=approved decided_by=%s", out, userID)
	}

	// Delegation hit the real approve-hire state machine: agent IDLE…
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = 'a-hire-decide'`).Scan(&status); err != nil {
		t.Fatalf("verify agent status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agent status after decide = %q, want IDLE", status)
	}
	// …and the blocking inbox waitpoint resolved.
	var state string
	if err := db.QueryRow(`SELECT state FROM inbox_items WHERE source_id = 'a-hire-decide' AND kind = 'waitpoint'`).Scan(&state); err != nil {
		t.Fatalf("verify inbox state: %v", err)
	}
	if state != "resolved" {
		t.Errorf("inbox waitpoint state after decide = %q, want resolved", state)
	}
	// Second decide is an honest conflict, not a silent re-approve.
	rr2 := postApprovalsDecide(t, ah, userID, wsID, "OWNER", "a-hire-decide", "approved")
	if rr2.Code != http.StatusNotFound && rr2.Code != http.StatusConflict {
		t.Errorf("second decide status = %d, want 404 or 409", rr2.Code)
	}
}

func TestApprovals_Decide_DenyPendingHire_ConflictWithHint(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-deny")

	rr := postApprovalsDecide(t, ah, userID, wsID, "OWNER", "a-hire-deny", "denied")
	if rr.Code != http.StatusConflict {
		t.Fatalf("deny status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "TTL") {
		t.Errorf("deny body should explain the TTL-ghost lifecycle; got: %s", rr.Body.String())
	}
	// Deny must not have touched the agent.
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = 'a-hire-deny'`).Scan(&status); err != nil {
		t.Fatalf("verify agent status: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("agent status after rejected deny = %q, want PENDING_REVIEW", status)
	}
}

func TestApprovals_Decide_HireCrossWorkspace404(t *testing.T) {
	ah, _, db, userID, wsID, crewID := approvalsHireRig(t)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-xdec")
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed second workspace: %v", err)
	}
	rr := postApprovalsDecide(t, ah, userID, "ws-other", "OWNER", "a-hire-xdec", "approved")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace hire decide status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = 'a-hire-xdec'`).Scan(&status); err != nil {
		t.Fatalf("verify agent status: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("cross-workspace decide mutated the agent: status = %q", status)
	}
}

func TestApprovals_Decide_HireWithoutApproverWired_ConflictHint(t *testing.T) {
	// A handler without SetHireApprover (defensive: misconstructed
	// router) must not 500 or silently 404 — it points at the CLI flow.
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ah := NewApprovalsHandler(db, logger, noopEmitter{})
	seedPendingReviewAgent(t, db, wsID, crewID, "a-hire-nowire")

	rr := postApprovalsDecide(t, ah, userID, wsID, "OWNER", "a-hire-nowire", "approved")
	if rr.Code != http.StatusConflict {
		t.Fatalf("unwired decide status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "hire approve") {
		t.Errorf("unwired decide should hint at `crewship hire approve`; got: %s", rr.Body.String())
	}
}
