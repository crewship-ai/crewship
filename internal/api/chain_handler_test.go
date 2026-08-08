package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// chainRig builds the minimum row set the chain walk needs: a workspace, a
// crew and an agent (missions.crew_id / lead_agent_id are NOT NULL FKs), and
// the handler under test.
type chainRig struct {
	h     *ChainHandler
	db    *sql.DB
	user  string
	ws    string
	crew  string
	agent string
}

func newChainRig(t *testing.T) *chainRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "chain-crew", wsID, "Chain Crew", "chain-crew")
	agentID := seedAgentRow(t, db, "chain-agent", wsID, crewID, "Ada", "ada", "AGENT")
	return &chainRig{
		h:     NewChainHandler(db, newTestLogger()),
		db:    db,
		user:  userID,
		ws:    wsID,
		crew:  crewID,
		agent: agentID,
	}
}

func (r *chainRig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func (r *chainRig) seedIssue(t *testing.T, id, identifier, title string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier)
		VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', ?)`,
		id, r.ws, r.crew, r.agent, "trace-"+id, title, identifier)
}

// get drives the handler the way the mux would: path value set, workspace and
// user in context.
func (r *chainRig) get(t *testing.T, anchor, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/v1/chains/" + anchor
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("anchor", anchor)
	req = withWorkspaceUser(req, r.user, r.ws, "OWNER")
	rr := httptest.NewRecorder()
	r.h.Get(rr, req)
	return rr
}

type chainBody struct {
	Anchor     string `json:"anchor"`
	AnchorNode string `json:"anchor_node"`
	MaxDepth   int    `json:"max_depth"`
	MaxNodes   int    `json:"max_nodes"`
	Nodes      []struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		Ref           string `json:"ref"`
		Key           string `json:"key"`
		Label         string `json:"label"`
		Status        string `json:"status"`
		Depth         int    `json:"depth"`
		Anchor        bool   `json:"anchor"`
		Partial       bool   `json:"partial"`
		PartialReason string `json:"partial_reason"`
	} `json:"nodes"`
	Edges []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"`
	} `json:"edges"`
	Truncated   bool   `json:"truncated"`
	TruncatedBy string `json:"truncated_by"`
	Gaps        []struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	} `json:"gaps"`
}

func decodeChain(t *testing.T, rr *httptest.ResponseRecorder) chainBody {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var b chainBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v; body: %s", err, rr.Body.String())
	}
	return b
}

