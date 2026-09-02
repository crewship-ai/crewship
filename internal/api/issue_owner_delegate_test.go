package api

// Owner/delegate coverage for PRD-ISSUES-AND-ROUTINES-2026 work package A10
// (invariant I5, scenario 9, F62).
//
// Three things are proven here, each with its own test:
//
//  1. scenario 9 — delegating an issue that already has a human owner to an
//     agent (AssignmentHandler.Create, the /assign endpoint —
//     assignments_run.go — the exact site rev-1 dev1 observation 11 found
//     overwriting the owner) must leave owner_user_id untouched and set
//     delegate_agent_id.
//  2. F62 — Start used to accept any assignee_id that merely EXISTED,
//     whether it named a user or an agent. A user-owned issue with no
//     agent delegate (owner_user_id set, delegate_agent_id NULL — the
//     A10-typed equivalent of the old "assignee_type='user'" case) must
//     now be refused with a named 400 before any mission_task or run is
//     created, not fall through into a 500 from a dangling FK on
//     mission_tasks.assigned_agent_id.
//  3. legacy projection — a client that still reads assignee_type/
//     assignee_id (the compatibility projection A10 keeps for the
//     migration window) gets a coherent answer after delegation: the
//     legacy pair names the SAME agent as the new delegate field, not a
//     stale or empty value.
//  4. unassign coherence — the public PATCH's explicit unassign
//     (assignee_id: "") must clear owner_user_id and delegate_agent_id
//     exactly like the internal agent-facing Update already does, not just
//     the legacy assignee_type/assignee_id pair.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIssueDelegate_PreservesOwner_Scenario9 is PRD §18 scenario 9: "Human
// owner stays owner after delegating to an agent."
func TestIssueDelegate_PreservesOwner_Scenario9(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)

	// covAsgRig's setupTestDB → seedTestUser seeds exactly one user,
	// "test-user-id", already a member of wsID (seedTestWorkspace) — reuse
	// it as the issue's human owner rather than seeding a second one.
	ownerUserID := "test-user-id"

	// A missions row whose id == chat_id activates the issue-mirroring
	// path in Create (the same precondition TestAssignmentCreateCov_
	// MissionLinked_CommentAndAssignee uses), pre-seeded with a human
	// owner exactly as if a person had filed and claimed the issue before
	// ever delegating it to an agent.
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
			owner_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-s9', 'Owned issue', 'IN_PROGRESS', ?, datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID, ownerUserID); err != nil {
		t.Fatalf("seed owned mission: %v", err)
	}

	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"build the thing","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("delegate: status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var gotOwner, gotDelegate string
	if err := h.db.QueryRow(
		`SELECT COALESCE(owner_user_id,''), COALESCE(delegate_agent_id,'') FROM missions WHERE id = ?`, chatID,
	).Scan(&gotOwner, &gotDelegate); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if gotOwner != ownerUserID {
		t.Errorf("owner_user_id = %q after delegating to an agent, want unchanged %q (I5 violated)", gotOwner, ownerUserID)
	}
	if gotDelegate != workerID {
		t.Errorf("delegate_agent_id = %q, want %q", gotDelegate, workerID)
	}
}

// TestIssueStart_RefusesNonAgentDelegate_F62: an issue whose only recorded
// assignee is a human owner (owner_user_id set, delegate_agent_id left
// NULL — nobody was ever delegated an agent to execute the work) must be
// refused at Start with a named 400, not accepted and then fail deep inside
// task creation. Before the fix, Start checked only that ANY assignee_id
// existed — a user id passed that check exactly as an agent id would, and
// the mission_tasks INSERT below it (assigned_agent_id REFERENCES
// agents(id)) would then either violate the FK (500) or, worse, succeed
// silently for a caller lucky enough that the id happened to collide with
// something in the agents table.
func TestIssueStart_RefusesNonAgentDelegate_F62(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	// Owner set, no delegate — the A10 shape of "this issue has a human on
	// it but nobody has been delegated to run it".
	if _, err := h.db.Exec(`UPDATE missions SET owner_user_id = ?, assignee_type = 'user', assignee_id = ? WHERE id = ?`,
		userID, userID, id); err != nil {
		t.Fatalf("seed owner-only mission: %v", err)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code < 400 || rr.Code >= 500 {
		t.Fatalf("status = %d, want a named 4xx (not a crash, not success); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "delegate") {
		t.Errorf("body = %q, want it to name the missing delegate (F62's whole point is a named error)", rr.Body.String())
	}

	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("query mission status: %v", err)
	}
	if status != "BACKLOG" {
		t.Errorf("mission status = %q, want unchanged BACKLOG — Start must not have proceeded", status)
	}
	var taskCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, id).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 0 {
		t.Errorf("mission_tasks count = %d, want 0 — no run may be created for a non-agent delegate", taskCount)
	}
}

