package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The sub-issue hierarchy must stay a forest, on BOTH paths.
//
// Before this, each parent-setting path rejected only the trivial self-parent,
// so A → B → A took two ordinary calls. That stayed theoretical while a human
// dragging issues in the UI was the only way to set a parent; the agent verb
// makes it a loop a model runs unattended while decomposing a backlog. Nothing
// walks the chain recursively today, so a cycle does not hang a query — it
// makes every issue in the loop its own ancestor, which no roll-up over the
// hierarchy can terminate on.

// setParentDirect writes parent_issue_id straight to the DB, bypassing the
// handlers — used to build the "before" state a probe then tries to close.
func setParentDirect(t *testing.T, db *sql.DB, childID, parentID string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE missions SET parent_issue_id = ? WHERE id = ?`, parentID, childID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
}

func parentOf(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var p sql.NullString
	if err := db.QueryRow(`SELECT parent_issue_id FROM missions WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	return p
}

// --- the internal (agent) path ---------------------------------------------

// A is already B's parent. Making A a sub-issue of B closes the loop.
func TestInternalIssueRelation_RejectsTwoCycle(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	aID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	setParentDirect(t, h.db, bID, aID) // B's parent is A

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a 2-cycle; body=%s", rr.Code, rr.Body.String())
	}
	if p := parentOf(t, h.db, aID); p.Valid {
		t.Errorf("A must stay parentless, got %q", p.String)
	}
}

// Three levels deep: A ← B ← C. Making A a sub-issue of C closes the loop
// through a node that is not the immediate parent, which a one-hop check misses.
func TestInternalIssueRelation_RejectsDeepCycle(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	aID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	cID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-3", "BACKLOG")
	setParentDirect(t, h.db, bID, aID)
	setParentDirect(t, h.db, cID, bID)

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-3","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a 3-node cycle; body=%s", rr.Code, rr.Body.String())
	}
	if p := parentOf(t, h.db, aID); p.Valid {
		t.Errorf("A must stay parentless, got %q", p.String)
	}
}

// Negative control: a legitimate second level under an existing parent is not a
// cycle, and over-blocking would break the decomposition case the verb exists
// for. A ← B, then C under B.
func TestInternalIssueRelation_AllowsDeepeningTheTree(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	aID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	cID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-3", "BACKLOG")
	setParentDirect(t, h.db, bID, aID)

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-3",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if p := parentOf(t, h.db, cID); !p.Valid || p.String != bID {
		t.Errorf("C's parent = %v, want %s", p, bID)
	}
}

// --- the public (human) path ------------------------------------------------

// Same rule, same helper. If only the agent endpoint checked, the two would
// disagree about what the same graph is allowed to look like — and the UI would
// be the easy way to build the cycle the agent was just refused.
func TestIssueUpdate_RejectsParentCycle(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	leadID := "agent-lead"
	aID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	setParentDirect(t, h.db, bID, aID)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"parent_issue_id":"`+bID+`"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if p := parentOf(t, h.db, aID); p.Valid {
		t.Errorf("A must stay parentless, got %q", p.String)
	}
}

// --- the helper itself ------------------------------------------------------

// A pre-existing loop that does NOT include the child must not spin the walk.
// The seen-set is what stops it; without one the loop runs to the depth bound
// and answers "cycle" for a link that is actually fine.
func TestWouldCycleParent_PreExistingUnrelatedLoopTerminates(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	xID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	yID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	newID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-3", "BACKLOG")
	// X ↔ Y, written straight to the DB as a legacy row would be.
	setParentDirect(t, h.db, xID, yID)
	setParentDirect(t, h.db, yID, xID)

	if err := wouldCycleParent(context.Background(), h.db, newID, xID, wsID); err != nil {
		t.Errorf("walk over a pre-existing unrelated loop = %v, want nil", err)
	}
}

// The walk is workspace-scoped: an ancestor in another tenant is not readable,
// and the chain simply terminates there rather than following the link.
func TestWouldCycleParent_ForeignAncestorTerminatesWalk(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	foreignWS, _ := seedForeignWorkspaceIssue(t, h.db, "cycle")
	var foreignIssue string
	if err := h.db.QueryRow(`SELECT id FROM missions WHERE workspace_id = ?`, foreignWS).
		Scan(&foreignIssue); err != nil {
		t.Fatalf("read foreign issue: %v", err)
	}
	setParentDirect(t, h.db, parentID, foreignIssue)

	if err := wouldCycleParent(context.Background(), h.db, childID, parentID, wsID); err != nil {
		t.Errorf("walk = %v, want nil (the chain leaves the workspace and stops)", err)
	}
}

func TestValidIssueDueDate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"2026-09-01", true},
		{"2026-09-01T10:30:00Z", true},
		{"2026-09-01T10:30:00+02:00", true},
		{"", false},
		{"tomorrow", false},
		{"2026-13-01", false},
		{"2026-02-30", false},
		{"2026-9-1", false},
	} {
		if got := validIssueDueDate(tc.in); got != tc.want {
			t.Errorf("validIssueDueDate(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
