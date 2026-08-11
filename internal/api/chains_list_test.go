package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// Fixture.
//
// Runs are written through pipeline.RunStore — the ONLY production writer of
// pipeline_runs (executor.persistRunStart is its single caller) — rather than
// with hand-rolled INSERTs. That is the whole difference between a test that
// proves the index and one that proves a fixture: chain_origin, started_at and
// the terminal columns are then stamped by the same code the server runs, so a
// column rename or a changed default breaks this test instead of sliding past
// it.
//
// The one deliberate exception is seedPreMigrationRun, which writes raw SQL
// because the production path can no longer produce a NULL chain_origin — that
// row shape only exists in databases that predate the column, and reproducing
// it is the point of the test that uses it.
// ---------------------------------------------------------------------------

type chainsListRig struct {
	h     *ChainsListHandler
	db    *sql.DB
	runs  *pipeline.RunStore
	user  string
	ws    string
	crew  string
	agent string
	// lead is the agent the crewship dispatcher acts as, and chat is the
	// session its dispatches are filed under — both required by the real
	// assignment.create door.
	lead string
	chat string
	// assign and issues are the PRODUCTION doors a routine step reaches
	// through the crewship verbs. Fixtures go through them rather than
	// INSERTing rows, for the reason stated at the top of this file.
	assign *AssignmentHandler
	issues *InternalIssueHandler
	jw     *journal.Writer
	// clock hands out strictly increasing started_at values, so "newest
	// first" is an assertion about ordering rather than about how fast the
	// test machine inserts rows.
	clock time.Time
}

// chainsListWorkers are the dispatch targets, by slug. Six of them because the
// per-row fan-out cap is MaxChainSummaryRefs and a cap is only tested by
// exceeding it; all six fit inside the default delegation fan-out of 8.
var chainsListWorkers = []string{"ada", "bob", "cy", "di", "ed", "fi"}

func newChainsListRig(t *testing.T) *chainsListRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "chains-crew", wsID, "Chains Crew", "chains-crew")
	agentID := seedAgentRow(t, db, "chains-agent", wsID, crewID, "Ada", "ada", "AGENT")
	r := &chainsListRig{
		h:     NewChainsListHandler(db, newTestLogger()),
		db:    db,
		runs:  pipeline.NewRunStore(db),
		user:  userID,
		ws:    wsID,
		crew:  crewID,
		agent: agentID,
		clock: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
	}
	r.seedRoutine(t, "p1", wsID, "deploy")

	// An issue prefix and a LEAD are what the internal issue.create door
	// requires; without them it answers 400 rather than writing a row.
	r.exec(t, `UPDATE crews SET issue_prefix = 'ENG' WHERE id = ?`, crewID)
	r.lead = seedAgentRow(t, db, "chains-lead", wsID, crewID, "Lead", "lead", "LEAD")
	for _, slug := range chainsListWorkers[1:] { // [0] is the agent seeded above
		seedAgentRow(t, db, "chains-worker-"+slug, wsID, crewID, "Worker "+slug, slug, "AGENT")
	}
	r.chat = "chains-chat"
	r.exec(t, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`,
		r.chat, r.lead, wsID)

	r.assign = NewAssignmentHandler(db, nil, nil, "chains-internal-token", newTestLogger())
	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	r.issues = NewInternalIssueHandler(db, nil, newTestLogger())
	r.issues.SetJournal(jw)
	r.jw = jw
	return r
}

// ---------------------------------------------------------------------------
// The two production doors a routine step reaches through the crewship verbs.
//
// Both go through crewshipBody — the dispatcher's own body builder — so the
// provenance fields are stamped exactly as the step dispatcher stamps them,
// including author_run_id, the pointer every chain link on this page hangs off.
// A hand-written body here would be this test agreeing with itself about a key
// the dispatcher may not even send.
// ---------------------------------------------------------------------------

func (r *chainsListRig) crewshipCall(t *testing.T, verb, runID string, args map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(crewshipBody(pipeline.CrewshipRequest{
		Verb:        verb,
		Args:        args,
		WorkspaceID: r.ws,
		CrewID:      r.crew,
		AgentID:     r.lead,
		RunID:       runID,
	}))
	if err != nil {
		t.Fatalf("marshal %s body: %v", verb, err)
	}
	return raw
}

// dispatchAgent runs the assignment.create verb: the routine hop that gives an
// assignment its chain_origin and parent_run_id. Returns the assignment id.
func (r *chainsListRig) dispatchAgent(t *testing.T, runID, targetSlug string) string {
	t.Helper()
	raw := r.crewshipCall(t, "assignment.create", runID, map[string]any{
		"target_slug": targetSlug,
		"task":        "work for " + targetSlug,
		"chat_id":     r.chat,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.assign.Create(w, req)
	t.Cleanup(r.assign.WaitDispatches)
	return createdAssignmentID(t, w)
}

// issueFromRun runs the issue.create verb. insertIssueTx stores author_run_id
// on the missions row, which is the only exact record that a run CREATED an
// issue. Returns the identifier.
func (r *chainsListRig) issueFromRun(t *testing.T, runID, title string) string {
	t.Helper()
	raw := r.crewshipCall(t, "issue.create", runID, map[string]any{"title": title})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/issues", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.issues.Create(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("issue.create: status = %d, body %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode issue.create response %q: %v", w.Body.String(), err)
	}
	if resp["identifier"] == "" {
		t.Fatalf("issue.create returned no identifier: %s", w.Body.String())
	}
	return resp["identifier"]
}

// moveIssueFromRun runs the issue.update verb, which is what puts a
// mission.status_change journal entry on the run's trace — the record that a
// run CHANGED an issue it did not create.
func (r *chainsListRig) moveIssueFromRun(t *testing.T, runID, identifier, status string) {
	t.Helper()
	raw := r.crewshipCall(t, "issue.update", runID, map[string]any{"status": status})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/internal/issues/"+identifier, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("identifier", identifier)
	w := httptest.NewRecorder()
	r.issues.UpdateStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("issue.update %s: status = %d, body %s", identifier, w.Code, w.Body.String())
	}
	// The writer batches; the index reads the table, so drain it here rather
	// than making every assertion racy against a flush interval.
	if err := r.jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
}

func (r *chainsListRig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func (r *chainsListRig) tick() time.Time {
	r.clock = r.clock.Add(time.Minute)
	return r.clock
}

func (r *chainsListRig) seedWorkspace(t *testing.T, id, slug string) {
	t.Helper()
	r.exec(t, `INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, id, id, slug)
}

