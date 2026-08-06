package api

// The @mention trigger — the tests that define it (#1768 item 3).
//
// These are written as security tests, not feature tests: the interesting
// cases are the ones where a mention must NOT fire, because every one of them
// is a way to make somebody else's agent run.
//
//	 1. an id that names no agent leaves nothing behind;
//	 2. an id from another workspace is a probe, not a typo — no row, no
//	    activity, no dispatch;
//	 3. a mention inside a code fence does not fire, END TO END. The parser is
//	    tested for this too, but "the parser is careful" is a different claim
//	    from "the feature does not fire on documentation", and this page's own
//	    docs print well-formed mentions as examples;
//	 4. the same agent named twice is one mention, one activity row, one run;
//	 5. the delegation caps bound a mention chain — an agent already at the
//	    depth limit cannot mention its way past it. Carries its own mutation:
//	    the identical comment dispatches once the instance setting is raised,
//	    so the test proves the number is READ and not hard-coded;
//	 6. both comment doors parse. Getting one is how this half-works.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── fixture ────────────────────────────────────────────────────────────────

type mentionFixture struct {
	db     *sql.DB
	wsID   string
	userID string
	crewID string
	// author is the agent that writes comments on the agent path.
	author string
	// target is the agent that gets mentioned.
	target    string
	missionID string
	ident     string

	issues   *IssueHandler
	internal *InternalIssueHandler
	assign   *AssignmentHandler
}

func setupMentionFixture(t *testing.T) *mentionFixture {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")

	assign := NewAssignmentHandler(db, nil, nil, "token", newTestLogger())
	t.Cleanup(assign.WaitDispatches)

	issues := NewIssueHandler(db, nil, nil, newTestLogger())
	issues.SetMentionDispatcher(assign)

	internal := NewInternalIssueHandler(db, nil, newTestLogger())
	internal.SetMentionDispatcher(assign)

	return &mentionFixture{
		db: db, wsID: wsID, userID: userID, crewID: crewID,
		author: workerID, target: leadID,
		missionID: missionID, ident: "ENG-1",
		issues: issues, internal: internal, assign: assign,
	}
}

// mentionToken renders the wire format. Written here rather than imported so
// the test asserts against the SPELLING the docs promise, not against whatever
// a helper in the parser happens to produce.
func mentionToken(label, agentID string) string {
	return "[@" + label + "](crewship:agent/" + agentID + ")"
}

// comment posts a comment as a HUMAN through the public handler.
func (f *mentionFixture) comment(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", jsonBody(map[string]string{"body": body}))
	req.SetPathValue("crewId", f.crewID)
	req.SetPathValue("identifier", f.ident)
	req = withWorkspaceUser(req, f.userID, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.issues.CreateComment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("CreateComment status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	f.assign.WaitDispatches()
	return rr
}

// commentAsAgent posts a comment as an AGENT through the internal handler.
func (f *mentionFixture) commentAsAgent(t *testing.T, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", jsonBody(map[string]string{
		"workspace_id": f.wsID, "agent_id": agentID, "body": body,
	}))
	req.SetPathValue("identifier", f.ident)
	rr := httptest.NewRecorder()
	f.internal.CreateComment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("internal CreateComment status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	f.assign.WaitDispatches()
	return rr
}

func (f *mentionFixture) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (f *mentionFixture) mentionRows(t *testing.T) int {
	return f.countRows(t, `SELECT COUNT(*) FROM mission_comment_mentions`)
}

func (f *mentionFixture) mentionActivity(t *testing.T) int {
	return f.countRows(t,
		`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ? AND action = 'mentioned'`, f.missionID)
}

func (f *mentionFixture) assignments(t *testing.T) int {
	return f.countRows(t, `SELECT COUNT(*) FROM assignments`)
}