// A workspace-less request must not fall through to a walk. The route is
// registered behind wsCtx, so this is the belt to that braces — it is the
// assertion that survives someone re-registering the route without the
// middleware.
func TestChainHandler_NoWorkspace_401(t *testing.T) {
	r := newChainRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/chains/ENG-1", nil)
	req.SetPathValue("anchor", "ENG-1")
	rr := httptest.NewRecorder()
	r.h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestChainHandler_UnknownAnchor_404(t *testing.T) {
	r := newChainRig(t)
	rr := r.get(t, "NOPE-1", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// The endpoint's default view: an issue nobody has worked yet is a chain of
// one, not an error. A 404 here would make the UI unable to tell "bad anchor"
// from "nothing has happened".
func TestChainHandler_AnchorWithNoChain_OneNodeNoEdges(t *testing.T) {
	r := newChainRig(t)
	r.seedIssue(t, "m1", "ENG-1", "Untouched")

	b := decodeChain(t, r.get(t, "ENG-1", ""))

	if len(b.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1; body = %+v", len(b.Nodes), b.Nodes)
	}
	if len(b.Edges) != 0 {
		t.Errorf("edges = %+v, want none", b.Edges)
	}
	if b.Truncated {
		t.Error("truncated = true on a single-node chain")
	}
	if b.AnchorNode != "issue:m1" || !b.Nodes[0].Anchor {
		t.Errorf("anchor_node = %q, node = %+v", b.AnchorNode, b.Nodes[0])
	}
	// truncated must be present in the JSON even when false — a client that
	// treats a missing key as "unknown" would otherwise never trust the flag.
	if !jsonHasKey(t, r.get(t, "ENG-1", "").Body.Bytes(), "truncated") {
		t.Error(`response omits "truncated" when false; the flag must always be on the wire`)
	}
}

func jsonHasKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	_, ok := m[key]
	return ok
}

// The response must always carry the gap list, so a client can tell "nothing
// is attached" apart from "we cannot see what is attached".
func TestChainHandler_AlwaysReportsKnownGaps(t *testing.T) {
	r := newChainRig(t)
	r.seedIssue(t, "m1", "ENG-1", "Untouched")

	b := decodeChain(t, r.get(t, "ENG-1", ""))

	if len(b.Gaps) == 0 {
		t.Fatal("gaps is empty: the response implies every link is walkable")
	}
	for _, g := range b.Gaps {
		if g.Reason == "" {
			t.Errorf("gap %s->%s carries no reason", g.From, g.To)
		}
	}
	if !b.Nodes[0].Partial || b.Nodes[0].PartialReason == "" {
		t.Errorf("issue node = %+v, want partial with a reason (inbox_items has no mission column)", b.Nodes[0])
	}
}

// depth/limit are the caller's half of the bound. They must reach the walk,
// and truncation must be reported rather than implied by a short list.
func TestChainHandler_LimitQueryParamTruncates(t *testing.T) {
	r := newChainRig(t)
	r.exec(t, `
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('p1', ?, 'fanout', 'fanout', '{}', 'h')`, r.ws)
	for i := 0; i < 8; i++ {
		r.exec(t, `
			INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via)
			VALUES (?, ?, 'p1', 'fanout', 'completed', ?, 'manual')`,
			"run-"+string(rune('a'+i)), r.ws, time.Now().UTC().Format(time.RFC3339Nano))
	}

	b := decodeChain(t, r.get(t, "fanout", "limit=3"))

	if b.MaxNodes != 3 {
		t.Errorf("max_nodes = %d, want the requested 3 echoed back", b.MaxNodes)
	}
	if len(b.Nodes) > 3 {
		t.Errorf("len(nodes) = %d, want <= 3", len(b.Nodes))
	}
	if !b.Truncated || b.TruncatedBy != "nodes" {
		t.Errorf("truncated = %v by %q, want true by \"nodes\"", b.Truncated, b.TruncatedBy)
	}
}

func TestChainHandler_DepthQueryParamTruncates(t *testing.T) {
	r := newChainRig(t)
	r.exec(t, `INSERT INTO chats (id, workspace_id, agent_id) VALUES ('c1', ?, ?)`, r.ws, r.agent)
	parent := any(nil)
	for _, id := range []string{"a0", "a1", "a2"} {
		r.exec(t, `
			INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, parent_assignment_id)
			VALUES (?, ?, 'c1', ?, ?, ?, ?)`, id, r.ws, r.agent, r.agent, id, parent)
		parent = id
	}

	b := decodeChain(t, r.get(t, "a0", "depth=1"))

	if b.MaxDepth != 1 {
		t.Errorf("max_depth = %d, want 1", b.MaxDepth)
	}
	if !b.Truncated || b.TruncatedBy != "depth" {
		t.Errorf("truncated = %v by %q, want true by \"depth\"", b.Truncated, b.TruncatedBy)
	}
	for _, n := range b.Nodes {
		if n.Depth > 1 {
			t.Errorf("node %s at depth %d exceeds the requested depth 1", n.ID, n.Depth)
		}
	}
}

// A garbage depth must not 400 — a stray query param appended by a proxy
// should still get an answer, and the server clamps regardless.
func TestChainHandler_UnparseableBoundsFallBackToDefaults(t *testing.T) {
	r := newChainRig(t)
	r.seedIssue(t, "m1", "ENG-1", "Untouched")

	b := decodeChain(t, r.get(t, "ENG-1", "depth=banana&limit=-9999"))

	if b.MaxDepth != 4 || b.MaxNodes != 200 {
		t.Errorf("max_depth/max_nodes = %d/%d, want the package defaults 4/200", b.MaxDepth, b.MaxNodes)
	}
}

// The route resolves the anchor inside the caller's workspace only. Two
// workspaces can hold the same issue identifier since the identifier
// uniqueness migration, so this is the case that a missing workspace
// predicate would leak.
func TestChainHandler_ForeignIdentifierIsNotFound(t *testing.T) {
	r := newChainRig(t)
	r.exec(t, `INSERT INTO workspaces (id, name, slug) VALUES ('other-ws', 'Other', 'other')`)
	r.exec(t, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('other-crew', 'other-ws', 'C', 'c')`)
	r.exec(t, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('other-agent', 'other-ws', 'other-crew', 'G', 'g')`)
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier)
		VALUES ('m-other', 'other-ws', 'other-crew', 'other-agent', 'trace-other', 'theirs', 'PLANNING', 'ENG-1')`)

	rr := r.get(t, "ENG-1", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an identifier that only exists in another workspace; body: %s",
			rr.Code, rr.Body.String())
	}
}

// End-to-end through the handler, so the JSON contract the CLI decodes is the
// one under test rather than the Go struct.
func TestChainHandler_IssueToRunToNestedRun(t *testing.T) {
	r := newChainRig(t)
	r.seedIssue(t, "m1", "ENG-5", "Ship it")
	r.exec(t, `
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('p1', ?, 'deploy', 'Deploy', '{}', 'h')`, r.ws)
	r.exec(t, `UPDATE missions SET routine_id = 'p1' WHERE id = 'm1'`)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via, triggered_by_id)
		VALUES ('run-1', ?, 'p1', 'deploy', 'failed', ?, 'issue', 'ENG-5')`, r.ws, now)
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via, triggered_by_id)
		VALUES ('run-2', ?, 'p1', 'deploy', 'failed', ?, 'call_pipeline', 'run-1')`, r.ws, now)
	r.exec(t, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, payload_json)
		VALUES ('ibx-1', ?, 'failed_run', 'run-2', 'deploy failed', '{"run_id":"run-2"}')`, r.ws)

	b := decodeChain(t, r.get(t, "ENG-5", "depth=6&limit=50"))

	want := [][3]string{
		{"issue:m1", "routine:p1", "triggers"},
		{"issue:m1", "run:run-1", "triggers"},
		{"routine:p1", "run:run-1", "runs"},
		{"run:run-1", "run:run-2", "triggers"},
		{"run:run-2", "inbox:ibx-1", "produces"},
	}
	for _, wnt := range want {
		found := false
		for _, e := range b.Edges {
			if e.From == wnt[0] && e.To == wnt[1] && e.Kind == wnt[2] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing edge %s -%s-> %s\nedges: %+v", wnt[0], wnt[2], wnt[1], b.Edges)
		}
	}
	// Every edge endpoint must be a node that is actually in the response.
	present := map[string]bool{}
	for _, n := range b.Nodes {
		present[n.ID] = true
	}
	for _, e := range b.Edges {
		if !present[e.From] || !present[e.To] {
			t.Errorf("edge %s -%s-> %s references a node that is not in nodes[]", e.From, e.Kind, e.To)
		}
	}
}