func (r *chainsListRig) seedRoutine(t *testing.T, id, wsID, slug string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES (?, ?, ?, ?, '{}', 'h')`, id, wsID, slug, slug)
}

// run is one run as the executor would persist it: a root stamps its OWN id as
// chain_origin, a composed run inherits the origin it was handed. Passing
// origin explicitly (rather than defaulting it here) keeps the two cases
// visible at every call site, which is where the grouping is actually decided.
type runSpec struct {
	id         string
	ws         string
	pipelineID string
	slug       string
	via        pipeline.TriggeredVia
	byID       string
	userID     string
	depth      int
	origin     string
}

func (r *chainsListRig) seedRun(t *testing.T, spec runSpec) string {
	t.Helper()
	if spec.ws == "" {
		spec.ws = r.ws
	}
	if spec.pipelineID == "" {
		spec.pipelineID = "p1"
	}
	if spec.slug == "" {
		spec.slug = "deploy"
	}
	if spec.origin == "" {
		spec.origin = spec.id // a root run IS its own origin
	}
	rec := &pipeline.RunRecord{
		ID:             spec.id,
		WorkspaceID:    spec.ws,
		PipelineID:     spec.pipelineID,
		PipelineSlug:   spec.slug,
		Status:         pipeline.RunStatusRunning,
		StartedAt:      r.tick(),
		TriggeredVia:   spec.via,
		InvokingUserID: spec.userID,
		ChainDepth:     spec.depth,
		ChainOrigin:    spec.origin,
	}
	if spec.byID != "" {
		rec.TriggeredByID = spec.byID
	}
	if err := r.runs.Insert(context.Background(), rec); err != nil {
		t.Fatalf("insert run %s: %v", spec.id, err)
	}
	return spec.id
}

// finish drives the production terminal write, so status/ended_at come from
// MarkTerminal rather than from this test's idea of what those columns hold.
func (r *chainsListRig) finish(t *testing.T, runID string, status pipeline.RunStatus) {
	t.Helper()
	if err := r.runs.MarkTerminal(context.Background(), pipeline.MarkTerminalInput{
		RunID:   runID,
		Status:  status,
		EndedAt: r.tick(),
	}); err != nil {
		t.Fatalf("mark terminal %s: %v", runID, err)
	}
}

// seedPreMigrationRun writes the one row shape the production path cannot make
// any more: chain_origin NULL, as every run recorded before migration
// 20260807160100 carries.
func (r *chainsListRig) seedPreMigrationRun(t *testing.T, id string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via, chain_origin)
		VALUES (?, ?, 'p1', 'deploy', 'completed', ?, 'manual', NULL)`,
		id, r.ws, r.tick().Format(time.RFC3339Nano))
}

func (r *chainsListRig) seedAutomation(t *testing.T, id, wsID, name, eventType string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	r.exec(t, `
		INSERT INTO automations (id, workspace_id, name, enabled, event_type, matcher_json,
			action_kind, action_config_json, debounce_seconds, max_per_hour, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, '{}', 'routine', '{"routine_slug":"deploy"}', 10, 60, ?, ?)`,
		id, wsID, name, eventType, now, now)
}

func (r *chainsListRig) seedIssue(t *testing.T, id, wsID, crewID, agentID, identifier, title string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, identifier)
		VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', ?)`,
		id, wsID, crewID, agentID, "trace-"+id, title, identifier)
}

func (r *chainsListRig) seedSchedule(t *testing.T, id, wsID, name string) {
	t.Helper()
	r.exec(t, `
		INSERT INTO pipeline_schedules (id, workspace_id, name, target_pipeline_id, cron_expr)
		VALUES (?, ?, ?, 'p1', '0 8 * * *')`, id, wsID, name)
}

// list drives the handler the way the mux would: workspace and user in context.
func (r *chainsListRig) list(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/v1/chains"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withWorkspaceUser(req, r.user, r.ws, "OWNER")
	rr := httptest.NewRecorder()
	r.h.List(rr, req)
	return rr
}

// chainsListRow decodes one row off the wire. Deliberately a hand-written
// mirror of ChainSummary rather than the type itself: a test that unmarshals
// into the production struct cannot catch a renamed json tag, because both
// sides move together.
type chainsListRow struct {
	Origin        string `json:"origin"`
	StartedByKind string `json:"started_by_kind"`
	StartedByID   string `json:"started_by_id"`
	StartedByKey  string `json:"started_by_key"`
	StartedBy     string `json:"started_by"`
	TriggeredVia  string `json:"triggered_via"`
	RoutineID     string `json:"routine_id"`
	RoutineSlug   string `json:"routine_slug"`
	Runs          int    `json:"runs"`
	MaxChainDepth int    `json:"max_chain_depth"`
	FailedRuns    int    `json:"failed_runs"`
	RunningRuns   int    `json:"running_runs"`
	WaitingRuns   int    `json:"waiting_runs"`
	Failed        bool   `json:"failed"`
	FirstActivity string `json:"first_activity"`
	LastActivity  string `json:"last_activity"`
	DurationMS    *int64 `json:"duration_ms"`
	Issues        []struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		Created    bool   `json:"created"`
	} `json:"issues"`
	IssueCount int `json:"issue_count"`
	Agents     []struct {
		ID          string `json:"id"`
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Assignments int    `json:"assignments"`
	} `json:"agents"`
	AgentCount int `json:"agent_count"`
}

