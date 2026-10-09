package chain

// links_test.go — #2986, under docs/specs/activity-links.md.
//
// Two halves. The walk returned every inbox item a run produced, whatever the
// item's audience, so a member could read a manager-targeted ask's title
// through /chains. And it missed links the schema already stores: issue ↔
// assignment on assignments.mission_id, issue → run on issue_executions, run →
// issue on missions.author_run_id, and the run_needs_human asks. Every new
// edge is a stored column, fenced to the workspace.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memberView is the viewer the API builds for a MEMBER: untargeted items,
// items aimed at them, and items aimed at a role at or below theirs.
func memberView(userID string) func(targetUserID, targetRole string) bool {
	rank := map[string]int{"VIEWER": 1, "MEMBER": 2, "MANAGER": 3, "ADMIN": 4, "OWNER": 5}
	return func(targetUserID, targetRole string) bool {
		if targetUserID == "" && targetRole == "" {
			return true
		}
		if targetUserID != "" && targetUserID == userID {
			return true
		}
		return rank[targetRole] > 0 && rank[targetRole] <= rank["MEMBER"]
	}
}

func (r *rig) seedTargetedInbox(t *testing.T, id, kind, sourceID, title, payload, targetUser, targetRole string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, payload_json, target_user_id, target_role)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, r.ws, kind, sourceID, title, payload, nullable(targetUser), nullable(targetRole))
}

func TestWalk_InboxItemsFollowTheViewersAudience(t *testing.T) {
	r := newRig(t, "ws_a")
	p := r.seedRoutine(t, "pl_1", r.ws, "nightly")
	run := r.seedRun(t, "run_1", r.ws, p, "nightly", "schedule", "")
	r.seedTargetedInbox(t, "inb_open", "failed_run", run, "Everyone may see this", `{"run_id":"run_1"}`, "", "")
	r.seedTargetedInbox(t, "inb_mgr", "failed_run", run+"-b", "Manager-only budget", `{"run_id":"run_1"}`, "", "MANAGER")
	r.seedTargetedInbox(t, "inb_mine", "failed_run", run+"-c", "For me", `{"run_id":"run_1"}`, "usr_me", "")
	r.seedTargetedInbox(t, "inb_other", "failed_run", run+"-d", "For someone else", `{"run_id":"run_1"}`, "usr_other", "")

	g := walk(t, r, run, Options{CanSeeInbox: memberView("usr_me")})
	for _, id := range []string{"inbox:inb_open", "inbox:inb_mine"} {
		if !hasNode(g, id) {
			t.Errorf("visible item %s missing", id)
		}
	}
	for _, id := range []string{"inbox:inb_mgr", "inbox:inb_other"} {
		if hasNode(g, id) {
			t.Errorf("item %s outside the viewer's audience was returned", id)
		}
	}
	for _, n := range g.Nodes {
		if n.Label == "Manager-only budget" || n.Label == "For someone else" {
			t.Errorf("a hidden item's title leaked as %q", n.ID)
		}
	}
	var declared bool
	for _, gap := range g.Gaps {
		if gap.From == "inbox" && gap.To == "viewer" {
			declared = true
		}
	}
	if !declared {
		t.Errorf("the walk hid items without saying so: gaps = %+v", g.Gaps)
	}
}

func TestWalk_WithoutAViewerOnlyUntargetedInboxItemsShow(t *testing.T) {
	r := newRig(t, "ws_a")
	p := r.seedRoutine(t, "pl_1", r.ws, "nightly")
	run := r.seedRun(t, "run_1", r.ws, p, "nightly", "schedule", "")
	r.seedTargetedInbox(t, "inb_open", "failed_run", run, "open", `{"run_id":"run_1"}`, "", "")
	r.seedTargetedInbox(t, "inb_mgr", "failed_run", run+"-b", "mgr", `{"run_id":"run_1"}`, "", "MANAGER")

	g := walk(t, r, run, Options{})
	if !hasNode(g, "inbox:inb_open") || hasNode(g, "inbox:inb_mgr") {
		t.Errorf("default view: nodes %v, want only the untargeted item", nodeIDs(g))
	}
}

func TestWalk_AHiddenInboxAnchorIsNotFound(t *testing.T) {
	r := newRig(t, "ws_a")
	r.seedTargetedInbox(t, "inb_mgr", "escalation", "esc_1", "mgr", `{}`, "", "MANAGER")
	_, err := Walk(context.Background(), r.db, r.ws, "inb_mgr", Options{CanSeeInbox: memberView("usr_me")})
	if !errors.Is(err, ErrAnchorNotFound) {
		t.Errorf("err = %v, want ErrAnchorNotFound — a hidden item must be indistinguishable from a missing one", err)
	}
}

func TestWalk_IssueReachesAssignmentsByTheirMissionColumn(t *testing.T) {
	r := newRig(t, "ws_a")
	issue := r.seedIssue(t, "msn_1", r.ws, "ENG-1", "Fix it")
	a := r.seedAssignment(t, "asg_mention", r.ws, "answer the mention", "")
	r.exec(t, `UPDATE assignments SET mission_id = ? WHERE id = ?`, issue, a)

	g := walk(t, r, issue, Options{})
	if !hasEdge(g, "issue:msn_1", "assignment:asg_mention", EdgeTriggers) {
		t.Errorf("issue does not reach its mention assignment: %v", nodeIDs(g))
	}
	g = walk(t, r, a, Options{})
	if !hasEdge(g, "issue:msn_1", "assignment:asg_mention", EdgeTriggers) {
		t.Errorf("assignment does not reach back to its issue: %v", nodeIDs(g))
	}
}

