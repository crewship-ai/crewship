package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// Decomposition under human authority. A lead asked (in chat, or through an
// issue a person started) to check two hosts used to run
// create-then-link: the create succeeded, the sub_issue_of link was refused
// with chat_issue_action_not_supported, and an unattached child was left
// behind. issue.create with parent_identifier does both in one transaction.

func parentLookup(t *testing.T, f chatIssueFixture, identifier string) string {
	t.Helper()
	var parent *string
	if err := f.h.db.QueryRow(`SELECT parent_issue_id FROM missions WHERE identifier = ?`, identifier).Scan(&parent); err != nil {
		t.Fatalf("lookup %s: %v", identifier, err)
	}
	if parent == nil {
		return ""
	}
	return *parent
}

func TestInternalIssueCreate_WithParent_HumanAuthority(t *testing.T) {
	f := newChatIssueFixture(t)
	parentID := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-1", "IN_PROGRESS")

	body := f.createBody("split-host-a", "Host A: collect status")
	body["parent_identifier"] = "PAR-1"
	created := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusCreated)
	child := created["identifier"].(string)
	if got := parentLookup(t, f, child); got != parentID {
		t.Fatalf("child %s parent = %q, want %q", child, got, parentID)
	}

	// Idempotent retry: same key, same request → the same child, no second row.
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusOK)
	if replay["id"] != created["id"] {
		t.Fatalf("replay created a different child: %v vs %v", replay, created)
	}
	chatIssueCount(t, f, "missions", 2) // parent + one child

	// Same key, different parent: a conflict, never a silent re-parent.
	otherParent := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-2", "BACKLOG")
	_ = otherParent
	body["parent_identifier"] = "PAR-2"
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusConflict)
	if got := parentLookup(t, f, child); got != parentID {
		t.Fatalf("conflicting replay re-parented the child to %q", got)
	}
}

func TestInternalIssueCreate_WithParent_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, f chatIssueFixture) string // returns parent identifier
		status int
		msg    string
	}{
		{"unknown parent", func(t *testing.T, f chatIssueFixture) string { return "NOPE-9" }, http.StatusNotFound, "not found"},
		{"foreign workspace parent is indistinguishable from unknown", func(t *testing.T, f chatIssueFixture) string {
			_, ident := seedForeignWorkspaceIssue(t, f.h.db, "parent")
			return ident
		}, http.StatusNotFound, "not found"},
		{"unconnected crew", func(t *testing.T, f chatIssueFixture) string {
			execOrFatal(t, f.h.db, `INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-unrelated', ?, 'Unrelated', 'unrelated', 'UNR')`, f.workspace)
			seedIssue(t, f.h.db, f.workspace, "crew-unrelated", f.agent, "UNR-1", "BACKLOG")
			return "UNR-1"
		}, http.StatusForbidden, "not connected"},
		{"closed parent", func(t *testing.T, f chatIssueFixture) string {
			seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-4", "DONE")
			return "PAR-4"
		}, http.StatusConflict, "reopen it"},
		{"parent in review", func(t *testing.T, f chatIssueFixture) string {
			seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-5", "REVIEW")
			return "PAR-5"
		}, http.StatusConflict, "reopen it"},
		{"non-issue mission is indistinguishable from unknown", func(t *testing.T, f chatIssueFixture) string {
			seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-6", "BACKLOG")
			execOrFatal(t, f.h.db, `UPDATE missions SET mission_type = 'orchestration' WHERE identifier = 'PAR-6'`)
			return "PAR-6"
		}, http.StatusNotFound, "not found"},
		{"one-way connection from the parent's crew only", func(t *testing.T, f chatIssueFixture) string {
			execOrFatal(t, f.h.db, `INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-up', ?, 'Up', 'up', 'UPC')`, f.workspace)
			execOrFatal(t, f.h.db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status) VALUES ('cc-rev', ?, 'crew-up', ?, 'unidirectional', 'active')`, f.workspace, f.crew)
			seedIssue(t, f.h.db, f.workspace, "crew-up", f.agent, "UPC-1", "BACKLOG")
			return "UPC-1"
		}, http.StatusForbidden, "not connected"},
		{"inactive connection", func(t *testing.T, f chatIssueFixture) string {
			execOrFatal(t, f.h.db, `INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-off', ?, 'Off', 'off', 'OFF')`, f.workspace)
			execOrFatal(t, f.h.db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status) VALUES ('cc-off', ?, ?, 'crew-off', 'bidirectional', 'inactive')`, f.workspace, f.crew)
			seedIssue(t, f.h.db, f.workspace, "crew-off", f.agent, "OFF-1", "BACKLOG")
			return "OFF-1"
		}, http.StatusForbidden, "not connected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChatIssueFixture(t)
			parent := tc.setup(t, f)
			var before int
			f.h.db.QueryRow(`SELECT COUNT(*) FROM missions`).Scan(&before)
			body := f.createBody("refused-"+tc.name, "Should not exist")
			body["parent_identifier"] = parent
			rr := chatIssueCall(t, f.h.Create, "POST", "", body)
			if rr.Code != tc.status || !strings.Contains(rr.Body.String(), tc.msg) {
				t.Fatalf("status = %d body %s, want %d containing %q", rr.Code, rr.Body.String(), tc.status, tc.msg)
			}
			var after, ops int
			f.h.db.QueryRow(`SELECT COUNT(*) FROM missions`).Scan(&after)
			f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_operations`).Scan(&ops)
			if after != before || ops != 0 {
				t.Fatalf("refusal left state behind: missions %d→%d, operations=%d", before, after, ops)
			}
		})
	}
}