func (c chainsListRow) issueIdentifiers() []string {
	out := make([]string, 0, len(c.Issues))
	for _, i := range c.Issues {
		out = append(out, i.Identifier)
	}
	sort.Strings(out)
	return out
}

func (c chainsListRow) agentSlugs() []string {
	out := make([]string, 0, len(c.Agents))
	for _, a := range c.Agents {
		out = append(out, a.Slug)
	}
	sort.Strings(out)
	return out
}

type chainsListBody struct {
	Chains            []chainsListRow `json:"chains"`
	Count             int             `json:"count"`
	Limit             int             `json:"limit"`
	Offset            int             `json:"offset"`
	HasMore           bool            `json:"has_more"`
	HasUnrecordedRuns bool            `json:"has_unrecorded_runs"`
}

// byOrigin indexes the page so a test can name the row it means.
func (b chainsListBody) byOrigin(t *testing.T, origin string) chainsListRow {
	t.Helper()
	for _, c := range b.Chains {
		if c.Origin == origin {
			return c
		}
	}
	t.Fatalf("chain %q missing from the index; got %v", origin, b.origins())
	return chainsListRow{}
}

func decodeChainsList(t *testing.T, rr *httptest.ResponseRecorder) chainsListBody {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var b chainsListBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v; body: %s", err, rr.Body.String())
	}
	return b
}

func (b chainsListBody) origins() []string {
	out := make([]string, 0, len(b.Chains))
	for _, c := range b.Chains {
		out = append(out, c.Origin)
	}
	return out
}

// ---------------------------------------------------------------------------
// Required behaviours.
// ---------------------------------------------------------------------------

// The route sits behind wsCtx; this is the assertion that survives someone
// re-registering it without the middleware.
func TestChainsList_NoWorkspace_401(t *testing.T) {
	r := newChainsListRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/chains", nil)
	rr := httptest.NewRecorder()
	r.h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", rr.Code, rr.Body.String())
	}
}

// One row per chain, not per run: the three runs of one composed chain
// collapse into a single row carrying the chain's size and depth.
func TestChainsList_GroupsRunsByChainOrigin(t *testing.T) {
	r := newChainsListRig(t)
	root := r.seedRun(t, runSpec{id: "prn_root", via: pipeline.TriggeredViaManual})
	r.finish(t, root, pipeline.RunStatusCompleted)
	r.seedRun(t, runSpec{id: "prn_c1", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 1, origin: root})
	r.seedRun(t, runSpec{id: "prn_c2", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 2, origin: root})
	// A second, unrelated chain of one run, started later.
	lone := r.seedRun(t, runSpec{id: "prn_lone", via: pipeline.TriggeredViaManual})

	b := decodeChainsList(t, r.list(t, ""))

	if b.Count != 2 || len(b.Chains) != 2 {
		t.Fatalf("count = %d, chains = %v, want 2 rows (one per chain)", b.Count, b.origins())
	}
	// Newest first: the lone run started after the composed chain's last run.
	if got := b.origins(); got[0] != lone || got[1] != root {
		t.Errorf("origins = %v, want newest first [%s %s]", got, lone, root)
	}
	chain := b.Chains[1]
	if chain.Runs != 3 {
		t.Errorf("runs = %d, want 3 (the root and both composed runs)", chain.Runs)
	}
	if chain.MaxChainDepth != 2 {
		t.Errorf("max_chain_depth = %d, want the deepest hop 2", chain.MaxChainDepth)
	}
	if chain.FirstActivity == "" || chain.LastActivity == "" || chain.FirstActivity >= chain.LastActivity {
		t.Errorf("first/last activity = %q/%q, want a real window", chain.FirstActivity, chain.LastActivity)
	}
	if chain.RoutineSlug != "deploy" || chain.RoutineID != "p1" {
		t.Errorf("routine = %s/%s, want p1/deploy from the root run", chain.RoutineID, chain.RoutineSlug)
	}
	if b.Chains[0].Runs != 1 || b.Chains[0].MaxChainDepth != 0 {
		t.Errorf("lone chain = %+v, want a single run at depth 0", b.Chains[0])
	}
}

// A chain is failed when ANY run in it failed, whether or not that run is the
// root — a triage routine that a rule fired and that then died is exactly the
// row an operator is scanning for.
func TestChainsList_FailedRunAnywhereMarksTheChain(t *testing.T) {
	r := newChainsListRig(t)
	root := r.seedRun(t, runSpec{id: "prn_root", via: pipeline.TriggeredViaManual})
	r.finish(t, root, pipeline.RunStatusCompleted)
	child := r.seedRun(t, runSpec{id: "prn_child", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 1, origin: root})
	r.finish(t, child, pipeline.RunStatusFailed)
	clean := r.seedRun(t, runSpec{id: "prn_clean", via: pipeline.TriggeredViaManual})
	r.finish(t, clean, pipeline.RunStatusCompleted)

	b := decodeChainsList(t, r.list(t, ""))

	if len(b.Chains) != 2 {
		t.Fatalf("chains = %v, want the failed chain and the clean one", b.origins())
	}
	for _, c := range b.Chains {
		switch c.Origin {
		case root:
			if !c.Failed || c.FailedRuns != 1 {
				t.Errorf("chain %s: failed = %v (%d runs), want true with 1 failed run", root, c.Failed, c.FailedRuns)
			}
		case clean:
			if c.Failed || c.FailedRuns != 0 {
				t.Errorf("chain %s: failed = %v (%d runs), want a clean chain", clean, c.Failed, c.FailedRuns)
			}
		default:
			t.Errorf("unexpected chain %q in the index", c.Origin)
		}
	}
}

