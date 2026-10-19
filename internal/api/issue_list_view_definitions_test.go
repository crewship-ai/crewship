package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
)

// The six Issues quick views, one fixture per state that decides membership.
// The definitions live in the table at the top of issue_list_views.go; every
// row below is one line of that table, so a change to a view has to change
// both. Each view is also checked against X-Total-Count and the status counts
// because they share the page's WHERE clause.

func TestIssueList_QuickViewDefinitions(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	db := h.db
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email, full_name) VALUES ('other-user', 'other@example.com', 'Other')`)

	n := 0
	// issue seeds a mission with an owner and a requester (created_by_user_id).
	issue := func(status, owner, requester string) string {
		n++
		ident := "ENG-" + string(rune('A'+n-1))
		id := seedIssue(t, db, wsID, crewID, leadID, ident, status)
		exec(`UPDATE missions SET owner_user_id = NULLIF(?, ''), created_by_user_id = NULLIF(?, ''), delegate_agent_id = ? WHERE id = ?`,
			owner, requester, workerID, id)
		return ident + "|" + id
	}
	split := func(s string) (string, string) {
		for i := range s {
			if s[i] == '|' {
				return s[:i], s[i+1:]
			}
		}
		return s, ""
	}
	session := func(key, state string) {
		_, id := split(key)
		exec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES (?, ?, ?, ?, ?)`,
			"s-"+id, wsID, id, workerID, state)
	}
	execution := func(key, stage string) {
		_, id := split(key)
		exec(`INSERT INTO issue_executions (id, mission_id, work_revision, brief_revision, stage, reviewer_agent_id, created_at, updated_at)
			VALUES (?, ?, 1, 1, ?, ?, '2026-10-19T00:00:00Z', '2026-10-19T00:00:00Z')`, "x-"+id, id, stage, leadID)
	}
	humanHolds := func(key, user string) {
		_, id := split(key)
		exec(`UPDATE issue_work SET mode = 'human', worker_user_id = ? WHERE mission_id = ?`, user, id)
	}
	noClientReview := func(key string) {
		_, id := split(key)
		exec(`UPDATE issue_work SET client_review_required = 0 WHERE mission_id = ?`, id)
	}
	name := func(key string) string { s, _ := split(key); return s }

	// Needs my reply.
	replyAsk := issue("IN_PROGRESS", userID, "")             // agent asks a question
	replyErr := issue("IN_PROGRESS", userID, "")             // session ended in error: needs a person
	replyExec := issue("IN_PROGRESS", userID, "")            // review loop gave up: needs_human
	replyHeld := issue("IN_PROGRESS", "other-user", "")      // owned by someone else, I hold it
	replyOther := issue("IN_PROGRESS", "other-user", userID) // not mine to answer (I only requested it)
	replyDone := issue("DONE", userID, "")                   // closed issues never ask
	replyActive := issue("IN_PROGRESS", userID, "")          // agent is busy, nothing to answer
	replyIdle := issue("IN_PROGRESS", userID, "")            // idle session
	replyFailedExec := issue("IN_PROGRESS", userID, "")      // failed execution is not a question
	session(replyAsk, "awaiting_input")
	session(replyErr, "error")
	execution(replyExec, "needs_human")
	session(replyHeld, "awaiting_input")
	humanHolds(replyHeld, userID)
	session(replyOther, "awaiting_input")
	session(replyDone, "awaiting_input")
	session(replyActive, "active")
	session(replyIdle, "idle")
	execution(replyFailedExec, "failed")

	// Agents working.
	workActive := issue("IN_PROGRESS", "other-user", "")
	workStale := issue("IN_PROGRESS", "other-user", "")
	workHuman := issue("IN_PROGRESS", "other-user", "")
	workIdle := issue("IN_PROGRESS", "other-user", "")
	workDone := issue("DONE", "other-user", "")
	session(workActive, "active")
	session(workStale, "stale")
	session(workHuman, "active")
	humanHolds(workHuman, "other-user")
	session(workIdle, "idle")
	session(workDone, "active")

	// Waiting for my review: the requester accepts the result.
	revMine := issue("REVIEW", "other-user", userID)       // I asked for it, owner is someone else
	revOwnerOnly := issue("REVIEW", userID, "other-user")  // I own it, someone else asked
	revNoClient := issue("REVIEW", "other-user", userID)   // acceptance switched off
	revWrongStatus := issue("IN_PROGRESS", userID, userID) // not in review yet
	revAgentMade := issue("REVIEW", userID, "")            // created by an agent: the owner stands in
	noClientReview(revNoClient)

	// Blocked.
	blocker := issue("IN_PROGRESS", "other-user", "")
	blocked := issue("TODO", "other-user", "")
	doneBlocker := issue("DONE", "other-user", "")
	free := issue("TODO", "other-user", "")
	_, blockerID := split(blocker)
	_, blockedID := split(blocked)
	_, doneID := split(doneBlocker)
	_, freeID := split(free)
	exec(`INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES ('r1', ?, ?, 'blocks')`, blockerID, blockedID)
	exec(`INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES ('r2', ?, ?, 'blocks')`, doneID, freeID)

	// Another workspace in the same shape never leaks into any view.
	exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other-ws')`)
	exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-other', 'ws-other', 'X', 'x', 'XX')`)
	exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES ('agent-other', 'ws-other', 'crew-other', 'A', 'a', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`)
	leak := seedIssue(t, db, "ws-other", "crew-other", "agent-other", "XX-1", "REVIEW")
	exec(`UPDATE missions SET owner_user_id = ?, created_by_user_id = ? WHERE id = ?`, userID, userID, leak)
	exec(`INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state) VALUES ('s-leak', 'ws-other', ?, 'agent-other', 'awaiting_input')`, leak)

	cases := []struct {
		view  string
		query string
		want  []string
	}{
		{"needs my reply", "?needs=reply", []string{name(replyAsk), name(replyErr), name(replyExec), name(replyHeld)}},
		{"agents working", "?working=1", []string{name(replyActive), name(workActive), name(workStale)}},
		{"waiting for my review", "?needs=review", []string{name(revMine), name(revAgentMade)}},
		{"blocked", "?blocked=1", []string{name(blocked)}},
	}
	// Unlisted fixtures are the negatives: owner-only review, acceptance off,
	// wrong status, closed issues, human-held work, idle sessions, failed runs.
	_ = []string{revOwnerOnly, revNoClient, revWrongStatus, replyOther, replyDone, replyIdle, replyFailedExec, workIdle, workHuman}

	for _, tc := range cases {
		t.Run(tc.view, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.List(rr, issueListRequest(t, userID, wsID, tc.query+"&counts=1"))
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
			want := append([]string{}, tc.want...)
			sort.Strings(got)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
			if c := headerInt(t, rr, "X-Total-Count"); c != len(want) {
				t.Fatalf("X-Total-Count = %d, want %d", c, len(want))
			}
		})
	}
}