// nothingHappened is the assertion every "must not fire" test makes. All three
// halves matter: a row with no dispatch is a leak of "this id exists", an
// activity with no row is an un-auditable timeline entry, and a dispatch is
// the actual damage.
func (f *mentionFixture) nothingHappened(t *testing.T, why string) {
	t.Helper()
	if n := f.mentionRows(t); n != 0 {
		t.Errorf("%s: mission_comment_mentions rows = %d, want 0", why, n)
	}
	if n := f.mentionActivity(t); n != 0 {
		t.Errorf("%s: mentioned activity rows = %d, want 0", why, n)
	}
	if n := f.assignments(t); n != 0 {
		t.Errorf("%s: assignments = %d, want 0 — an agent was woken", why, n)
	}
}

// seedForeignAgent creates a SECOND tenant with one agent in it. Written by
// hand rather than through seedTestUser/seedTestWorkspace because those pin a
// single id and email, so a test needing two tenants cannot call them twice.
func (f *mentionFixture) seedForeignAgent(t *testing.T, crewID, agentID string) (wsID string) {
	t.Helper()
	wsID = "ws-foreign-" + crewID
	execOrFatal(t, f.db, `INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other User')`,
		"user-foreign-"+crewID, "other-"+crewID+"@example.com")
	execOrFatal(t, f.db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', ?)`, wsID, "other-"+crewID)
	execOrFatal(t, f.db,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"mem-foreign-"+crewID, wsID, "user-foreign-"+crewID)
	execOrFatal(t, f.db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Other', ?)`, crewID, wsID, "other-"+crewID)
	execOrFatal(t, f.db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'Foreign', ?)`,
		agentID, crewID, wsID, "foreign-"+crewID)
	return wsID
}

func (f *mentionFixture) setLimit(t *testing.T, key string, v int) {
	t.Helper()
	execOrFatal(t, f.db,
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, fmt.Sprint(v))
}

// ── 1. the happy path, all four obligations ────────────────────────────────

func TestMentions_ResolvedMentionIsPersistedAuditedAndDispatched(t *testing.T) {
	f := setupMentionFixture(t)

	f.comment(t, "over to you "+mentionToken("lead", f.target)+" — the CSV job is failing")

	var agentID, state, assignmentID string
	var position int
	if err := f.db.QueryRow(`
		SELECT agent_id, dispatch_state, COALESCE(assignment_id,''), position
		  FROM mission_comment_mentions`).Scan(&agentID, &state, &assignmentID, &position); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if agentID != f.target {
		t.Errorf("agent_id = %q, want %q", agentID, f.target)
	}
	if state != mentionDispatchDispatched {
		t.Errorf("dispatch_state = %q, want %q", state, mentionDispatchDispatched)
	}
	if assignmentID == "" {
		t.Error("assignment_id empty — the mention was recorded but nobody was woken")
	}
	if position != 0 {
		t.Errorf("position = %d, want 0", position)
	}

	// The activity row carries the BARE agent id — the shape
	// lib/mentions.ts's mentionTargetFromActivityDetails reads.
	var actorType, actorID, details string
	if err := f.db.QueryRow(`
		SELECT actor_type, actor_id, details FROM mission_activity
		 WHERE mission_id = ? AND action = 'mentioned'`, f.missionID).
		Scan(&actorType, &actorID, &details); err != nil {
		t.Fatalf("mentioned activity row missing: %v", err)
	}
	if actorType != "user" || actorID != f.userID {
		t.Errorf("activity actor = %s/%s, want user/%s", actorType, actorID, f.userID)
	}
	if details != f.target {
		t.Errorf("activity details = %q, want the bare agent id %q", details, f.target)
	}

	// And the dispatch is a real assignment addressed to the mentioned agent,
	// carrying a server-derived depth.
	var assignedTo string
	var depth int
	if err := f.db.QueryRow(
		`SELECT assigned_to_id, depth FROM assignments WHERE id = ?`, assignmentID).
		Scan(&assignedTo, &depth); err != nil {
		t.Fatalf("assignment row missing: %v", err)
	}
	if assignedTo != f.target {
		t.Errorf("assignment assigned_to_id = %q, want %q", assignedTo, f.target)
	}
	if depth != 1 {
		t.Errorf("assignment depth = %d, want 1 — a human's mention is a root dispatch", depth)
	}
}

// ── 2. an id that resolves to nothing ──────────────────────────────────────

func TestMentions_UnresolvedIdLeavesNothingBehind(t *testing.T) {
	f := setupMentionFixture(t)
	f.comment(t, "hello "+mentionToken("ghost", "agentthatdoesnotexist"))
	f.nothingHappened(t, "unresolved id")
}

// ── 3. an id from another workspace ────────────────────────────────────────
//
// This is the test the workspace predicate in resolveMentionedAgents exists
// for. Drop that predicate and the foreign agent resolves, gets a row, gets an
// activity entry, and is handed a copy of this tenant's comment.

func TestMentions_ForeignWorkspaceAgentIsAProbeNotAMention(t *testing.T) {
	f := setupMentionFixture(t)

	f.seedForeignAgent(t, "crewX", "foreignAgent")

	f.comment(t, "psst "+mentionToken("foreign", "foreignAgent"))
	f.nothingHappened(t, "foreign-workspace id")
}

// ── 4. documentation does not fire ─────────────────────────────────────────
//
// The end-to-end version of internal/mentions' property 2. A comment quoting
// the syntax — which docs/guides/issue-mentions.mdx does, repeatedly — must
// produce no row, no activity and no run.

func TestMentions_InsideCodeDoesNotFire(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"fenced block", "the format is:\n\n```markdown\n" + mentionToken("lead", "PLACEHOLDER") + "\n```\n"},
		{"code span", "write `" + mentionToken("lead", "PLACEHOLDER") + "` to mention someone"},
		{"indented block", "example:\n\n    " + mentionToken("lead", "PLACEHOLDER") + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMentionFixture(t)
			// The id in the body is the REAL agent, so the only thing standing
			// between this comment and a dispatch is the code context.
			body := strings.ReplaceAll(tc.body, "PLACEHOLDER", f.target)
			f.comment(t, body)
			f.nothingHappened(t, tc.name)
		})
	}
}

// ── 5. de-duplication ──────────────────────────────────────────────────────

func TestMentions_SameAgentTwiceDispatchesOnce(t *testing.T) {
	f := setupMentionFixture(t)

	f.comment(t, mentionToken("lead", f.target)+" and again "+mentionToken("lead", f.target)+
		" and once more "+mentionToken("Lead Agent", f.target))

	if n := f.mentionRows(t); n != 1 {
		t.Errorf("mention rows = %d, want 1", n)
	}
	if n := f.mentionActivity(t); n != 1 {
		t.Errorf("mentioned activity rows = %d, want 1", n)
	}
	if n := f.assignments(t); n != 1 {
		t.Errorf("assignments = %d, want 1 — three tokens, one agent, one run", n)
	}
}

// ── 6. the caps bound a mention chain ──────────────────────────────────────
//
// An agent that is itself running a delegated task at the depth limit cannot
// mention its way to another hop. The number comes from app_settings and the
// position from the assignment row the author is executing — neither is in the
// comment, which is the whole point.
//
// The mutation is in the test: the identical comment DOES dispatch once the
// limit is raised by one, so a cap that refused unconditionally would fail
// here.

func TestMentions_DepthCapBoundsAMentionChain(t *testing.T) {
	f := setupMentionFixture(t)
	f.setLimit(t, SettingDelegationMaxDepth, 2)

	// The author is executing a depth-2 assignment: it is AT the limit.
	execOrFatal(t, f.db, `
		INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatM', ?, ?, 'CHAT', 'ACTIVE')`,
		f.target, f.wsID)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at)
		VALUES ('inflight', ?, 'chatM', ?, ?, 'chain', 'RUNNING', 2, datetime('now'))`,
		f.wsID, f.target, f.author)

	f.commentAsAgent(t, f.author, "handing over to "+mentionToken("lead", f.target))

	var state, detail string
	if err := f.db.QueryRow(
		`SELECT dispatch_state, COALESCE(dispatch_detail,'') FROM mission_comment_mentions`).
		Scan(&state, &detail); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state != mentionDispatchRefused {
		t.Fatalf("dispatch_state = %q, want %q — the depth cap did not bound the mention", state, mentionDispatchRefused)
	}
	if !strings.Contains(detail, SettingDelegationMaxDepth) {
		t.Errorf("refusal %q does not name %s — an agent cannot report what an operator would change",
			detail, SettingDelegationMaxDepth)
	}
	// The mention is still recorded and audited: "R was mentioned and the cap
	// said no" is the fact an operator needs.
	if n := f.mentionActivity(t); n != 1 {
		t.Errorf("mentioned activity rows = %d, want 1 — a refused dispatch is still a mention", n)
	}
	// One assignment: the in-flight one seeded above. No second.
	if n := f.assignments(t); n != 1 {
		t.Errorf("assignments = %d, want 1 (the seeded in-flight row only)", n)
	}

	// MUTATION: raise the limit by one and the identical comment dispatches.
	f.setLimit(t, SettingDelegationMaxDepth, 3)
	f.commentAsAgent(t, f.author, "trying again "+mentionToken("lead", f.target))

	if n := f.countRows(t,
		`SELECT COUNT(*) FROM mission_comment_mentions WHERE dispatch_state = ?`, mentionDispatchDispatched); n != 1 {
		t.Errorf("dispatched mentions after raising the limit = %d, want 1 — "+
			"the cap is refusing unconditionally rather than reading %s", n, SettingDelegationMaxDepth)
	}
	if n := f.assignments(t); n != 2 {
		t.Errorf("assignments = %d, want 2 (seeded + the newly permitted mention)", n)
	}
}

// ── 6b. fan-out ────────────────────────────────────────────────────────────
//
// The other axis, on the HUMAN door, where there is no parent assignment to
// count against. The fan-out is counted against the agent the row is filed
// under and scoped to the issue, so a person cannot mention one agent into an
// unbounded number of CONCURRENT runs on one issue.
//
// The seeded row is what makes this a real test: the cap counts only in-flight
// dispatches (delegation_limits.go's root branch, deliberately — a lead that
// retires after N lifetime tasks is a different and wrong product decision),
// so a mention whose run has already finished frees its slot. Seeding a RUNNING
// sibling is the state the cap actually exists for.

func TestMentions_FanoutCapBoundsConcurrentMentionsOfOneAgent(t *testing.T) {
	f := setupMentionFixture(t)
	f.setLimit(t, SettingDelegationMaxFanout, 1)

	// assignments.chat_id has an FK to chats, and an issue dispatch uses the
	// mission id as its pseudo-chat — the same synthetic row DispatchMention
	// creates.
	execOrFatal(t, f.db, `
		INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		f.missionID, f.target, f.wsID)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at)
		VALUES ('inflight-mention', ?, ?, ?, ?, 'earlier mention', 'RUNNING', 1, datetime('now'))`,
		f.wsID, f.missionID, f.target, f.target)

	f.comment(t, "while you are at it "+mentionToken("lead", f.target))

	var state, detail string
	if err := f.db.QueryRow(
		`SELECT dispatch_state, COALESCE(dispatch_detail,'') FROM mission_comment_mentions`).
		Scan(&state, &detail); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state != mentionDispatchRefused {
		t.Fatalf("dispatch_state = %q, want %q — the fan-out cap did not bound the mention", state, mentionDispatchRefused)
	}
	if !strings.Contains(detail, SettingDelegationMaxFanout) {
		t.Errorf("refusal %q does not name %s", detail, SettingDelegationMaxFanout)
	}
	if n := f.assignments(t); n != 1 {
		t.Errorf("assignments = %d, want 1 (the seeded in-flight row only)", n)
	}

	// MUTATION: raise the limit and the identical mention dispatches.
	f.setLimit(t, SettingDelegationMaxFanout, 2)
	f.comment(t, "trying again "+mentionToken("lead", f.target))
	if n := f.assignments(t); n != 2 {
		t.Errorf("assignments after raising %s = %d, want 2 — the cap is refusing unconditionally",
			SettingDelegationMaxFanout, n)
	}
}

// ── 7. both doors ──────────────────────────────────────────────────────────

func TestMentions_BothCommentPathsParse(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		f := setupMentionFixture(t)
		f.comment(t, "human says "+mentionToken("lead", f.target))
		if n := f.mentionRows(t); n != 1 {
			t.Fatalf("mention rows = %d, want 1", n)
		}
		var actorType string
		if err := f.db.QueryRow(
			`SELECT actor_type FROM mission_activity WHERE action = 'mentioned'`).Scan(&actorType); err != nil {
			t.Fatalf("activity row: %v", err)
		}
		if actorType != "user" {
			t.Errorf("actor_type = %q, want user", actorType)
		}
	})

	t.Run("agent", func(t *testing.T) {
		f := setupMentionFixture(t)
		f.commentAsAgent(t, f.author, "agent says "+mentionToken("lead", f.target))
		if n := f.mentionRows(t); n != 1 {
			t.Fatalf("mention rows = %d, want 1", n)
		}
		var actorType, actorID string
		if err := f.db.QueryRow(
			`SELECT actor_type, actor_id FROM mission_activity WHERE action = 'mentioned'`).
			Scan(&actorType, &actorID); err != nil {
			t.Fatalf("activity row: %v", err)
		}
		if actorType != "agent" || actorID != f.author {
			t.Errorf("activity actor = %s/%s, want agent/%s", actorType, actorID, f.author)
		}
		if n := f.assignments(t); n != 1 {
			t.Errorf("assignments = %d, want 1", n)
		}
	})

	// The third door: an agent's inline comment on PATCH /issues/{id}. Same
	// prose, same wire format — a mention there is a mention.
	t.Run("agent inline comment on PATCH", func(t *testing.T) {
		f := setupMentionFixture(t)
		req := httptest.NewRequest(http.MethodPatch, "/", jsonBody(map[string]any{
			"workspace_id": f.wsID,
			"agent_id":     f.author,
			"status":       "IN_PROGRESS",
			"comment":      "picking this up, " + mentionToken("lead", f.target) + " please review",
		}))
		req.SetPathValue("identifier", f.ident)
		rr := httptest.NewRecorder()
		f.internal.UpdateStatus(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("UpdateStatus status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		f.assign.WaitDispatches()

		if n := f.mentionRows(t); n != 1 {
			t.Errorf("mention rows = %d, want 1 — the PATCH inline comment did not parse", n)
		}
		if n := f.assignments(t); n != 1 {
			t.Errorf("assignments = %d, want 1", n)
		}
	})
}

// ── 8. an agent does not mention itself awake ──────────────────────────────

func TestMentions_SelfMentionIsRecordedButNotDispatched(t *testing.T) {
	f := setupMentionFixture(t)
	f.commentAsAgent(t, f.author, "note to self "+mentionToken("me", f.author))

	var state string
	if err := f.db.QueryRow(`SELECT dispatch_state FROM mission_comment_mentions`).Scan(&state); err != nil {
		t.Fatalf("mention row missing: %v", err)
	}
	if state != mentionDispatchSkipped {
		t.Errorf("dispatch_state = %q, want %q", state, mentionDispatchSkipped)
	}
	if n := f.assignments(t); n != 0 {
		t.Errorf("assignments = %d, want 0 — an agent must not wake itself in a loop", n)
	}
}

// ── 9. a comment with no mention costs nothing ─────────────────────────────

func TestMentions_PlainCommentWritesNothing(t *testing.T) {
	f := setupMentionFixture(t)
	f.comment(t, "just a normal comment about pavel@unify.cz and @notatoken")
	f.nothingHappened(t, "plain comment")
}

// ── 10. the resolve is workspace-scoped at the DB level ────────────────────
//
// A unit-level companion to test 3, so a regression points at the function
// rather than at the whole handler.

func TestMentions_ResolveIsScopedToOneWorkspace(t *testing.T) {
	f := setupMentionFixture(t)
	f.seedForeignAgent(t, "crewY", "farAgent")

	m := mentionRecorder{db: f.db, logger: newTestLogger()}
	got, err := m.resolveMentionedAgents(context.Background(), f.wsID, []string{"farAgent", f.target, "nope"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].AgentID != f.target {
		t.Fatalf("resolved = %+v, want exactly the in-workspace agent %q", got, f.target)
	}
}