func TestInternalIssueCreate_WithParent_ConnectedCrew(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-devops', ?, 'DevOps', 'devops', 'DEV')`, f.workspace)
	parentID := seedIssue(t, f.h.db, f.workspace, "crew-devops", f.agent, "DEV-7", "BACKLOG")

	body := f.createBody("connected", "Child under a connected crew's issue")
	body["parent_identifier"] = "DEV-7"
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusForbidden)

	execOrFatal(t, f.h.db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status) VALUES ('cc1', ?, ?, 'crew-devops', 'unidirectional', 'active')`, f.workspace, f.crew)
	body["operation_key"] = "connected-2"
	created := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusCreated)
	if got := parentLookup(t, f, created["identifier"].(string)); got != parentID {
		t.Fatalf("parent = %q, want %q", got, parentID)
	}
}

// A bidirectional connection counts in either direction: the parent's crew
// opened it, the child's crew may attach work under the parent.
func TestInternalIssueCreate_WithParent_ReverseBidirectionalConnection(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-ops', ?, 'Ops', 'ops', 'OPS')`, f.workspace)
	parentID := seedIssue(t, f.h.db, f.workspace, "crew-ops", f.agent, "OPS-3", "BACKLOG")
	execOrFatal(t, f.h.db, `INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id, direction, status) VALUES ('cc-bi', ?, 'crew-ops', ?, 'bidirectional', 'active')`, f.workspace, f.crew)

	body := f.createBody("reverse-bi", "Child under a two-way connected crew's issue")
	body["parent_identifier"] = "OPS-3"
	created := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), http.StatusCreated)
	if got := parentLookup(t, f, created["identifier"].(string)); got != parentID {
		t.Fatalf("parent = %q, want %q", got, parentID)
	}
}

// End to end through a routine run that carries a person's chat authority:
// two delegated sub-issues are created and attached, the run completes, and
// the old create-then-link shape is still refused (the guard is not widened).
func TestRoutineUnderHumanAuthority_CreatesAttachedSubIssues(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition string
		wantStatus string
		wantKids   int
	}{
		{
			name:       "create with parent",
			definition: `{"name":"Split","steps":[{"id":"win","type":"crewship","action":"issue.create","args":{"title":"Host A status","parent_identifier":"PAR-1"}},{"id":"pve","type":"crewship","action":"issue.create","args":{"title":"Host B status","parent_identifier":"PAR-1"}}]}`,
			wantStatus: "COMPLETED",
			wantKids:   2,
		},
		{
			name:       "create then link stays refused",
			definition: `{"name":"Split","steps":[{"id":"win","type":"crewship","action":"issue.create","args":{"title":"Host A status"}},{"id":"link","type":"crewship","action":"issue.link","args":{"identifier":"{{ steps.win.output.identifier }}","target_identifier":"PAR-1","relation_type":"sub_issue_of"}}]}`,
			wantStatus: "FAILED",
			wantKids:   0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChatIssueFixture(t)
			parentID := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "PAR-1", "IN_PROGRESS")
			host := NewInternalHandler(f.h.db, "origin-host-token", newTestLogger())
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/internal/issues", host.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.Create))))
			server := httptest.NewServer(mux)
			defer server.Close()
			store := pipeline.NewStore(f.h.db)
			now := time.Now()
			recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "split", Name: "Split", DefinitionJSON: tc.definition, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: f.run, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true})
			if err != nil {
				t.Fatal(err)
			}
			actions := newCrewshipActions(server.URL, "origin-host-token", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
			executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(actions)
			res, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.wantStatus {
				t.Fatalf("status = %s (%+v), want %s", res.Status, res, tc.wantStatus)
			}
			var kids int
			f.h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE parent_issue_id = ?`, parentID).Scan(&kids)
			if kids != tc.wantKids {
				t.Fatalf("attached children = %d, want %d", kids, tc.wantKids)
			}
		})
	}
}

// A requester who may create issues but not update them passes the general
// "created" check; only the parent gate ("updated") refuses attaching work
// under an existing issue. The role is set before the run is admitted so the
// run's human authority is current, not stale.
func TestInternalIssueCreate_WithParent_RequesterWithoutUpdate(t *testing.T) {
	h, workspace, crew, agent, user := newInternalIssueHandler(t)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role = 'VIEWER', capabilities = '["chat","issue.create"]' WHERE workspace_id = ? AND user_id = ?`, workspace, user)
	f := chatIssueFixture{h, workspace, crew, agent, user, "operation-chat", "operation-run"}
	seedChatIssueRun(t, h, workspace, agent, user, f.chat, f.run)
	seedIssue(t, h.db, workspace, crew, agent, "PAR-3", "BACKLOG")

	// Without a parent the same requester may create: the refusal below is
	// the parent gate, not the general one.
	chatIssueJSON(t, chatIssueCall(t, h.Create, "POST", "", f.createBody("plain", "Allowed")), http.StatusCreated)

	body := f.createBody("with-parent", "Refused")
	body["parent_identifier"] = "PAR-3"
	rr := chatIssueCall(t, h.Create, "POST", "", body)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "may not update issues") {
		t.Fatalf("status = %d body %s, want 403 from the parent gate", rr.Code, rr.Body.String())
	}
	chatIssueCount(t, f, "missions", 2) // PAR-3 + the plain issue

	// The requester check runs before the status check: a closed parent
	// answers the same 403, never a 409 that would reveal its status.
	seedIssue(t, h.db, workspace, crew, agent, "PAR-8", "DONE")
	body = f.createBody("with-closed-parent", "Refused")
	body["parent_identifier"] = "PAR-8"
	rr = chatIssueCall(t, h.Create, "POST", "", body)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "may not update issues") {
		t.Fatalf("closed parent: status = %d body %s, want 403 before the status is revealed", rr.Code, rr.Body.String())
	}
}
