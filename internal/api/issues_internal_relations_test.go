package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// issues_internal_relations_test.go — the internal (agent-driven) half of the
// issue-relation surface. The PUBLIC handler (IssueHandler.CreateRelation) has
// been there since v39; the internal one is new, and it is the one an agent
// reaches through the sidecar. It therefore has to re-derive every boundary the
// public handler gets for free from the JWT session:
//
//   - the workspace comes from the X-Internal-Token binding, not the body;
//   - a crew-bound (crwv1) token may only mutate its OWN crew's issues (#1365,
//     the same rule UpdateStatus/CreateComment already carry);
//   - the target issue must live in the bound workspace, and a miss is a 404
//     rather than a "not yours" so the endpoint is not a cross-tenant
//     existence oracle.
//
// The sub-issue direction is deliberately one-way: relation_type
// "sub_issue_of" writes parent_issue_id on the SOURCE issue, which the crew
// gate above already covers. There is no "parent_of" (which would write the
// TARGET row and need a second, differently-scoped gate) — see the PR body.

// relationReq builds a PATCH/POST request carrying the internal-token context
// a crew-bound sidecar would arrive with.
func relationReq(ident, body string, ctx context.Context) *http.Request {
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
	req = req.WithContext(ctx)
	req.SetPathValue("identifier", ident)
	return req
}

// wsBoundCtxRel builds a request context as requireInternal would for a wsv1
// (workspace-bound, crew-less) token.
func wsBoundCtxRel(wsID string) context.Context {
	return context.WithValue(context.Background(), ctxInternalTokenWS, wsID)
}

// seedForeignWorkspaceIssue creates an entirely separate tenant with its own
// crew, lead and issue, and returns the issue identifier. Used as the target of
// the cross-workspace probes.
func seedForeignWorkspaceIssue(t *testing.T, db *sql.DB, suffix string) (foreignWS, identifier string) {
	t.Helper()
	foreignWS = "foreign-ws-" + suffix
	foreignUser := "foreign-user-" + suffix
	foreignCrew := "foreign-crew-" + suffix
	foreignLead := "foreign-lead-" + suffix
	identifier = "FGN-1"

	exec := func(q string, args ...any) {
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed foreign tenant (%s): %v", q, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', ?)`, foreignWS, "foreign-"+suffix)
	exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Foreign Person')`, foreignUser, "foreign-"+suffix+"@example.com")
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"fm-"+suffix, foreignWS, foreignUser)
	exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Foreign', ?, 'FGN')`,
		foreignCrew, foreignWS, "foreign-crew-"+suffix)
	exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
	      VALUES (?, ?, ?, 'FLead', ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		foreignLead, foreignWS, foreignCrew, "flead-"+suffix)
	seedIssue(t, db, foreignWS, foreignCrew, foreignLead, identifier, "BACKLOG")
	return foreignWS, identifier
}

// relationRowCount counts mission_relations rows regardless of tenant — the
// probes assert on the GLOBAL count so a leak into any workspace is caught.
func relationRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_relations`).Scan(&n); err != nil {
		t.Fatalf("count relations: %v", err)
	}
	return n
}

// --- happy paths -----------------------------------------------------------

func TestInternalIssueRelation_Blocks(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	srcID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	tgtID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"blocks"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var src, tgt, typ string
	if err := h.db.QueryRow(`SELECT source_id, target_id, relation_type FROM mission_relations`).
		Scan(&src, &tgt, &typ); err != nil {
		t.Fatalf("read relation: %v", err)
	}
	if src != srcID || tgt != tgtID || typ != "blocks" {
		t.Errorf("relation = (%s,%s,%s), want (%s,%s,blocks)", src, tgt, typ, srcID, tgtID)
	}
}

// blocked_by is stored as the inverse `blocks` row with the endpoints swapped,
// exactly as the public handler normalises it — otherwise the same logical link
// would exist in two shapes and ListRelations' direction flip would double-count.
func TestInternalIssueRelation_BlockedByNormalised(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	srcID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	tgtID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"blocked_by"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var src, tgt, typ string
	if err := h.db.QueryRow(`SELECT source_id, target_id, relation_type FROM mission_relations`).
		Scan(&src, &tgt, &typ); err != nil {
		t.Fatalf("read relation: %v", err)
	}
	if src != tgtID || tgt != srcID || typ != "blocks" {
		t.Errorf("blocked_by must persist as swapped blocks; got (%s,%s,%s), want (%s,%s,blocks)",
			src, tgt, typ, tgtID, srcID)
	}
}

// The decomposition case: a big issue is split, each child is linked to the
// parent, and each child then gets its own agent.
func TestInternalIssueRelation_SubIssueOfSetsParent(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	parentID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-2",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-1","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var parent sql.NullString
	if err := h.db.QueryRow(`SELECT parent_issue_id FROM missions WHERE id = ?`, childID).Scan(&parent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if !parent.Valid || parent.String != parentID {
		t.Errorf("parent_issue_id = %v, want %s", parent, parentID)
	}
	// A sub-issue link is a missions column, not a mission_relations row —
	// the CHECK constraint on relation_type would reject 'sub_issue_of' anyway.
	if n := relationRowCount(t, h.db); n != 0 {
		t.Errorf("sub_issue_of must not write mission_relations, found %d rows", n)
	}
}

func TestInternalIssueRelation_Rejections(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid type", `{"target_identifier":"ENG-2","relation_type":"causes"}`, http.StatusBadRequest},
		{"missing target", `{"relation_type":"blocks"}`, http.StatusBadRequest},
		{"self relation", `{"target_identifier":"ENG-1","relation_type":"blocks"}`, http.StatusBadRequest},
		{"self parent", `{"target_identifier":"ENG-1","relation_type":"sub_issue_of"}`, http.StatusBadRequest},
		{"unknown target", `{"target_identifier":"ENG-99","relation_type":"blocks"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
			seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
			seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

			body := `{"workspace_id":"` + wsID + `","agent_id":"agent-worker",` + tc.body[1:]
			rr := httptest.NewRecorder()
			h.CreateRelation(rr, relationReq("ENG-1", body, crewBoundCtx1186(wsID, crewID)))
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d; body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestInternalIssueRelation_DuplicateConflict(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	body := `{"workspace_id":"` + wsID + `","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"relates_to"}`
	for i, want := range []int{http.StatusCreated, http.StatusConflict} {
		rr := httptest.NewRecorder()
		h.CreateRelation(rr, relationReq("ENG-1", body, crewBoundCtx1186(wsID, crewID)))
		if rr.Code != want {
			t.Fatalf("call %d: status = %d, want %d; body=%s", i, rr.Code, want, rr.Body.String())
		}
	}
}

// --- security: the authorisation boundary ----------------------------------

// A body-declared workspace that disagrees with the token binding is the
// cross-tenant forgery assertInternalTokenWorkspace exists to stop.
func TestSecInternalIssueRelation_BodyWorkspaceMismatchRejected(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"someone-elses-ws","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"blocks"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if n := relationRowCount(t, h.db); n != 0 {
		t.Errorf("no relation may be written on a rejected request, found %d", n)
	}
}