// "What started it" is the column the Activity sidebar renders, so each
// triggered_via must resolve to something a human recognises rather than to an
// opaque id.
func TestChainsList_ResolvesWhatStartedTheChain(t *testing.T) {
	r := newChainsListRig(t)
	r.seedAutomation(t, "aut_1", r.ws, "Triage on failure", "run.failed")
	r.seedIssue(t, "mis_1", r.ws, r.crew, r.agent, "ENG-7", "Deploy is flaky")
	r.seedSchedule(t, "psched_1", r.ws, "Nightly deploy")

	r.seedRun(t, runSpec{id: "prn_rule", via: pipeline.TriggeredViaAutomation, byID: "aut_1"})
	r.seedRun(t, runSpec{id: "prn_issue", via: pipeline.TriggeredViaIssue, byID: "ENG-7"})
	r.seedRun(t, runSpec{id: "prn_sched", via: pipeline.TriggeredViaSchedule, byID: "psched_1"})
	r.seedRun(t, runSpec{id: "prn_manual", via: pipeline.TriggeredViaManual, userID: r.user})
	// A trigger this index has no resolver for must still say what it knows —
	// the raw triggered_via — rather than rendering as an empty cell.
	r.seedRun(t, runSpec{id: "prn_wake", via: pipeline.TriggeredViaWakeCheck})

	b := decodeChainsList(t, r.list(t, ""))
	got := map[string]struct{ kind, id, key, label string }{}
	for _, c := range b.Chains {
		got[c.Origin] = struct{ kind, id, key, label string }{c.StartedByKind, c.StartedByID, c.StartedByKey, c.StartedBy}
	}

	for _, tc := range []struct{ origin, kind, id, key, label string }{
		{"prn_rule", "automation", "aut_1", "run.failed", "Triage on failure"},
		{"prn_issue", "issue", "mis_1", "ENG-7", "Deploy is flaky"},
		{"prn_sched", "schedule", "psched_1", "", "Nightly deploy"},
		{"prn_manual", "user", r.user, "", "Test User"},
		{"prn_wake", "unknown", "", "", "wake_check"},
	} {
		g, ok := got[tc.origin]
		if !ok {
			t.Errorf("chain %s missing from the index", tc.origin)
			continue
		}
		if g.kind != tc.kind {
			t.Errorf("%s: started_by_kind = %q, want %q", tc.origin, g.kind, tc.kind)
		}
		if tc.id != "" && g.id != tc.id {
			t.Errorf("%s: started_by_id = %q, want %q", tc.origin, g.id, tc.id)
		}
		if g.key != tc.key {
			t.Errorf("%s: started_by_key = %q, want %q", tc.origin, g.key, tc.key)
		}
		if g.label != tc.label {
			t.Errorf("%s: started_by = %q, want %q", tc.origin, g.label, tc.label)
		}
	}
}

// The fence. A chain in another workspace must not appear, and must not be
// reachable by asking for it: chain_origin is an untyped string column and a
// missing workspace predicate here is a cross-tenant read of the whole
// workflow history.
func TestChainsList_ForeignWorkspaceChainNeverAppears(t *testing.T) {
	r := newChainsListRig(t)
	r.seedWorkspace(t, "ws-other", "other")
	r.seedRoutine(t, "p-other", "ws-other", "theirs")
	r.seedRun(t, runSpec{id: "prn_theirs", ws: "ws-other", pipelineID: "p-other", slug: "theirs", via: pipeline.TriggeredViaManual})
	mine := r.seedRun(t, runSpec{id: "prn_mine", via: pipeline.TriggeredViaManual})

	b := decodeChainsList(t, r.list(t, ""))

	if got := b.origins(); len(got) != 1 || got[0] != mine {
		t.Fatalf("origins = %v, want only this workspace's chain %s", got, mine)
	}
	for _, c := range b.Chains {
		if c.RoutineSlug == "theirs" {
			t.Errorf("a routine from another workspace surfaced: %+v", c)
		}
	}
}

// The label joins are the second half of the fence. triggered_by_id is an
// untyped string — a rule id, an issue IDENTIFIER, a schedule id — and issue
// identifiers are only unique PER WORKSPACE, so an unfenced join happily
// borrows another tenant's issue title or rule name for a run of ours.
func TestChainsList_ForeignRuleAndIssueNeverSupplyTheLabel(t *testing.T) {
	r := newChainsListRig(t)
	r.seedWorkspace(t, "ws-other", "other")
	r.exec(t, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('other-crew', 'ws-other', 'C', 'c')`)
	r.exec(t, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('other-agent', 'ws-other', 'other-crew', 'G', 'g')`)
	r.seedAutomation(t, "aut_foreign", "ws-other", "Their secret rule", "run.failed")
	r.seedIssue(t, "mis_foreign", "ws-other", "other-crew", "other-agent", "ENG-7", "Their secret issue")

	r.seedRun(t, runSpec{id: "prn_rule", via: pipeline.TriggeredViaAutomation, byID: "aut_foreign"})
	r.seedRun(t, runSpec{id: "prn_issue", via: pipeline.TriggeredViaIssue, byID: "ENG-7"})

	b := decodeChainsList(t, r.list(t, ""))

	if len(b.Chains) != 2 {
		t.Fatalf("chains = %v, want both of this workspace's chains", b.origins())
	}
	for _, c := range b.Chains {
		if c.StartedBy == "Their secret rule" || c.StartedBy == "Their secret issue" {
			t.Errorf("chain %s borrowed a label from another workspace: %+v", c.Origin, c)
		}
		if c.StartedByID == "mis_foreign" {
			t.Errorf("chain %s resolved a foreign mission id: %+v", c.Origin, c)
		}
	}
}

