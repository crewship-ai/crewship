package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// Quick-view filters on GET /api/v1/issues (ODCH-ISS-03): owner_id,
// delegate_id, work_mode, working, needs=reply|review, blocked. They are
// server-side because a client filter over one loaded page would silently
// drop issues. Each view also has to reach X-Total-Count and the status
// counts, which share the page's WHERE clause.

func TestIssueList_QuickViewFilters(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	db := h.db

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('other-user', 'other@example.com', 'Other')`)

	// A: mine, an agent asked me a question and waits.
	a := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	mustExec(`UPDATE missions SET owner_user_id = ?, delegate_agent_id = ? WHERE id = ?`, userID, workerID, a)
	mustExec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES ('s-a', ?, ?, ?, 'awaiting_input')`, wsID, a, workerID)

	// B: mine, in review.
	b := seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "REVIEW")
	mustExec(`UPDATE missions SET owner_user_id = ? WHERE id = ?`, userID, b)

	// C: someone else's, in review, session awaiting input: must not appear in my views.
	c := seedIssue(t, db, wsID, crewID, leadID, "ENG-3", "REVIEW")
	mustExec(`UPDATE missions SET owner_user_id = 'other-user', delegate_agent_id = ? WHERE id = ?`, workerID, c)
	mustExec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES ('s-c', ?, ?, ?, 'awaiting_input')`, wsID, c, workerID)

	// D: an agent is working it (active session).
	d := seedIssue(t, db, wsID, crewID, leadID, "ENG-4", "IN_PROGRESS")
	mustExec(`UPDATE missions SET delegate_agent_id = ? WHERE id = ?`, workerID, d)
	mustExec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES ('s-d', ?, ?, ?, 'active')`, wsID, d, workerID)

	// E: blocked by an open issue (D). F: blocked by a DONE issue, not blocked.
	e := seedIssue(t, db, wsID, crewID, leadID, "ENG-5", "TODO")
	f := seedIssue(t, db, wsID, crewID, leadID, "ENG-6", "TODO")
	done := seedIssue(t, db, wsID, crewID, leadID, "ENG-7", "DONE")
	mustExec(`INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES ('r1', ?, ?, 'blocks')`, d, e)
	mustExec(`INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES ('r2', ?, ?, 'blocks')`, done, f)

	// G: a human holds the work.
	g := seedIssue(t, db, wsID, crewID, leadID, "ENG-8", "IN_PROGRESS")
	mustExec(`UPDATE issue_work SET mode = 'human', worker_user_id = ? WHERE mission_id = ?`, userID, g)

	// H: another workspace, same shape as A: never leaks.
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other-ws')`)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-other', 'ws-other', 'X', 'x', 'XX')`)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES ('agent-other', 'ws-other', 'crew-other', 'A', 'a', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`)
	hh := seedIssue(t, db, "ws-other", "crew-other", "agent-other", "XX-1", "REVIEW")
	mustExec(`UPDATE missions SET owner_user_id = ? WHERE id = ?`, userID, hh)
	mustExec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES ('s-h', 'ws-other', ?, 'agent-other', 'awaiting_input')`, hh)

	ids := map[string]string{a: "ENG-1", b: "ENG-2", c: "ENG-3", d: "ENG-4", e: "ENG-5", f: "ENG-6", g: "ENG-8"}
	_ = done

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"owner me", "?owner_id=me", []string{"ENG-1", "ENG-2"}},
		{"mine", "?mine=1", []string{"ENG-1", "ENG-2", "ENG-8"}},
		{"owner by id", "?owner_id=other-user", []string{"ENG-3"}},
		{"delegate", "?delegate_id=" + workerID, []string{"ENG-1", "ENG-3", "ENG-4"}},
		{"work mode human", "?work_mode=human", []string{"ENG-8"}},
		{"needs reply", "?needs=reply", []string{"ENG-1"}},
		{"needs review", "?needs=review", []string{"ENG-2"}},
		{"agents working", "?working=1", []string{"ENG-4"}},
		{"blocked", "?blocked=1", []string{"ENG-5"}},
		{"blocked composes with status", "?blocked=1&status=DONE", []string{}},
		{"needs reply composes with delegate", "?needs=reply&delegate_id=nobody", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.List(rr, issueListRequest(t, userID, wsID, tc.query))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
			}
			var page []issueResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := []string{}
			for _, i := range page {
				if i.Identifier != nil {
					got = append(got, *i.Identifier)
				}
			}
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v (known: %v)", got, tc.want, ids)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
			if n := headerInt(t, rr, "X-Total-Count"); n != len(tc.want) {
				t.Fatalf("X-Total-Count = %d, want %d: the count must follow the view", n, len(tc.want))
			}
		})
	}
}

func TestIssueList_QuickViewRejectsBadValues(t *testing.T) {
	h, userID, wsID, _, _, _ := newTestIssueHandler(t)
	for _, q := range []string{"?needs=everything", "?work_mode=robot"} {
		rr := httptest.NewRecorder()
		h.List(rr, issueListRequest(t, userID, wsID, q))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; body=%s", q, rr.Code, rr.Body.String())
		}
	}
}

// Without a person behind the request (a token with no user) "me" views are
// empty rather than falling back to every issue.
func TestIssueList_PersonalViewsNeedAPerson(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	b := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "REVIEW")
	if _, err := h.db.Exec(`UPDATE missions SET owner_user_id = ? WHERE id = ?`, userID, b); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"?needs=review", "?owner_id=me"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/issues"+q, nil)
		req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", q, rr.Code)
		}
		if n := headerInt(t, rr, "X-Total-Count"); n != 0 {
			t.Fatalf("%s: X-Total-Count = %d, want 0 without a user", q, n)
		}
	}
}