// TestIssueStart_RefusesDeletedDelegate_F62 covers the other half of "exists
// vs executable": delegate_agent_id pointed at a real agent at delegation
// time, but the agent has since been soft-deleted. The typed FK (ON DELETE
// SET NULL, hard delete) doesn't catch a soft delete, so Start's own
// deleted_at check is what closes this gap.
func TestIssueStart_RefusesDeletedDelegate_F62(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET delegate_agent_id = ?, assignee_type = 'agent', assignee_id = ? WHERE id = ?`,
		workerID, workerID, id); err != nil {
		t.Fatalf("seed delegate: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE agents SET deleted_at = datetime('now') WHERE id = ?`, workerID); err != nil {
		t.Fatalf("soft-delete delegate agent: %v", err)
	}

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-2")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a soft-deleted delegate; body=%s", rr.Code, rr.Body.String())
	}
	var taskCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, id).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 0 {
		t.Errorf("mission_tasks count = %d, want 0", taskCount)
	}
}

// TestIssueDelegate_LegacyAssigneeProjectionStaysCoherent: an old client
// that never learned about owner/delegate and only reads assignee_type/
// assignee_id must still see a coherent answer after a delegation — the
// same agent the new delegate field names, not a stale or empty legacy
// pair.
func TestIssueDelegate_LegacyAssigneeProjectionStaysCoherent(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status,
			owner_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'trace-legacy', 'Owned issue', 'IN_PROGRESS', 'test-user-id', datetime('now'), datetime('now'))`,
		chatID, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed owned mission: %v", err)
	}

	rr := covAsgPost(t, h, `{"target_slug":"asg-worker","task":"build the thing","crew_id":"`+crewID+`","workspace_id":"`+wsID+`","chat_id":"`+chatID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("delegate: status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var assigneeType, assigneeID, delegateAgentID string
	if err := h.db.QueryRow(
		`SELECT COALESCE(assignee_type,''), COALESCE(assignee_id,''), COALESCE(delegate_agent_id,'') FROM missions WHERE id = ?`,
		chatID,
	).Scan(&assigneeType, &assigneeID, &delegateAgentID); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if assigneeType != "agent" {
		t.Errorf("legacy assignee_type = %q, want 'agent' (old client must still see the delegation)", assigneeType)
	}
	if assigneeID != workerID {
		t.Errorf("legacy assignee_id = %q, want %q", assigneeID, workerID)
	}
	if assigneeID != delegateAgentID {
		t.Errorf("legacy assignee_id (%q) and new delegate_agent_id (%q) disagree — not a coherent answer", assigneeID, delegateAgentID)
	}
}

// TestIssueUpdate_Unassign_ClearsTypedColumns_A10 is the public-PATCH
// counterpart of the internal handler's unassign fix
// (issues_internal.go): an explicit unassign (assignee_id: "") must clear
// owner_user_id and delegate_agent_id, not just the legacy
// assignee_type/assignee_id pair — otherwise the compatibility projection
// and the typed columns disagree the moment anyone unassigns through the
// public API.
func TestIssueUpdate_Unassign_ClearsTypedColumns_A10(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := h.db.Exec(
		`UPDATE missions SET owner_user_id = ?, delegate_agent_id = ?, assignee_type = 'agent', assignee_id = ? WHERE id = ?`,
		userID, workerID, workerID, id); err != nil {
		t.Fatalf("seed both typed columns: %v", err)
	}

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"assignee_id": ""})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var ownerUserID, delegateAgentID, assigneeType, assigneeID sql.NullString
	if err := h.db.QueryRow(
		`SELECT owner_user_id, delegate_agent_id, assignee_type, assignee_id FROM missions WHERE id = ?`, id,
	).Scan(&ownerUserID, &delegateAgentID, &assigneeType, &assigneeID); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if ownerUserID.Valid {
		t.Errorf("owner_user_id = %q after unassign, want NULL", ownerUserID.String)
	}
	if delegateAgentID.Valid {
		t.Errorf("delegate_agent_id = %q after unassign, want NULL", delegateAgentID.String)
	}
	if assigneeType.Valid && assigneeType.String != "" {
		t.Errorf("legacy assignee_type = %q after unassign, want empty", assigneeType.String)
	}
	if assigneeID.Valid && assigneeID.String != "" {
		t.Errorf("legacy assignee_id = %q after unassign, want empty", assigneeID.String)
	}
}