// The source issue belongs to a SIBLING crew in the same workspace. The
// workspace binding alone would let it through — the crew gate is what stops
// it (#1365, the boundary UpdateStatus/CreateComment already hold).
func TestSecInternalIssueRelation_CrossCrewSourceRejected(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")
	_, _, siblingIdent := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq(siblingIdent,
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-1","relation_type":"blocks"}`,
		crewBoundCtx1186(wsID, crewA)))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a sibling crew's issue; body=%s", rr.Code, rr.Body.String())
	}
	if n := relationRowCount(t, h.db); n != 0 {
		t.Errorf("no relation may be written across the crew boundary, found %d", n)
	}
}

// Same shape, but the write lands in a missions column rather than a relations
// row: a crew-A token must not be able to re-parent crew B's issue.
func TestSecInternalIssueRelation_CrossCrewSubIssueRejected(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")
	_, _, siblingIdent := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq(siblingIdent,
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-1","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewA)))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	var parent sql.NullString
	if err := h.db.QueryRow(`SELECT parent_issue_id FROM missions WHERE identifier = ?`, siblingIdent).Scan(&parent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if parent.Valid {
		t.Errorf("crew B's issue must not be re-parented, got parent %q", parent.String)
	}
}

// The target lives in another TENANT. A workspace-bound token's own issue is
// the source, so the crew/workspace gate on the source passes — the target
// lookup is the only thing standing between the agent and a cross-tenant link.
// It must answer 404 (not 403): a distinguishable answer would confirm the
// foreign identifier exists.
func TestSecInternalIssueRelation_CrossWorkspaceTargetRejected(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	_, foreignIdent := seedForeignWorkspaceIssue(t, h.db, "rel")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"`+foreignIdent+`","relation_type":"relates_to"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a foreign-tenant target; body=%s", rr.Code, rr.Body.String())
	}
	if n := relationRowCount(t, h.db); n != 0 {
		t.Errorf("no cross-tenant relation may be written, found %d", n)
	}
}

// Same probe on the parent path: parent_issue_id must not be able to point at
// another tenant's issue.
func TestSecInternalIssueRelation_CrossWorkspaceParentRejected(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	childID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	_, foreignIdent := seedForeignWorkspaceIssue(t, h.db, "parent")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"`+foreignIdent+`","relation_type":"sub_issue_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	var parent sql.NullString
	if err := h.db.QueryRow(`SELECT parent_issue_id FROM missions WHERE id = ?`, childID).Scan(&parent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if parent.Valid {
		t.Errorf("parent_issue_id must stay NULL, got %q", parent.String)
	}
}

// Negative control: over-blocking is its own bug. A workspace-bound (wsv1)
// token has no crew binding and keeps workspace-wide reach.
func TestInternalIssueRelation_WorkspaceBoundTokenAllowed(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")
	_, _, siblingIdent := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq(siblingIdent,
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-1","relation_type":"relates_to"}`,
		wsBoundCtxRel(wsID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("a wsv1 token must keep workspace-wide reach; status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// Negative control: the crew-bound token's OWN crew is untouched by the gate.
func TestInternalIssueRelation_SameCrewAllowed(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"duplicate_of"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusCreated {
		t.Fatalf("same-crew link must be allowed; status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// The activity feed is the audit trail humans read. An agent-driven link has to
// leave the same row a human-driven one does, attributed to the AGENT.
func TestInternalIssueRelation_LogsActivityAsAgent(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.CreateRelation(rr, relationReq("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","target_identifier":"ENG-2","relation_type":"blocks"}`,
		crewBoundCtx1186(wsID, crewID)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var actorType, actorID, action string
	if err := h.db.QueryRow(
		`SELECT actor_type, actor_id, action FROM mission_activity ORDER BY created_at DESC LIMIT 1`).
		Scan(&actorType, &actorID, &action); err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if actorType != "agent" || actorID != "agent-worker" || action != "relation_added" {
		t.Errorf("activity = (%s,%s,%s), want (agent,agent-worker,relation_added)", actorType, actorID, action)
	}
}