func TestWalk_AnAssignmentLinkedTwiceIsListedOnce(t *testing.T) {
	r := newRig(t, "ws_a")
	issue := r.seedIssue(t, "msn_1", r.ws, "ENG-1", "Fix it")
	a := r.seedAssignment(t, "asg_task", r.ws, "do it", "")
	r.exec(t, `UPDATE assignments SET mission_id = ? WHERE id = ?`, issue, a)
	r.seedMissionTask(t, "mt_1", issue, a)

	g := walk(t, r, issue, Options{})
	n := 0
	for _, e := range g.Edges {
		if e.From == "issue:msn_1" && e.To == "assignment:asg_task" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("issue→assignment edges = %d, want 1", n)
	}
}

func TestWalk_IssueReachesTheRoutineRunItsExecutionStarted(t *testing.T) {
	r := newRig(t, "ws_a")
	issue := r.seedIssue(t, "msn_1", r.ws, "ENG-1", "Fix it")
	p := r.seedRoutine(t, "pl_1", r.ws, "fix")
	run := r.seedRun(t, "run_exec", r.ws, p, "fix", "manual", "")
	now := time.Now().UTC().Format(time.RFC3339)
	r.exec(t, `
		INSERT INTO issue_executions (id, mission_id, work_revision, brief_revision, stage, reviewer_agent_id, routine_run_id, created_at, updated_at)
		VALUES ('iex_1', ?, 1, 1, 'working', ?, ?, ?, ?)`, issue, r.agent, run, now, now)

	g := walk(t, r, issue, Options{})
	if !hasEdge(g, "issue:msn_1", "run:run_exec", EdgeTriggers) {
		t.Errorf("issue does not reach the run its execution started: %v", nodeIDs(g))
	}
	g = walk(t, r, run, Options{})
	if !hasEdge(g, "issue:msn_1", "run:run_exec", EdgeTriggers) {
		t.Errorf("run does not reach back to the issue that started it: %v", nodeIDs(g))
	}
}

func TestWalk_RunReachesTheIssueItCreated(t *testing.T) {
	r := newRig(t, "ws_a")
	p := r.seedRoutine(t, "pl_1", r.ws, "triage")
	run := r.seedRun(t, "run_author", r.ws, p, "triage", "schedule", "")
	issue := r.seedIssue(t, "msn_new", r.ws, "ENG-9", "Filed by the routine")
	r.exec(t, `UPDATE missions SET author_run_id = ? WHERE id = ?`, run, issue)

	g := walk(t, r, run, Options{})
	if !hasEdge(g, "run:run_author", "issue:msn_new", EdgeProduces) {
		t.Errorf("run does not reach the issue it created: %v", nodeIDs(g))
	}
	g = walk(t, r, issue, Options{})
	if !hasEdge(g, "run:run_author", "issue:msn_new", EdgeProduces) {
		t.Errorf("issue does not name the run that created it: %v", nodeIDs(g))
	}
}

func TestWalk_RunNeedsHumanAsksAreOutputsOfTheirRunOrAssignment(t *testing.T) {
	r := newRig(t, "ws_a")
	p := r.seedRoutine(t, "pl_1", r.ws, "triage")
	run := r.seedRun(t, "run_rnh", r.ws, p, "triage", "schedule", "")
	r.seedInbox(t, "inb_run", r.ws, "run_needs_human", run, "Routine needs you", `{}`)
	a := r.seedAssignment(t, "asg_rnh", r.ws, "work the issue", "")
	r.seedInbox(t, "inb_asg", r.ws, "run_needs_human", a, "Issue work needs you", `{}`)

	g := walk(t, r, run, Options{})
	if !hasEdge(g, "run:run_rnh", "inbox:inb_run", EdgeProduces) {
		t.Errorf("run does not reach its run_needs_human ask: %v", nodeIDs(g))
	}
	g = walk(t, r, "inb_run", Options{})
	if !hasEdge(g, "run:run_rnh", "inbox:inb_run", EdgeProduces) {
		t.Errorf("ask does not walk back to its run: %v", nodeIDs(g))
	}
	g = walk(t, r, a, Options{})
	if !hasEdge(g, "assignment:asg_rnh", "inbox:inb_asg", EdgeProduces) {
		t.Errorf("assignment does not reach its run_needs_human ask: %v", nodeIDs(g))
	}
	g = walk(t, r, "inb_asg", Options{})
	if !hasEdge(g, "assignment:asg_rnh", "inbox:inb_asg", EdgeProduces) {
		t.Errorf("ask does not walk back to its assignment: %v", nodeIDs(g))
	}
}

func TestWalk_NewLinksNeverCrossTheWorkspace(t *testing.T) {
	r := newRig(t, "ws_a")
	r.seedWorkspace(t, "ws_b", "ws_b-slug")
	p := r.seedRoutine(t, "pl_1", r.ws, "triage")
	run := r.seedRun(t, "run_a", r.ws, p, "triage", "schedule", "")
	// Another tenant's rows name our run and our ids.
	r.exec(t, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_b', 'ws_b', 'B', 'crew_b')`)
	r.exec(t, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agt_b', 'ws_b', 'crew_b', 'B', 'agt_b')`)
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier, author_run_id)
		VALUES ('msn_b', 'ws_b', 'crew_b', 'agt_b', 'tr_b', 'Theirs', 'TODO', 'B-1', ?)`, run)
	r.exec(t, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, payload_json) VALUES ('inb_b', 'ws_b', 'run_needs_human', ?, 'Theirs', '{}')`, run)

	g := walk(t, r, run, Options{})
	if hasNode(g, "issue:msn_b") || hasNode(g, "inbox:inb_b") {
		t.Errorf("another workspace's rows joined the walk: %v", nodeIDs(g))
	}
}
