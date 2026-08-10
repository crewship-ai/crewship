package chain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// ---------------------------------------------------------------------------
// Fixtures.
//
// These insert into the real tables rather than through a helper the
// production path does not use. The whole point of this package is that it
// reads columns nothing else joins on (mission_tasks.assignment_id,
// pipeline_runs.triggered_by_id holding an issue IDENTIFIER, inbox_items
// .payload_json->run_id), so a fixture that wrote through a convenience layer
// would be asserting the convenience layer, not the schema.
// ---------------------------------------------------------------------------

type rig struct {
	db   *sql.DB
	ws   string
	crew string
	// agent and chat exist because assignments.chat_id / assigned_to_id are
	// NOT NULL FKs; missions.crew_id / lead_agent_id likewise.
	agent string
	chat  string
}

func newRig(t *testing.T, wsID string) *rig {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	r := &rig{db: db, ws: wsID}
	r.seedWorkspace(t, wsID, wsID+"-slug")
	r.crew = r.seedCrew(t, wsID+"-crew", wsID)
	r.agent = r.seedAgent(t, wsID+"-agent", wsID, r.crew, "Ada")
	r.chat = r.seedChat(t, wsID+"-chat", wsID, r.agent)
	return r
}

func (r *rig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", strings.SplitN(strings.TrimSpace(q), "\n", 2)[0], err)
	}
}

func (r *rig) seedWorkspace(t *testing.T, id, slug string) {
	t.Helper()
	r.exec(t, `INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, id, id, slug)
}

func (r *rig) seedCrew(t *testing.T, id, wsID string) string {
	t.Helper()
	r.exec(t, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew', ?)`, id, wsID, id)
	return id
}

func (r *rig) seedAgent(t *testing.T, id, wsID, crewID, name string) string {
	t.Helper()
	r.exec(t, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, ?, ?)`,
		id, wsID, crewID, name, id)
	return id
}

func (r *rig) seedChat(t *testing.T, id, wsID, agentID string) string {
	t.Helper()
	r.exec(t, `INSERT INTO chats (id, workspace_id, agent_id) VALUES (?, ?, ?)`, id, wsID, agentID)
	return id
}

// seedIssue writes a missions row with an identifier — an "issue" in product
// terms. trace_id is globally UNIQUE, hence the id-derived value.
func (r *rig) seedIssue(t *testing.T, id, wsID, identifier, title string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier)
		VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', ?)`,
		id, wsID, r.crew, r.agent, "trace-"+id, title, identifier)
	return id
}

func (r *rig) seedRoutine(t *testing.T, id, wsID, slug string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES (?, ?, ?, ?, '{}', 'h')`, id, wsID, slug, slug)
	return id
}

func (r *rig) seedRun(t *testing.T, id, wsID, pipelineID, slug, via, byID string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via, triggered_by_id)
		VALUES (?, ?, ?, ?, 'completed', ?, ?, ?)`,
		id, wsID, pipelineID, slug, time.Now().UTC().Format(time.RFC3339Nano), via, nullable(byID))
	return id
}

// seedAutomation writes an automations row targeting routineSlug. It inserts
// the raw JSON rather than going through automation.Store so the fixture keeps
// asserting the SCHEMA (action_config_json's shape is the contract the walk
// reads) rather than the store that happens to write it today.
func (r *rig) seedAutomation(t *testing.T, id, wsID, name, eventType, routineSlug string, enabled bool) string {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	en := 0
	if enabled {
		en = 1
	}
	r.exec(t, `
		INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
			action_kind, action_config_json, debounce_seconds, max_per_hour, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '{}', 'routine', ?, 10, 60, ?, ?)`,
		id, wsID, name, en, eventType, fmt.Sprintf(`{"routine_slug":%q}`, routineSlug), now, now)
	return id
}

// softDeleteAutomation stamps deleted_at the way automation.Store.Delete does.
// Written as raw SQL for the same reason seedAutomation is: the walk reads the
// schema, so the fixture asserts against the schema.
func (r *rig) softDeleteAutomation(t *testing.T, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.exec(t, `UPDATE automations SET deleted_at = ?, updated_at = ? WHERE id = ?`, now, now, id)
}

func (r *rig) seedAssignment(t *testing.T, id, wsID, task, parentID string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, parent_assignment_id)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		id, wsID, r.chat, r.agent, r.agent, task, nullable(parentID))
	return id
}

func (r *rig) seedMissionTask(t *testing.T, id, missionID, assignmentID string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO mission_tasks (id, mission_id, title, assignment_id)
		VALUES (?, ?, 'task', ?)`, id, missionID, nullable(assignmentID))
}

func (r *rig) seedInbox(t *testing.T, id, wsID, kind, sourceID, title, payload string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)`, id, wsID, kind, sourceID, title, payload)
	return id
}