// Runs recorded before the chain_origin column landed carry NULL, and the link
// they would have had was never written. They are excluded rather than shown as
// orphan single-run chains — but their existence is declared, so a client can
// say "older runs predate chain recording" instead of implying the workspace
// never ran anything.
func TestChainsList_PreMigrationRunsAreExcludedAndDeclared(t *testing.T) {
	r := newChainsListRig(t)
	r.seedPreMigrationRun(t, "prn_old")
	mine := r.seedRun(t, runSpec{id: "prn_new", via: pipeline.TriggeredViaManual})

	b := decodeChainsList(t, r.list(t, ""))

	if got := b.origins(); len(got) != 1 || got[0] != mine {
		t.Fatalf("origins = %v, want only the recorded chain %s", got, mine)
	}
	if !b.HasUnrecordedRuns {
		t.Error("has_unrecorded_runs = false while a run with no chain_origin exists: the gap is silent")
	}

	// And when there are none, the flag must be false — otherwise it is
	// decoration rather than a signal.
	clean := newChainsListRig(t)
	clean.seedRun(t, runSpec{id: "prn_only", via: pipeline.TriggeredViaManual})
	if cb := decodeChainsList(t, clean.list(t, "")); cb.HasUnrecordedRuns {
		t.Error("has_unrecorded_runs = true on a database with no pre-migration runs")
	}
}

// Run retention sweeps old rows, and a chain outlives its root: the composed
// runs still carry the origin id of a run that is no longer in the table. The
// chain must still be listed — its runs are right there — with an honest
// "we cannot tell what started it" rather than a fabricated cause.
func TestChainsList_ChainWhoseRootRunIsGoneIsStillListed(t *testing.T) {
	r := newChainsListRig(t)
	r.seedRun(t, runSpec{id: "prn_c1", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 1, origin: "prn_swept"})
	r.seedRun(t, runSpec{id: "prn_c2", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 2, origin: "prn_swept"})

	b := decodeChainsList(t, r.list(t, ""))

	if got := b.origins(); len(got) != 1 || got[0] != "prn_swept" {
		t.Fatalf("origins = %v, want the orphaned chain prn_swept", got)
	}
	c := b.Chains[0]
	if c.Runs != 2 || c.MaxChainDepth != 2 {
		t.Errorf("chain = %+v, want both surviving runs and depth 2", c)
	}
	if c.StartedByKind != "unknown" || c.StartedBy != "" {
		t.Errorf("started_by = %q/%q, want an admitted unknown rather than a guess", c.StartedByKind, c.StartedBy)
	}
}

// An unbounded GROUP BY over every run in a busy workspace is a slow query
// behind an authenticated route. The default has to be small, the ceiling
// enforced, and the page has to say whether more exists.
func TestChainsList_LimitDefaultsCapsAndPaginates(t *testing.T) {
	r := newChainsListRig(t)
	for _, id := range []string{"prn_1", "prn_2", "prn_3"} {
		r.seedRun(t, runSpec{id: id, via: pipeline.TriggeredViaManual})
	}

	def := decodeChainsList(t, r.list(t, ""))
	if def.Limit != DefaultChainsListLimit {
		t.Errorf("limit = %d, want the default %d echoed back", def.Limit, DefaultChainsListLimit)
	}
	if def.HasMore {
		t.Error("has_more = true with 3 chains inside the default page")
	}

	capped := decodeChainsList(t, r.list(t, "limit=99999"))
	if capped.Limit != MaxChainsListLimit {
		t.Errorf("limit = %d, want the ceiling %d", capped.Limit, MaxChainsListLimit)
	}

	first := decodeChainsList(t, r.list(t, "limit=2"))
	if len(first.Chains) != 2 || !first.HasMore {
		t.Fatalf("page 1 = %v (has_more=%v), want 2 rows and has_more", first.origins(), first.HasMore)
	}
	second := decodeChainsList(t, r.list(t, "limit=2&offset=2"))
	if len(second.Chains) != 1 || second.HasMore || second.Offset != 2 {
		t.Fatalf("page 2 = %v (has_more=%v, offset=%d), want the last row", second.origins(), second.HasMore, second.Offset)
	}
	if second.Chains[0].Origin == first.Chains[0].Origin || second.Chains[0].Origin == first.Chains[1].Origin {
		t.Errorf("page 2 repeats a row from page 1: %v then %v", first.origins(), second.origins())
	}
}

// ---------------------------------------------------------------------------
// A row identifies a RUN, not a routine.
// ---------------------------------------------------------------------------

