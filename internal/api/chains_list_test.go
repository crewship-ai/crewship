package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	// clock hands out strictly increasing started_at values, so "newest
	// first" is an assertion about ordering rather than about how fast the
	// test machine inserts rows.
	clock time.Time
}

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
	return r
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

type chainsListBody struct {
	Chains []struct {
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
		Failed        bool   `json:"failed"`
		FirstActivity string `json:"first_activity"`
		LastActivity  string `json:"last_activity"`
	} `json:"chains"`
	Count             int  `json:"count"`
	Limit             int  `json:"limit"`
	Offset            int  `json:"offset"`
	HasMore           bool `json:"has_more"`
	HasUnrecordedRuns bool `json:"has_unrecorded_runs"`
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