func (r *rig) seedWaitpoint(t *testing.T, token, wsID, runID string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, timeout_at)
		VALUES (?, ?, ?, 'step-1', 'approval', ?)`,
		token, wsID, runID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// Assertion helpers.
// ---------------------------------------------------------------------------

func walk(t *testing.T, r *rig, anchor string, opt Options) *Graph {
	t.Helper()
	g, err := Walk(context.Background(), r.db, r.ws, anchor, opt)
	if err != nil {
		t.Fatalf("Walk(%q): %v", anchor, err)
	}
	return g
}

func hasNode(g *Graph, id string) bool {
	for _, n := range g.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func hasEdge(g *Graph, from, to string, kind EdgeKind) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

func nodeByID(t *testing.T, g *Graph, id string) Node {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not in graph; nodes = %v", id, nodeIDs(g))
	return Node{}
}

func nodeIDs(g *Graph) []string {
	out := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// ---------------------------------------------------------------------------
// Required behaviours.
// ---------------------------------------------------------------------------

// A chain of one is a valid answer. Returning 404 or an error for an issue
// that simply has not been worked yet would make the endpoint unusable as the
// UI's default view — the caller would have to guess whether the anchor was
// wrong or the chain was empty.
func TestWalk_AnchorWithNoChain_ReturnsOneNodeAndNoEdges(t *testing.T) {
	r := newRig(t, "ws-lonely")
	r.seedIssue(t, "m1", r.ws, "ENG-1", "Nothing has happened yet")

	g := walk(t, r, "ENG-1", Options{})

	if len(g.Nodes) != 1 {
		t.Fatalf("nodes = %v, want exactly the anchor", nodeIDs(g))
	}
	if len(g.Edges) != 0 {
		t.Errorf("edges = %#v, want none", g.Edges)
	}
	if !g.Nodes[0].Anchor {
		t.Error("the single node is not flagged as the anchor")
	}
	if g.Nodes[0].Depth != 0 {
		t.Errorf("anchor depth = %d, want 0", g.Nodes[0].Depth)
	}
	if g.Truncated {
		t.Error("truncated = true on a chain that fits entirely in the response")
	}
	if g.AnchorNode != "issue:m1" {
		t.Errorf("anchor_node = %q, want issue:m1", g.AnchorNode)
	}
}

// A cycle in the DATA must not become a cycle in the WALK. assignments
// .parent_assignment_id has no cycle constraint, and delegation is the path
// that writes it, so a → b → a is reachable by ordinary use rather than by
// corruption.
func TestWalk_CycleInDelegation_Terminates(t *testing.T) {
	r := newRig(t, "ws-cycle")
	r.seedAssignment(t, "a1", r.ws, "one", "")
	r.seedAssignment(t, "a2", r.ws, "two", "a1")
	// Close the loop: a1's parent is a2, a2's parent is a1.
	r.exec(t, `UPDATE assignments SET parent_assignment_id = 'a2' WHERE id = 'a1'`)

	done := make(chan *Graph, 1)
	go func() {
		g, err := Walk(context.Background(), r.db, r.ws, "a1", Options{})
		if err != nil {
			t.Errorf("Walk: %v", err)
			done <- nil
			return
		}
		done <- g
	}()

	select {
	case g := <-done:
		if g == nil {
			t.Fatal("walk failed")
		}
		// Both assignments plus the agent executing them; each exactly once.
		if !hasNode(g, "assignment:a1") || !hasNode(g, "assignment:a2") {
			t.Fatalf("nodes = %v, want both assignments", nodeIDs(g))
		}
		seen := map[string]int{}
		for _, n := range g.Nodes {
			seen[n.ID]++
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("node %s materialised %d times, want 1", id, n)
			}
		}
		// The cycle itself must still be visible as edges — dropping them
		// would hide the loop that a reader is most likely hunting.
		if !hasEdge(g, "assignment:a1", "assignment:a2", EdgeTriggers) {
			t.Error("missing a1 -> a2 edge")
		}
		if !hasEdge(g, "assignment:a2", "assignment:a1", EdgeTriggers) {
			t.Error("missing the back-edge a2 -> a1 that closes the cycle")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Walk did not terminate on cyclic delegation data")
	}
}

// A cycle that spans two tables (issue -> assignment -> issue) is the shape a
// single-table visited-set would still loop on.
func TestWalk_CycleAcrossTables_Terminates(t *testing.T) {
	r := newRig(t, "ws-cycle2")
	r.seedIssue(t, "m1", r.ws, "ENG-1", "issue")
	r.seedAssignment(t, "a1", r.ws, "work", "")
	r.seedMissionTask(t, "mt1", "m1", "a1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		g, err := Walk(context.Background(), r.db, r.ws, "ENG-1", Options{})
		if err != nil {
			t.Errorf("Walk: %v", err)
			return
		}
		if !hasEdge(g, "issue:m1", "assignment:a1", EdgeTriggers) {
			t.Errorf("edges = %#v, want issue:m1 -> assignment:a1", g.Edges)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Walk did not terminate on issue<->assignment reciprocal links")
	}
}

// The walk hops between tables on untyped string columns, so a value that
// collides across tenants is the realistic leak: two workspaces both have an
// issue "ENG-1", because identifiers are only unique per workspace.
func TestWalk_NeverCrossesWorkspaceBoundary(t *testing.T) {
	r := newRig(t, "ws-a")
	// A second, fully populated workspace sharing every joinable value.
	r.seedWorkspace(t, "ws-b", "ws-b-slug")
	otherCrew := r.seedCrew(t, "ws-b-crew", "ws-b")
	otherAgent := r.seedAgent(t, "ws-b-agent", "ws-b", otherCrew, "Grace")
	r.exec(t, `INSERT INTO chats (id, workspace_id, agent_id) VALUES ('ws-b-chat', 'ws-b', ?)`, otherAgent)

	// Same identifier in both workspaces.
	r.seedIssue(t, "m-a", r.ws, "ENG-1", "ours")
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier)
		VALUES ('m-b', 'ws-b', ?, ?, 'trace-m-b', 'theirs', 'PLANNING', 'ENG-1')`, otherCrew, otherAgent)

	// A routine + run in the OTHER workspace, triggered by that same "ENG-1".
	r.seedRoutine(t, "p-b", "ws-b", "nightly")
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via, triggered_by_id)
		VALUES ('run-b', 'ws-b', 'p-b', 'nightly', 'completed', ?, 'issue', 'ENG-1')`,
		time.Now().UTC().Format(time.RFC3339Nano))

	// And the issue in OUR workspace bound to the OTHER workspace's routine —
	// missions.routine_id has no FK, so this row is writable.
	r.exec(t, `UPDATE missions SET routine_id = 'p-b' WHERE id = 'm-a'`)

	g := walk(t, r, "ENG-1", Options{})

	if g.AnchorNode != "issue:m-a" {
		t.Fatalf("anchor resolved to %q, want the ws-a issue", g.AnchorNode)
	}
	for _, n := range g.Nodes {
		switch n.Ref {
		case "m-b", "run-b", "p-b", "ws-b-agent":
			t.Errorf("node %s (%s) belongs to ws-b and must not appear in a ws-a chain", n.ID, n.Label)
		}
	}
	if len(g.Nodes) != 1 {
		t.Errorf("nodes = %v, want only the ws-a issue", nodeIDs(g))
	}
}

// A run id is likewise only meaningful inside its workspace.
func TestWalk_ForeignAnchorIsNotFound(t *testing.T) {
	r := newRig(t, "ws-a2")
	r.seedWorkspace(t, "ws-b2", "ws-b2-slug")
	otherCrew := r.seedCrew(t, "ws-b2-crew", "ws-b2")
	r.seedRoutine(t, "p-b", "ws-b2", "nightly")
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at)
		VALUES ('run-b', 'ws-b2', 'p-b', 'nightly', 'completed', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano))
	_ = otherCrew

	_, err := Walk(context.Background(), r.db, r.ws, "run-b", Options{})
	if !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("err = %v, want ErrAnchorNotFound (a foreign run must be indistinguishable from a missing one)", err)
	}
}

// Silent truncation reads as "this is the whole chain", which is the one
// answer this endpoint must never give by accident.
func TestWalk_NodeCapSetsTruncated(t *testing.T) {
	r := newRig(t, "ws-nodecap")
	r.seedRoutine(t, "p1", r.ws, "fanout")
	for i := 0; i < 12; i++ {
		r.seedRun(t, fmt.Sprintf("run-%02d", i), r.ws, "p1", "fanout", "manual", "")
	}

	g := walk(t, r, "p1", Options{MaxNodes: 5})

	if !g.Truncated {
		t.Fatal("truncated = false with 13 reachable nodes and a cap of 5")
	}
	if g.TruncatedBy != "nodes" {
		t.Errorf("truncated_by = %q, want \"nodes\"", g.TruncatedBy)
	}
	if len(g.Nodes) > 5 {
		t.Errorf("len(nodes) = %d, want <= 5", len(g.Nodes))
	}
	// Every edge must land on a node that is actually in the response.
	present := map[string]bool{}
	for _, n := range g.Nodes {
		present[n.ID] = true
	}
	for _, e := range g.Edges {
		if !present[e.From] || !present[e.To] {
			t.Errorf("edge %s -%s-> %s dangles: an endpoint was dropped by the cap but the edge was kept", e.From, e.Kind, e.To)
		}
	}
}

func TestWalk_DepthCapSetsTruncated(t *testing.T) {
	r := newRig(t, "ws-depthcap")
	// A delegation ladder: a0 -> a1 -> a2 -> a3.
	parent := ""
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("a%d", i)
		r.seedAssignment(t, id, r.ws, "step "+id, parent)
		parent = id
	}

	g := walk(t, r, "a0", Options{MaxDepth: 1, MaxNodes: 100})

	if !g.Truncated {
		t.Fatal("truncated = false with a 4-deep ladder and a depth cap of 1")
	}
	if g.TruncatedBy != "depth" {
		t.Errorf("truncated_by = %q, want \"depth\"", g.TruncatedBy)
	}
	if hasNode(g, "assignment:a2") {
		t.Errorf("nodes = %v, want nothing beyond depth 1", nodeIDs(g))
	}
	for _, n := range g.Nodes {
		if n.Depth > 1 {
			t.Errorf("node %s at depth %d exceeds max_depth 1", n.ID, n.Depth)
		}
	}
}

// The same ladder, uncapped, must NOT report truncation — otherwise the flag
// is decoration rather than information.
func TestWalk_CompleteChainIsNotFlaggedTruncated(t *testing.T) {
	r := newRig(t, "ws-complete")
	parent := ""
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("a%d", i)
		r.seedAssignment(t, id, r.ws, "step "+id, parent)
		parent = id
	}

	g := walk(t, r, "a0", Options{MaxDepth: 10, MaxNodes: 100})

	if g.Truncated {
		t.Fatalf("truncated = true (%s) on a chain that fits: nodes = %v", g.TruncatedBy, nodeIDs(g))
	}
	if !hasNode(g, "assignment:a3") {
		t.Errorf("nodes = %v, want the whole ladder", nodeIDs(g))
	}
}

// ---------------------------------------------------------------------------
// The links themselves.
// ---------------------------------------------------------------------------

// The end-to-end shape the endpoint exists for: an issue bound to a routine,
// a run fired from that issue, a nested run that run called, and the inbox
// item the failure produced.
func TestWalk_IssueToRoutineToRunToNestedRunToInbox(t *testing.T) {
	r := newRig(t, "ws-full")
	r.seedIssue(t, "m1", r.ws, "ENG-7", "Ship the thing")
	r.seedRoutine(t, "p1", r.ws, "deploy")
	r.exec(t, `UPDATE missions SET routine_id = 'p1' WHERE id = 'm1'`)
	r.seedRun(t, "run-parent", r.ws, "p1", "deploy", "issue", "ENG-7")
	r.seedRun(t, "run-child", r.ws, "p1", "deploy", "call_pipeline", "run-parent")
	r.seedInbox(t, "ibx-1", r.ws, "failed_run", "run-child", "deploy failed", `{"run_id":"run-child"}`)
	r.seedWaitpoint(t, "wp-token", r.ws, "run-parent")
	r.seedInbox(t, "ibx-2", r.ws, "waitpoint", "wp-token", "approve deploy?", `{}`)

	g := walk(t, r, "ENG-7", Options{MaxDepth: 6, MaxNodes: 100})

	for _, want := range []struct {
		from, to string
		kind     EdgeKind
	}{
		{"issue:m1", "routine:p1", EdgeTriggers},
		{"issue:m1", "run:run-parent", EdgeTriggers},
		{"routine:p1", "run:run-parent", EdgeRuns},
		{"run:run-parent", "run:run-child", EdgeTriggers},
		{"run:run-child", "inbox:ibx-1", EdgeProduces},
		{"run:run-parent", "inbox:ibx-2", EdgeProduces},
	} {
		if !hasEdge(g, want.from, want.to, want.kind) {
			t.Errorf("missing edge %s -%s-> %s\nedges: %#v", want.from, want.kind, want.to, g.Edges)
		}
	}
	if g.Truncated {
		t.Errorf("truncated = true (%s) on a 6-node chain with a cap of 100", g.TruncatedBy)
	}
}

// mission_tasks.assignment_id is the only column joining the issue substrate
// to the delegation substrate, and assignments.parent_assignment_id is the
// only column carrying delegation depth.
func TestWalk_IssueToAssignmentToDelegateToAgent(t *testing.T) {
	r := newRig(t, "ws-deleg")
	r.seedIssue(t, "m1", r.ws, "ENG-2", "Investigate")
	r.seedAssignment(t, "a-lead", r.ws, "lead work", "")
	r.seedAssignment(t, "a-sub", r.ws, "delegated work", "a-lead")
	r.seedMissionTask(t, "mt1", "m1", "a-lead")

	g := walk(t, r, "ENG-2", Options{MaxDepth: 6, MaxNodes: 100})

	if !hasEdge(g, "issue:m1", "assignment:a-lead", EdgeTriggers) {
		t.Errorf("missing issue -> assignment edge (mission_tasks.assignment_id); edges: %#v", g.Edges)
	}
	if !hasEdge(g, "assignment:a-lead", "assignment:a-sub", EdgeTriggers) {
		t.Errorf("missing delegation edge (assignments.parent_assignment_id); edges: %#v", g.Edges)
	}
	if !hasEdge(g, "agent:"+r.agent, "assignment:a-lead", EdgeExecutes) {
		t.Errorf("missing agent -> assignment edge (assignments.assigned_to_id); edges: %#v", g.Edges)
	}
}

// triggered_by_id is polymorphic. A schedule id that happens to equal a run id
// must not be dereferenced as a run.
func TestWalk_PolymorphicTriggeredByIsNotFollowedBlind(t *testing.T) {
	r := newRig(t, "ws-poly")
	r.seedRoutine(t, "p1", r.ws, "nightly")
	r.seedRun(t, "run-1", r.ws, "p1", "nightly", "manual", "")
	// A schedule-triggered run whose triggered_by_id collides with run-1's id.
	r.seedRun(t, "run-2", r.ws, "p1", "nightly", "schedule", "run-1")

	g := walk(t, r, "run-2", Options{MaxDepth: 3, MaxNodes: 100})

	if hasEdge(g, "run:run-1", "run:run-2", EdgeTriggers) {
		t.Error("followed triggered_by_id as a parent run although triggered_via is 'schedule'")
	}
}

// Who actually executed a run is only recorded in the journal — pipeline_runs
// .invoking_agent_id names who STARTED it, not who ran its steps. The
// correlation has two arms because pipeline runs tag payload.run_id while
// agent-driven runs use trace_id; both must be walked.
func TestWalk_RunToAgentViaJournal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		traceID string
		payload string
	}{
		{"payload.run_id arm", "some-other-trace", `{"run_id":"run-1"}`},
		{"trace_id arm", "run-1", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := "ws-journal-" + strings.ReplaceAll(tc.name, " ", "-")
			r := newRig(t, ws)
			r.seedRoutine(t, "p1", r.ws, "nightly")
			r.seedRun(t, "run-1", r.ws, "p1", "nightly", "manual", "")
			r.exec(t, `
				INSERT INTO journal_entries (id, workspace_id, entry_type, actor_type, actor_id, summary, trace_id, payload)
				VALUES ('j1', ?, 'agent.step', 'agent', ?, 'did a thing', ?, ?)`,
				r.ws, r.agent, tc.traceID, tc.payload)

			g := walk(t, r, "run-1", Options{MaxDepth: 3, MaxNodes: 100})

			if !hasEdge(g, "agent:"+r.agent, "run:run-1", EdgeExecutes) {
				t.Errorf("missing agent -> run edge; edges: %#v", g.Edges)
			}
		})
	}
}

// A journal entry belonging to another workspace must not attach that
// workspace's agent to this run, even when the run id collides.
func TestWalk_JournalCorrelationIsWorkspaceScoped(t *testing.T) {
	r := newRig(t, "ws-journal-fence")
	r.seedWorkspace(t, "ws-other", "ws-other-slug")
	otherCrew := r.seedCrew(t, "ws-other-crew", "ws-other")
	otherAgent := r.seedAgent(t, "ws-other-agent", "ws-other", otherCrew, "Grace")
	r.seedRoutine(t, "p1", r.ws, "nightly")
	r.seedRun(t, "run-1", r.ws, "p1", "nightly", "manual", "")
	r.exec(t, `
		INSERT INTO journal_entries (id, workspace_id, entry_type, actor_type, actor_id, summary, trace_id, payload)
		VALUES ('j1', 'ws-other', 'agent.step', 'agent', ?, 'theirs', 'run-1', '{"run_id":"run-1"}')`,
		otherAgent)

	g := walk(t, r, "run-1", Options{MaxDepth: 3, MaxNodes: 100})

	if hasNode(g, "agent:"+otherAgent) {
		t.Errorf("a journal entry from another workspace attached its agent to this run; nodes = %v", nodeIDs(g))
	}
}

// An anchor with no chain and an anchor that does not exist must be different
// answers.
func TestWalk_UnknownAnchor(t *testing.T) {
	r := newRig(t, "ws-unknown")
	_, err := Walk(context.Background(), r.db, r.ws, "NOPE-1", Options{})
	if !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("err = %v, want ErrAnchorNotFound", err)
	}
}

func TestWalk_RoutineAnchorBySlugAndByID(t *testing.T) {
	r := newRig(t, "ws-slug")
	r.seedRoutine(t, "p1", r.ws, "nightly")
	r.seedRun(t, "run-1", r.ws, "p1", "nightly", "manual", "")

	for _, anchor := range []string{"p1", "nightly"} {
		g := walk(t, r, anchor, Options{})
		if g.AnchorNode != "routine:p1" {
			t.Errorf("anchor %q resolved to %q, want routine:p1", anchor, g.AnchorNode)
		}
		if !hasEdge(g, "routine:p1", "run:run-1", EdgeRuns) {
			t.Errorf("anchor %q: missing routine -> run edge", anchor)
		}
	}
}

// ---------------------------------------------------------------------------
// Declared gaps.
// ---------------------------------------------------------------------------

// The two links that do not exist must be reported, not guessed. If someone
// later adds the column, this test is where the claim gets retired.
func TestWalk_DeclaresTheLinksTheSchemaDoesNotCarry(t *testing.T) {
	r := newRig(t, "ws-gaps")
	r.seedIssue(t, "m1", r.ws, "ENG-9", "issue")
	// An inbox item raised while this issue was worked. Nothing connects it.
	r.seedInbox(t, "ibx-1", r.ws, "escalation", "esc-1", "needs a human", `{}`)

	g := walk(t, r, "ENG-9", Options{MaxDepth: 6, MaxNodes: 100})

	if len(g.Gaps) == 0 {
		t.Fatal("gaps is empty: the response claims every link is walkable")
	}
	var sawInboxIssue, sawEscalationRun bool
	for _, gap := range g.Gaps {
		if gap.From == "inbox" && gap.To == "issue" {
			sawInboxIssue = true
		}
		if gap.From == "escalation" && gap.To == "run" {
			sawEscalationRun = true
		}
		if gap.Reason == "" {
			t.Errorf("gap %s->%s has no reason", gap.From, gap.To)
		}
	}
	if !sawInboxIssue {
		t.Error("no gap declared for inbox -> issue (inbox_items has no mission column)")
	}
	if !sawEscalationRun {
		t.Error("no gap declared for escalation -> run (escalations has no run column)")
	}

	// The issue node itself must admit its blind spot rather than looking
	// complete.
	if !g.Nodes[0].Partial || g.Nodes[0].PartialReason == "" {
		t.Errorf("issue node = %+v, want partial with a stated reason", g.Nodes[0])
	}
	// And the unreachable inbox item must genuinely be absent, not invented
	// in by a guessed join.
	if hasNode(g, "inbox:ibx-1") {
		t.Error("an inbox item with no pointer to this issue was attached to it anyway")
	}
}

func TestWalk_EscalationInboxAnchorIsAPartialLeaf(t *testing.T) {
	r := newRig(t, "ws-esc")
	r.seedInbox(t, "ibx-1", r.ws, "escalation", "esc-1", "needs a human", `{}`)

	g := walk(t, r, "ibx-1", Options{})

	if len(g.Nodes) != 1 {
		t.Fatalf("nodes = %v, want just the inbox item", nodeIDs(g))
	}
	if !g.Nodes[0].Partial {
		t.Error("an escalation inbox item is a dead end and must say so")
	}
	if !strings.Contains(g.Nodes[0].PartialReason, "escalations") {
		t.Errorf("partial_reason = %q, want it to name the table that lacks the column", g.Nodes[0].PartialReason)
	}
}

// ---------------------------------------------------------------------------
// Automations — the rule that started a chain.
// ---------------------------------------------------------------------------

// A rule must be an anchor in its own right: "what does this automation do,
// and what has it actually done" is the question an author asks about a rule
// they just wrote.
func TestWalk_AutomationAnchorWalksToItsRoutineAndTheRunsItCaused(t *testing.T) {
	r := newRig(t, "ws-aut-anchor")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")

	g := walk(t, r, "aut_1", Options{MaxDepth: 4, MaxNodes: 100})

	if g.AnchorNode != "automation:aut_1" {
		t.Fatalf("anchor_node = %q, want automation:aut_1", g.AnchorNode)
	}
	if !hasEdge(g, "automation:aut_1", "routine:p1", EdgeTriggers) {
		t.Errorf("missing automation -> routine edge (action_config_json.routine_slug); edges: %#v", g.Edges)
	}
	if !hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Errorf("missing automation -> run edge (triggered_via='automation'); edges: %#v", g.Edges)
	}
}

// The precise, indexed link back: a run stamped triggered_via='automation'
// names the automations.id that caused it. Without this the topology can draw
// "routine -> run -> agent" and never the rule that started it, which is the
// origin the reader actually wants.
func TestWalk_RunWalksBackToTheRuleThatFiredIt(t *testing.T) {
	r := newRig(t, "ws-aut-back")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasNode(g, "automation:aut_1") {
		t.Fatalf("the rule that fired this run is not in the graph; nodes = %v", nodeIDs(g))
	}
	if !hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Errorf("missing automation -> run edge; edges: %#v", g.Edges)
	}
}

// Deleting a rule must not make the past unexplainable.
//
// automations.Delete is a SOFT delete, and pipeline_runs keeps
// triggered_via='automation' forever — so after a rule is deleted the run row
// still says "a rule started me" while the rule itself stops resolving. That
// combination is the worst of both: the fact is on the record, the name is
// hidden, and the topology draws a run with no origin. A reader then concludes
// "nobody started this", which is precisely the inference the gaps machinery
// exists to prevent.
//
// The walk resolves a deleted rule and marks it. Its history is what a chain
// is FOR.
func TestWalk_ADeletedRuleStillExplainsTheRunsItCaused(t *testing.T) {
	r := newRig(t, "ws-aut-tombstone")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")
	r.softDeleteAutomation(t, "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasNode(g, "automation:aut_1") {
		t.Fatalf("a deleted rule dropped out of the chain, leaving the run with no origin; nodes = %v", nodeIDs(g))
	}
	if !hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Errorf("missing automation -> run edge after delete; edges: %#v", g.Edges)
	}
	n := nodeByID(t, g, "automation:aut_1")
	if n.Label != "Triage on failure" {
		t.Errorf("label = %q, want the rule name it had when it fired", n.Label)
	}
}

// "deleted" and "disabled" are not the same fact and must not share a spelling.
// A disabled rule is one toggle from firing again; a deleted one will never
// fire again and cannot be edited. The client renders from this string, so
// collapsing them would offer a reader a control that does not exist.
func TestWalk_ADeletedRuleIsNotSpelledTheSameAsADisabledOne(t *testing.T) {
	r := newRig(t, "ws-aut-tombstone-spelling")
	r.seedRoutine(t, "p1", r.ws, "triage")
	// Seeded DISABLED, then deleted: if the walk reported enabled-state alone,
	// this row would be indistinguishable from a rule someone merely paused.
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", false)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")
	r.softDeleteAutomation(t, "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "automation:aut_1")

	if n.Status != "deleted" {
		t.Errorf("status = %q, want %q — a deleted rule read as merely disabled invites a reader to re-enable something that is gone", n.Status, "deleted")
	}
}

// A deleted rule explains history; it must not still be presented as live
// wiring. Anchoring on it answers "what did this rule do", not "what will it
// do", and the status is what carries that distinction.
func TestWalk_ADeletedRuleIsStillAnchorableAsHistory(t *testing.T) {
	r := newRig(t, "ws-aut-tombstone-anchor")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")
	r.softDeleteAutomation(t, "aut_1")

	g := walk(t, r, "aut_1", Options{MaxDepth: 4, MaxNodes: 100})

	if g.AnchorNode != "automation:aut_1" {
		t.Fatalf("anchor_node = %q, want automation:aut_1", g.AnchorNode)
	}
	if !hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Errorf("a deleted rule must still show the runs it caused; edges: %#v", g.Edges)
	}
	if n := nodeByID(t, g, "automation:aut_1"); n.Status != "deleted" {
		t.Errorf("status = %q, want %q", n.Status, "deleted")
	}
}

// The card the frontend draws needs a name, the event that arms the rule, and
// whether it is live. `enabled` is derived from status !== "disabled" on the
// client, so the two spellings are a contract, not cosmetics.
func TestWalk_AutomationNodeCarriesNameEventTypeAndEnabledState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		wantStatus string
	}{
		{"enabled rule", true, "enabled"},
		{"disabled rule", false, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := "ws-aut-card-" + tc.wantStatus
			r := newRig(t, ws)
			r.seedRoutine(t, "p1", r.ws, "triage")
			r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed", "triage", tc.enabled)

			g := walk(t, r, "aut_1", Options{})
			n := nodeByID(t, g, "automation:aut_1")

			if n.Kind != KindAutomation {
				t.Errorf("kind = %q, want %q", n.Kind, KindAutomation)
			}
			if n.Label != "Triage on failure" {
				t.Errorf("label = %q, want the rule name", n.Label)
			}
			if n.Key != "run.failed" {
				t.Errorf("key = %q, want the event_type", n.Key)
			}
			if n.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", n.Status, tc.wantStatus)
			}
		})
	}
}

// A disabled rule that already fired still explains the runs it caused. Only
// enabled rules FIRE; every rule that has fired must remain readable, or the
// act of switching a rule off would retroactively erase the origin of the runs
// it started.
func TestWalk_DisabledRuleStillExplainsTheRunsItAlreadyCaused(t *testing.T) {
	r := newRig(t, "ws-aut-disabled")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Retired rule", "run.failed", "triage", false)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Errorf("disabling a rule erased the origin of a run it had already caused; edges: %#v", g.Edges)
	}
}

// THE JUDGEMENT CALL, half one.
//
// A rule that has never fired still points at a routine. It must NOT show up
// when walking that routine. A graph titled "how this happened" that offers a
// rule which did not fire is not an omission, it is a false cause — and the
// reader has no way to tell it from the real one, because the real one is
// drawn with the identical edge.
func TestWalk_RoutineDoesNotSurfaceRulesThatNeverFired(t *testing.T) {
	r := newRig(t, "ws-aut-never")
	r.seedRoutine(t, "p1", r.ws, "triage")
	// Three rules aimed at this routine. None of them has ever fired.
	r.seedAutomation(t, "aut_1", r.ws, "Rule one", "run.failed", "triage", true)
	r.seedAutomation(t, "aut_2", r.ws, "Rule two", "issue.created", "triage", true)
	r.seedAutomation(t, "aut_3", r.ws, "Rule three", "run.completed", "triage", false)
	// The run that DID happen was started by hand.
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "manual", "")

	g := walk(t, r, "p1", Options{MaxDepth: 4, MaxNodes: 100})

	for _, id := range []string{"automation:aut_1", "automation:aut_2", "automation:aut_3"} {
		if hasNode(g, id) {
			t.Errorf("%s never fired but was offered as a cause; nodes = %v", id, nodeIDs(g))
		}
	}
	// And walking the manual run must not acquire a rule either.
	g2 := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})
	for _, n := range g2.Nodes {
		if n.Kind == KindAutomation {
			t.Errorf("a hand-started run was attributed to rule %s", n.ID)
		}
	}
}

// THE JUDGEMENT CALL, half two.
//
// The rules that DID fire stay reachable from the routine — through the runs
// they caused, which is the evidence that they fired. No separate query buys
// this; it falls out of run -> automation. The asymmetry is the point: a rule
// earns a place in the graph by having acted.
func TestWalk_RoutineReachesTheRuleThatDidFireThroughItsRun(t *testing.T) {
	r := newRig(t, "ws-aut-did")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_fired", r.ws, "Fired once", "run.failed", "triage", true)
	r.seedAutomation(t, "aut_idle", r.ws, "Never fired", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_fired")

	g := walk(t, r, "p1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasNode(g, "automation:aut_fired") {
		t.Errorf("the rule that actually fired is unreachable from its routine; nodes = %v", nodeIDs(g))
	}
	if hasNode(g, "automation:aut_idle") {
		t.Errorf("a rule that never fired was surfaced alongside one that did; nodes = %v", nodeIDs(g))
	}
}

// triggered_by_id is polymorphic. An id that happens to equal an automations.id
// must not be dereferenced as a rule when triggered_via names another table.
func TestWalk_AutomationTriggeredByIsNotFollowedBlind(t *testing.T) {
	r := newRig(t, "ws-aut-poly")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage", "run.failed", "triage", true)
	// A SCHEDULE-triggered run whose triggered_by_id collides with the rule id.
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "schedule", "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasEdge(g, "automation:aut_1", "run:run-1", EdgeTriggers) {
		t.Error("followed triggered_by_id as an automation although triggered_via is 'schedule'")
	}
}

// Automations are workspace-scoped and the walk hops to them on an untyped
// string column, so a colliding id in another tenant is the realistic leak.
func TestWalk_AutomationFromAnotherWorkspaceNeverAppears(t *testing.T) {
	r := newRig(t, "ws-aut-fence")
	r.seedWorkspace(t, "ws-other", "ws-other-slug")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRoutine(t, "p-other", "ws-other", "triage")
	// The rule lives in the OTHER workspace; our run names its id.
	r.seedAutomation(t, "aut_foreign", "ws-other", "Theirs", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_foreign")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasNode(g, "automation:aut_foreign") {
		t.Errorf("a rule from another workspace was attached to this run; nodes = %v", nodeIDs(g))
	}
	for _, n := range g.Nodes {
		if n.Ref == "p-other" {
			t.Errorf("the other workspace's routine leaked in via its rule; nodes = %v", nodeIDs(g))
		}
	}

	// And it must not be anchorable from here either.
	if _, err := Walk(context.Background(), r.db, r.ws, "aut_foreign", Options{}); !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("err = %v, want ErrAnchorNotFound for a foreign automation anchor", err)
	}
}

// REVERSED. This assertion used to be its opposite — "a soft-deleted rule must
// not appear" — on the reasoning that the rule is not-found on every other
// surface, so the chain should agree with them.
//
// That reasoning applied an operational predicate to a historical question.
// pipeline_runs keeps triggered_via='automation' forever, so under the old
// behaviour a deleted rule left a run that RECORDS being started by a rule
// with no rule beside it, and the reader concludes nobody started it. Hiding
// only the rule's name while the fact of it stays on the row is the worst of
// both: see TestWalk_ADeletedRuleStillExplainsTheRunsItCaused.
//
// What survives from the original is the assertion below that the run does not
// vanish with its rule — that was right then and is right now.
func TestWalk_ADeletedRuleDoesNotTakeItsRunWithIt(t *testing.T) {
	r := newRig(t, "ws-aut-deleted")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Deleted rule", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")
	r.softDeleteAutomation(t, "aut_1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasNode(g, "run:run-1") {
		t.Errorf("the run vanished along with its deleted rule; nodes = %v", nodeIDs(g))
	}
	if !hasNode(g, "routine:p1") {
		t.Errorf("the routine vanished along with the deleted rule; nodes = %v", nodeIDs(g))
	}
}

// The workspace fence is NOT relaxed along with the liveness filter. Reading a
// deleted rule crosses one boundary deliberately; crossing the tenant boundary
// with it would turn a history feature into a cross-tenant read.
func TestWalk_ADeletedRuleInAnotherWorkspaceStaysInvisible(t *testing.T) {
	r := newRig(t, "ws-aut-deleted-fence")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_foreign", "ws-somebody-else", "Theirs", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_foreign")
	r.softDeleteAutomation(t, "aut_foreign")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasNode(g, "automation:aut_foreign") {
		t.Errorf("a deleted rule from another workspace leaked into the chain; nodes = %v", nodeIDs(g))
	}
	if _, err := Walk(context.Background(), r.db, r.ws, "aut_foreign", Options{}); !errors.Is(err, ErrAnchorNotFound) {
		t.Fatalf("err = %v, want ErrAnchorNotFound for a foreign deleted automation anchor", err)
	}
}

// A rule whose routine_slug does not resolve (renamed or deleted routine) is
// still a valid anchor — it just has no routine to show. Returning an error
// would make the rule unreadable exactly when the author needs to see why it
// stopped working.
func TestWalk_AutomationWithUnresolvableRoutineIsStillAnchorable(t *testing.T) {
	r := newRig(t, "ws-aut-dangling")
	r.seedAutomation(t, "aut_1", r.ws, "Points at nothing", "run.failed", "gone", true)

	g := walk(t, r, "aut_1", Options{})

	if g.AnchorNode != "automation:aut_1" {
		t.Fatalf("anchor_node = %q, want automation:aut_1", g.AnchorNode)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("nodes = %v, want just the rule", nodeIDs(g))
	}
}

// chain_depth tells a reader they are looking at a COMPOSED chain — a run that
// a rule started off another run — which the hop distance from the anchor
// cannot say, because that is a property of the query, not of the run.
func TestWalk_RunNodeCarriesChainDepth(t *testing.T) {
	r := newRig(t, "ws-aut-depth")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage", "run.failed", "triage", true)
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1")
	r.exec(t, `UPDATE pipeline_runs SET chain_depth = 3 WHERE id = 'run-1'`)

	// Reachable both as the anchor and as a discovered neighbour: a field set
	// on only one path is the bug this covers.
	for _, anchor := range []string{"run-1", "aut_1", "p1"} {
		g := walk(t, r, anchor, Options{MaxDepth: 4, MaxNodes: 100})
		n := nodeByID(t, g, "run:run-1")
		if n.ChainDepth != 3 {
			t.Errorf("anchor %q: chain_depth = %d, want 3", anchor, n.ChainDepth)
		}
	}
}

// ---------------------------------------------------------------------------
// Option clamping.
// ---------------------------------------------------------------------------

func TestClampOptions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		in               Options
		wantDepth, wantN int
	}{
		{"zero -> defaults", Options{}, DefaultMaxDepth, DefaultMaxNodes},
		{"negative -> defaults", Options{MaxDepth: -3, MaxNodes: -1}, DefaultMaxDepth, DefaultMaxNodes},
		{"over ceiling -> ceiling", Options{MaxDepth: 999, MaxNodes: 100000}, MaxMaxDepth, MaxMaxNodes},
		{"in range -> unchanged", Options{MaxDepth: 2, MaxNodes: 7}, 2, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clampOptions(tc.in)
			if got.MaxDepth != tc.wantDepth || got.MaxNodes != tc.wantN {
				t.Errorf("clampOptions(%+v) = %+v, want depth %d nodes %d", tc.in, got, tc.wantDepth, tc.wantN)
			}
		})
	}
}

func TestWalk_RejectsEmptyWorkspace(t *testing.T) {
	r := newRig(t, "ws-empty")
	if _, err := Walk(context.Background(), r.db, "", "ENG-1", Options{}); err == nil {
		t.Fatal("a walk with no workspace must fail closed, not scan every tenant")
	}
}

// ---------------------------------------------------------------------------
// The automation edge
// ---------------------------------------------------------------------------

// A run an automation fired must name the rule that fired it.
//
// expandRun dereferences triggered_by_id for triggered_via 'issue' and
// 'call_pipeline' and for nothing else, so a run stamped 'automation' — the
// stamp the dispatcher now sets, and the one `routine records` prints — has no
// parent edge at all. The graph presents it as a chain root: a run that
// started for no reason anybody can see.
//
// resolveAnchor carries the same gap from the other direction, with a comment
// saying "there is no automations table on this branch". There is one, since
// migration 20260807160000; this package was simply never told.
//
// Observed on a live instance on 2026-08-07: `crewship chain <run-id>` on a
// run whose triggered_via was 'automation' and whose triggered_by_id named a
// real rule printed two nodes — the run and its routine — and one edge. The
// rule and the issue whose status change caused it were both absent, and
// `crewship chain <automation-id>` 404s.
//
// The assertion is on the automation's Ref rather than on a NodeKind constant
// so it does not prejudge what the kind is called.
func TestWalk_RunTriggeredByAutomationNamesTheRule(t *testing.T) {
	r := newRig(t, "ws-auto")
	pid := r.seedRoutine(t, "pl-1", r.ws, "triage")
	r.exec(t, `
		INSERT INTO automations (id, workspace_id, name, event_type, action_kind,
		                         action_config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'routine', '{"routine_slug":"triage"}', ?, ?)`,
		"aut_1", r.ws, "triage on close", "mission.status_change",
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	runID := r.seedRun(t, "run-1", r.ws, pid, "triage", "automation", "aut_1")

	g, err := Walk(context.Background(), r.db, r.ws, runID, Options{})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for _, n := range g.Nodes {
		if n.Ref == "aut_1" {
			return
		}
	}
	var got []string
	for _, n := range g.Nodes {
		got = append(got, string(n.Kind)+":"+n.Ref)
	}
	t.Fatalf("no node for automation aut_1; got %v — a run stamped triggered_via=automation "+
		"is rendered as a chain root, so the one surface that answers \"what caused this\" "+
		"cannot see the composition edge", got)
}

// ── The routine → agent edge ────────────────────────────────────────────────

// seedDispatchedAssignment writes an assignment the way a ROUTINE's
// assignment.create verb writes one: no parent assignment, a parent_run_id
// naming the run that dispatched it. Raw SQL for the same reason
// seedAutomation is — the walk reads the schema, so the fixture asserts
// against the schema.
func (r *rig) seedDispatchedAssignment(t *testing.T, id, wsID, task, runID string) string {
	t.Helper()
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, parent_run_id)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		id, wsID, r.chat, r.agent, r.agent, task, nullable(runID))
	return id
}

// The hop the whole trace was missing. A routine dispatches an agent, and
// until parent_run_id existed nothing recorded which run did it — so the walk
// could group the work into the trace and never draw the line, which is the
// one sentence the picture exists to say.
func TestWalk_AssignmentWalksBackToTheRunThatDispatchedIt(t *testing.T) {
	r := newRig(t, "ws-dispatch-up")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "manual", "")
	r.seedDispatchedAssignment(t, "asg-1", r.ws, "summarise the thread", "run-1")

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})

	if !hasNode(g, "run:run-1") {
		t.Fatalf("the run that dispatched this agent is not in the graph; nodes = %v", nodeIDs(g))
	}
	if !hasEdge(g, "run:run-1", "assignment:asg-1", EdgeTriggers) {
		t.Errorf("missing run -> assignment edge; edges: %#v", g.Edges)
	}
}

// And downward: from a run, the agents it put to work. Without this half a
// reader who starts at the routine — the common case — sees the run and never
// the work it caused.
func TestWalk_RunReachesTheAgentsItDispatched(t *testing.T) {
	r := newRig(t, "ws-dispatch-down")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "manual", "")
	r.seedDispatchedAssignment(t, "asg-1", r.ws, "first", "run-1")
	r.seedDispatchedAssignment(t, "asg-2", r.ws, "second", "run-1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	for _, id := range []string{"assignment:asg-1", "assignment:asg-2"} {
		if !hasNode(g, id) {
			t.Errorf("%s missing; nodes = %v", id, nodeIDs(g))
		}
		if !hasEdge(g, "run:run-1", id, EdgeTriggers) {
			t.Errorf("missing run -> %s edge", id)
		}
	}
}

// An assignment nobody dispatched must not gain an edge. An invented one reads
// exactly like a real one and points at nothing — worse than the gap, which
// the walk at least declares.
func TestWalk_AnUndispatchedAssignmentGainsNoRunEdge(t *testing.T) {
	r := newRig(t, "ws-dispatch-none")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "manual", "")
	r.seedAssignment(t, "asg-1", r.ws, "nobody sent me", "")

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasNode(g, "run:run-1") {
		t.Errorf("an unrelated run was drawn as the dispatcher; nodes = %v", nodeIDs(g))
	}
}

// The tenant fence, on the new edge too. Every other lookup in this walk is
// workspace-scoped and this one carries the same risk: a dispatching run named
// across the boundary would put another tenant's run id into this graph.
func TestWalk_ADispatchingRunInAnotherWorkspaceStaysInvisible(t *testing.T) {
	r := newRig(t, "ws-dispatch-fence")
	r.seedWorkspace(t, "ws-somebody-else", "somebody-else")
	r.seedRoutine(t, "p1", "ws-somebody-else", "triage")
	r.seedRun(t, "run-foreign", "ws-somebody-else", "p1", "triage", "manual", "")
	r.seedDispatchedAssignment(t, "asg-1", r.ws, "mine", "run-foreign")

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasNode(g, "run:run-foreign") {
		t.Errorf("a run from another workspace leaked in; nodes = %v", nodeIDs(g))
	}
}

// The fence on the DOWNWARD half too. Asserting it upward is not enough: this
// direction is the more dangerous one, because a run enumerates rows rather
// than dereferencing a single id, so a missing fence lists another tenant's
// agent work inside this workspace's graph.
//
// Added after a mutation that removed this exact WHERE clause left every test
// green — the upward fence test could not see it.
func TestWalk_ARunNeverListsAnotherWorkspacesAssignments(t *testing.T) {
	r := newRig(t, "ws-dispatch-fence-down")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRun(t, "run-1", r.ws, "p1", "triage", "manual", "")

	// Same dispatching run id, an assignment belonging to somebody else.
	r.seedWorkspace(t, "ws-other-tenant", "other-tenant")
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, parent_run_id)
		VALUES ('asg-theirs', 'ws-other-tenant', ?, ?, ?, 'their work', 'PENDING', 'run-1')`,
		r.chat, r.agent, r.agent)
	r.seedDispatchedAssignment(t, "asg-mine", r.ws, "my work", "run-1")

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	if hasNode(g, "assignment:asg-theirs") {
		t.Errorf("another tenant's assignment was listed under this run; nodes = %v", nodeIDs(g))
	}
	if !hasNode(g, "assignment:asg-mine") {
		t.Errorf("the workspace's own assignment went missing; nodes = %v", nodeIDs(g))
	}
}