// The complaint this exists to answer: two runs of one routine rendered as two
// identical lines — the routine slug and the number 1, twice, with nothing to
// tell them apart. A workflow row has to name what THAT run did.
//
// Same routine, same trigger kind, same user. The only things that differ are
// what the runs touched, and that has to be enough.
func TestChainsList_TwoRunsOfOneRoutineAreTellableApart(t *testing.T) {
	r := newChainsListRig(t)

	first := r.seedRun(t, runSpec{id: "prn_first", via: pipeline.TriggeredViaManual, userID: r.user})
	firstIssue := r.issueFromRun(t, first, "Cold start is slow")
	r.dispatchAgent(t, first, "ada")
	r.finish(t, first, pipeline.RunStatusCompleted)

	second := r.seedRun(t, runSpec{id: "prn_second", via: pipeline.TriggeredViaManual, userID: r.user})
	secondIssue := r.issueFromRun(t, second, "Sidecar leaks a socket")
	r.dispatchAgent(t, second, "bob")
	r.dispatchAgent(t, second, "cy")
	r.finish(t, second, pipeline.RunStatusCompleted)

	b := decodeChainsList(t, r.list(t, ""))
	a := b.byOrigin(t, first)
	z := b.byOrigin(t, second)

	// The premise: on the fields the row already had, these two are the same
	// line. If this ever stops holding the rest of the test proves nothing.
	if a.RoutineSlug != z.RoutineSlug || a.Runs != z.Runs || a.StartedByKind != z.StartedByKind {
		t.Fatalf("the two chains already differ on the old fields (%s/%d/%s vs %s/%d/%s); "+
			"this test only means something while they do not",
			a.RoutineSlug, a.Runs, a.StartedByKind, z.RoutineSlug, z.Runs, z.StartedByKind)
	}

	if got := a.issueIdentifiers(); len(got) != 1 || got[0] != firstIssue {
		t.Errorf("chain %s issues = %v, want the issue it created, %q", first, got, firstIssue)
	}
	if got := z.issueIdentifiers(); len(got) != 1 || got[0] != secondIssue {
		t.Errorf("chain %s issues = %v, want the issue it created, %q", second, got, secondIssue)
	}
	if a.IssueCount != 1 || z.IssueCount != 1 {
		t.Errorf("issue_count = %d and %d, want 1 each", a.IssueCount, z.IssueCount)
	}
	if got := a.agentSlugs(); len(got) != 1 || got[0] != "ada" {
		t.Errorf("chain %s agents = %v, want [ada]", first, got)
	}
	if got := z.agentSlugs(); len(got) != 2 || got[0] != "bob" || got[1] != "cy" {
		t.Errorf("chain %s agents = %v, want [bob cy]", second, got)
	}
	if a.AgentCount != 1 || z.AgentCount != 2 {
		t.Errorf("agent_count = %d and %d, want 1 and 2", a.AgentCount, z.AgentCount)
	}
	for _, ag := range z.Agents {
		if ag.Assignments != 1 {
			t.Errorf("chain %s agent %s: assignments = %d, want 1", second, ag.Slug, ag.Assignments)
		}
	}
	// And an issue this run CREATED is marked as such — the strongest noun a
	// row can carry, because it exists only because of this run.
	for _, iss := range a.Issues {
		if !iss.Created {
			t.Errorf("issue %s: created = false, but %s is the run that authored it", iss.Identifier, first)
		}
	}
}

// An issue the chain only MOVED is on the row too, and is not claimed as one it
// created. The two are different facts about the same run and the distinction is
// the whole reason the flag exists rather than a bare list.
func TestChainsList_IssueChangedByTheChainIsListedWithoutClaimingItWasCreated(t *testing.T) {
	r := newChainsListRig(t)

	// An issue that already existed, authored by a run in an unrelated chain.
	earlier := r.seedRun(t, runSpec{id: "prn_earlier", via: pipeline.TriggeredViaManual, userID: r.user})
	existing := r.issueFromRun(t, earlier, "Deploy is flaky")
	r.finish(t, earlier, pipeline.RunStatusCompleted)

	mover := r.seedRun(t, runSpec{id: "prn_mover", via: pipeline.TriggeredViaManual, userID: r.user})
	r.moveIssueFromRun(t, mover, existing, "TODO")
	r.finish(t, mover, pipeline.RunStatusCompleted)

	b := decodeChainsList(t, r.list(t, ""))
	row := b.byOrigin(t, mover)

	if got := row.issueIdentifiers(); len(got) != 1 || got[0] != existing {
		t.Fatalf("chain %s issues = %v, want the issue it moved, %q", mover, got, existing)
	}
	if row.Issues[0].Created {
		t.Errorf("issue %s reads created=true on chain %s, which only moved it — "+
			"the run that authored it was %s", existing, mover, earlier)
	}
	if row.Issues[0].Title != "Deploy is flaky" {
		t.Errorf("issue title = %q, want the title a human recognises", row.Issues[0].Title)
	}
	// The chain that DID create it still says so.
	if created := b.byOrigin(t, earlier); len(created.Issues) != 1 || !created.Issues[0].Created {
		t.Errorf("chain %s = %+v, want the issue it authored marked created", earlier, created.Issues)
	}
}

// ---------------------------------------------------------------------------
// How long it took.
// ---------------------------------------------------------------------------

// Wall clock between the first and the last activity, NOT the sum of the runs'
// own duration_ms.
//
// Same decision lib/activity-stream.chainElapsedMs made client-side, for the
// same two reasons: the sum reads 0 for work no agent billed time for, and it
// counts a nested run's time twice inside the run that contains it. The fixture
// pins the first half — every run here finishes with duration_ms 0, exactly as
// a run of agentless steps does — so a summing implementation reports "instant"
// on a chain that took three minutes.
func TestChainsList_ElapsedIsWallClockNotTheSumOfRunDurations(t *testing.T) {
	r := newChainsListRig(t)

	root := r.seedRun(t, runSpec{id: "prn_root", via: pipeline.TriggeredViaManual})
	r.finish(t, root, pipeline.RunStatusCompleted)
	child := r.seedRun(t, runSpec{id: "prn_child", via: pipeline.TriggeredViaAutomation, byID: "aut_x", depth: 1, origin: root})
	r.finish(t, child, pipeline.RunStatusCompleted)

	// Non-vacuous: assert the column a summing implementation would read really
	// does hold zero, so "not the sum" is a claim about this data.
	var summed int64
	if err := r.db.QueryRow(
		`SELECT COALESCE(SUM(duration_ms),0) FROM pipeline_runs WHERE chain_origin = ?`, root).Scan(&summed); err != nil {
		t.Fatalf("sum duration_ms: %v", err)
	}
	if summed != 0 {
		t.Fatalf("per-run duration_ms sums to %d; this test needs the agentless shape (0)", summed)
	}

	row := decodeChainsList(t, r.list(t, "")).byOrigin(t, root)

	// clock ticks a minute per event: start root, end root, start child, end
	// child — three minutes from the first activity to the last.
	const wantMS = int64(3 * 60 * 1000)
	if row.DurationMS == nil {
		t.Fatalf("duration_ms is null on a chain spanning %v..%v", row.FirstActivity, row.LastActivity)
	}
	if *row.DurationMS != wantMS {
		t.Errorf("duration_ms = %d, want %d — the wall clock between first_activity and "+
			"last_activity, not the %d the per-run durations sum to",
			*row.DurationMS, wantMS, summed)
	}
}

