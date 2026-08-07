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