// One run, still going: there is no span to measure between, and 0 would assert
// "it was instant". Null is the honest answer — the same rule chainElapsedMs
// applies to a chain of fewer than two datable entries.
func TestChainsList_ElapsedIsNullWhenThereIsNothingToMeasureBetween(t *testing.T) {
	r := newChainsListRig(t)
	only := r.seedRun(t, runSpec{id: "prn_only", via: pipeline.TriggeredViaManual})

	row := decodeChainsList(t, r.list(t, "")).byOrigin(t, only)

	if row.FirstActivity != row.LastActivity {
		t.Fatalf("first/last = %q/%q; this test needs the single-open-run shape",
			row.FirstActivity, row.LastActivity)
	}
	if row.DurationMS != nil {
		t.Errorf("duration_ms = %d on a run that has not ended; want null, because 0 reads "+
			"as 'it was instant' where the truth is 'it has not finished'", *row.DurationMS)
	}
}

// ---------------------------------------------------------------------------
// Fan-out.
// ---------------------------------------------------------------------------

// This is a LIST. The nouns are capped per row so one authenticated request
// cannot fan out over every chain in the workspace — and the count is exact, so
// the reader is told what the cap hid rather than shown a short list that looks
// complete.
func TestChainsList_TouchedNounsAreCappedPerRowAndCountedInFull(t *testing.T) {
	r := newChainsListRig(t)
	run := r.seedRun(t, runSpec{id: "prn_busy", via: pipeline.TriggeredViaManual})

	const issues = MaxChainSummaryRefs + 2
	for i := 0; i < issues; i++ {
		r.issueFromRun(t, run, fmt.Sprintf("Issue number %d", i))
	}
	for _, slug := range chainsListWorkers {
		r.dispatchAgent(t, run, slug)
	}
	r.finish(t, run, pipeline.RunStatusCompleted)

	row := decodeChainsList(t, r.list(t, "")).byOrigin(t, run)

	if len(row.Issues) != MaxChainSummaryRefs {
		t.Errorf("issues = %d entries, want the cap %d — an uncapped per-row subquery over "+
			"every chain is the slow query this endpoint must not become",
			len(row.Issues), MaxChainSummaryRefs)
	}
	if row.IssueCount != issues {
		t.Errorf("issue_count = %d, want %d — the count is what tells the reader the list was cut",
			row.IssueCount, issues)
	}
	if len(row.Agents) != MaxChainSummaryRefs {
		t.Errorf("agents = %d entries, want the cap %d", len(row.Agents), MaxChainSummaryRefs)
	}
	if row.AgentCount != len(chainsListWorkers) {
		t.Errorf("agent_count = %d, want %d", row.AgentCount, len(chainsListWorkers))
	}
}

// ---------------------------------------------------------------------------
// The fence, for the joins this page adds.
// ---------------------------------------------------------------------------

// chain_origin on assignments, author_run_id on missions and trace_id on
// journal_entries are all untyped string columns with no foreign key behind
// them, exactly like chain_origin on runs. A join missing the workspace hands
// another tenant's issue titles and agent names to a row of ours.
//
// The three foreign rows are written with raw SQL for the same reason
// seedPreMigrationRun is: the production path cannot produce a cross-tenant
// pointer, and reproducing one is the entire point of the test.
func TestChainsList_ForeignWorkspaceWorkNeverAttachesToARow(t *testing.T) {
	r := newChainsListRig(t)
	mine := r.seedRun(t, runSpec{id: "prn_mine", via: pipeline.TriggeredViaManual})

	// One issue and one agent that ARE ours, so the row has a real answer to
	// compare against. Without them every fence in these two queries is
	// redundant with every other — an empty row is an empty row however it got
	// that way — and the test would pass with three of the four removed.
	ourIssue := r.issueFromRun(t, mine, "Ours")
	r.dispatchAgent(t, mine, "ada")

	r.seedWorkspace(t, "ws-other", "other")
	r.exec(t, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('other-crew', 'ws-other', 'C', 'c')`)
	r.exec(t, `INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('other-agent', 'ws-other', 'other-crew', 'Their Secret Agent', 'ghost')`)
	r.exec(t, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('other-chat', 'other-agent', 'ws-other', 'CHAT', 'ACTIVE')`)
	r.seedIssue(t, "mis_foreign", "ws-other", "other-crew", "other-agent", "THEIRS-1", "Their secret issue")

	// Their agent work, pointed at OUR chain — what the outer workspace
	// predicate on assignments has to exclude.
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, chain_origin, created_at)
		VALUES ('asn_foreign', 'ws-other', 'other-chat', 'other-agent', 'other-agent', 'theirs', 'PENDING', 1, ?, datetime('now'))`,
		mine)
	// OUR assignment, addressed to THEIR agent — what the join predicate has to
	// exclude. assignments.assigned_to_id has a foreign key to agents but none
	// to a workspace, so the row is legal and the agent name behind it is not
	// ours to render. A separate case from the one above: a fence on the outer
	// predicate alone lets this one through.
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, chain_origin, created_at)
		VALUES ('asn_crossed', ?, ?, ?, 'other-agent', 'crossed', 'PENDING', 1, ?, datetime('now'))`,
		r.ws, r.chat, r.lead, mine)
	// Their issue, claiming OUR run authored it.
	r.exec(t, `UPDATE missions SET author_run_id = ? WHERE id = 'mis_foreign'`, mine)
	// Their journal entry, on OUR run's trace.
	r.exec(t, `
		INSERT INTO journal_entries (id, workspace_id, mission_id, entry_type, severity, actor_type, summary, trace_id, ts)
		VALUES ('jrn_foreign', 'ws-other', 'mis_foreign', 'mission.status_change', 'info', 'system', 'moved', ?, ?)`,
		mine, time.Now().UTC().Format(time.RFC3339Nano))
	// OUR journal entry, on our run's trace, naming THEIR issue.
	// journal_entries.mission_id has a foreign key to missions and none to a
	// workspace, so the entry-side fence alone does not stop this one — it is
	// the missions join that has to carry j.workspace_id.
	r.exec(t, `
		INSERT INTO journal_entries (id, workspace_id, mission_id, entry_type, severity, actor_type, summary, trace_id, ts)
		VALUES ('jrn_crossed', ?, 'mis_foreign', 'mission.status_change', 'info', 'system', 'moved', ?, ?)`,
		r.ws, mine, time.Now().UTC().Format(time.RFC3339Nano))

	row := decodeChainsList(t, r.list(t, "")).byOrigin(t, mine)

	if got := row.issueIdentifiers(); len(got) != 1 || got[0] != ourIssue {
		t.Errorf("chain %s issues = %+v, want only our own %q", mine, row.Issues, ourIssue)
	}
	// The count is asserted separately from the list because they are computed
	// at different points: the total is a window function over the grouped set,
	// the list is what survives the final label join. A fence missing from an
	// EARLIER arm lets a foreign issue into the count while the last join still
	// keeps it off the wire — a row that says "2 issues" and shows one.
	if row.IssueCount != 1 {
		t.Errorf("issue_count = %d, want 1 — a foreign issue reached the total even though "+
			"the label join kept it out of the list", row.IssueCount)
	}
	if got := row.agentSlugs(); len(got) != 1 || got[0] != "ada" {
		t.Errorf("chain %s agents = %+v, want only our own [ada]", mine, row.Agents)
	}
	if row.AgentCount != 1 {
		t.Errorf("agent_count = %d, want 1", row.AgentCount)
	}
}

// ---------------------------------------------------------------------------
// Live state: what is still going, and what is still asking.
//
// A chain's timestamps cannot answer either question. last_activity falls back
// to started_at while a run is in flight, so a chain that has been parked on an
// approval since Tuesday and one that finished on Tuesday carry the same
// instant — and the rail's "Active now" section, which is the reason a person
// opens this page twice in a day, would have to guess between them.
// ---------------------------------------------------------------------------

// TestChainsList_RunStillGoingIsCountedAsRunning proves the count comes from
// the run's STATUS and not from a missing ended_at, which is the cheap
// derivation that would also report an interrupted run as running forever.
func TestChainsList_RunStillGoingIsCountedAsRunning(t *testing.T) {
	r := newChainsListRig(t)
	live := r.seedRun(t, runSpec{id: "prn_live", via: pipeline.TriggeredViaManual})
	done := r.seedRun(t, runSpec{id: "prn_done", via: pipeline.TriggeredViaManual})
	r.finish(t, done, pipeline.RunStatusCompleted)

	b := decodeChainsList(t, r.list(t, ""))
	for _, c := range b.Chains {
		switch c.Origin {
		case live:
			if c.RunningRuns != 1 || c.WaitingRuns != 0 {
				t.Errorf("live chain: running=%d waiting=%d, want 1 and 0", c.RunningRuns, c.WaitingRuns)
			}
		case done:
			if c.RunningRuns != 0 || c.WaitingRuns != 0 {
				t.Errorf("finished chain: running=%d waiting=%d, want 0 and 0", c.RunningRuns, c.WaitingRuns)
			}
		default:
			t.Errorf("unexpected chain %q", c.Origin)
		}
	}
}

// TestChainsList_RunParkedOnAnApprovalIsWaitingNotRunning is the distinction
// the section header rests on: both are non-terminal, but only one of them will
// never move without a person. Counting them together would put "awaiting your
// approval" under the same word as "busy", and the reader would stop looking.
func TestChainsList_RunParkedOnAnApprovalIsWaitingNotRunning(t *testing.T) {
	r := newChainsListRig(t)
	parked := r.seedRun(t, runSpec{id: "prn_parked", via: pipeline.TriggeredViaManual})
	if err := r.runs.MarkWaiting(context.Background(), parked, "approve"); err != nil {
		t.Fatalf("mark waiting: %v", err)
	}

	b := decodeChainsList(t, r.list(t, ""))
	if len(b.Chains) != 1 {
		t.Fatalf("chains = %v, want one", b.origins())
	}
	if got := b.Chains[0]; got.WaitingRuns != 1 || got.RunningRuns != 0 {
		t.Errorf("parked chain: waiting=%d running=%d, want 1 and 0", got.WaitingRuns, got.RunningRuns)
	}
}

// TestChainsList_LiveCountsAreScopedToTheWorkspace fences the two new columns
// the way every other predicate in this query is fenced. chain_origin has no
// foreign key behind it, so an unfenced SUM lets another tenant's in-flight run
// light up the "Active now" section of a workspace it does not belong to.
func TestChainsList_LiveCountsAreScopedToTheWorkspace(t *testing.T) {
	r := newChainsListRig(t)
	ours := r.seedRun(t, runSpec{id: "prn_ours", via: pipeline.TriggeredViaManual})
	r.finish(t, ours, pipeline.RunStatusCompleted)
	// Another tenant stamps OUR origin on a run that is still going.
	r.seedWorkspace(t, "ws_other", "other")
	r.seedRoutine(t, "p_other", "ws_other", "deploy")
	r.seedRun(t, runSpec{id: "prn_theirs", ws: "ws_other", pipelineID: "p_other", via: pipeline.TriggeredViaManual, origin: ours})

	b := decodeChainsList(t, r.list(t, ""))
	if len(b.Chains) != 1 {
		t.Fatalf("chains = %v, want only ours", b.origins())
	}
	if got := b.Chains[0].RunningRuns; got != 0 {
		t.Errorf("running_runs = %d, want 0 — a foreign run must not light up our chain", got)
	}
}
