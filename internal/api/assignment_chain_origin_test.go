package api

// The bridge between the two trees of caused work — the tests that define it.
//
// Crewship traces routine work by pipeline_runs.chain_origin and agent work by
// assignments.parent_assignment_id, and until this change the second carried no
// trace id at all. `crewship chain ENG-6` on an issue whose closure started a
// whole process answered one node and zero edges: every assignment was its own
// island.
//
// These tests are written against the REAL doors, deliberately. Three tests
// landed on this branch the same week that passed while proving nothing, all by
// the same route — the fixture built, by hand, data that no production path
// produces, and then asserted on it. So:
//
//   - the routine hop posts the body crewshipBody ACTUALLY ASSEMBLES (the
//     dispatcher's own function, called here) to the route the dispatcher posts
//     it to, and reads the column back out of the database;
//   - the delegation hop's PARENT row is produced by a real dispatch through
//     that same route, not by an INSERT this file writes. Only the parent's
//     status is forced afterwards, because the fixture's orchestrator is nil
//     and would otherwise finish the row before the second hop can inherit
//     from it. The value under test — the parent's chain_origin — is production
//     output in every case.
//
// The negatives matter as much as the positives: a root must keep an EMPTY
// origin rather than a fabricated one, an unresolvable run must not be copied
// into the tenant's trace, and a delegation must not re-root a chain it is
// already part of.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// seedChainRun writes the pipeline run a routine step would be executing.
// origin is that run's own chain_origin: empty means the run IS a chain root,
// which is what pipeline_runs stores for a run a human started.
func seedChainRun(t *testing.T, db *sql.DB, wsID, runID, origin string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		 VALUES ('pipe_chain', ?, 'chain-pipe', 'Chain Pipe', '{}', 'h')`, wsID); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	var originVal any
	if origin != "" {
		originVal = origin
	}
	if _, err := db.Exec(`
		INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode,
		                           started_at, triggered_via, chain_origin, created_at, updated_at)
		VALUES (?, ?, 'pipe_chain', 'chain-pipe', 'running', 'run', ?, 'schedule', ?, ?, ?)`,
		runID, wsID, now, originVal, now, now); err != nil {
		t.Fatalf("seed pipeline_run %s: %v", runID, err)
	}
}

// routineDispatch drives the assignment.create verb end to end: the body is
// built by the DISPATCHER's own assembler (crewshipBody) from the request a
// routine step makes, and posted to the route that verb names in
// pipeline.crewshipVerbs. Nothing about the body is invented here — that is the
// point, since a hand-written body is exactly how a test comes to assert on a
// shape production never sends.
func (f *delegationFixture) routineDispatch(t *testing.T, actingAgentID, runID, target string) *httptest.ResponseRecorder {
	t.Helper()
	body := crewshipBody(pipeline.CrewshipRequest{
		Verb: "assignment.create",
		Args: map[string]any{
			"target_slug": target,
			"task":        "work the routine asked for",
			"chat_id":     f.chat,
		},
		WorkspaceID: f.wsID,
		CrewID:      "crewD",
		AgentID:     actingAgentID,
		RunID:       runID,
	})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal dispatcher body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.Create(w, req)
	t.Cleanup(f.h.WaitDispatches)
	return w
}

// createdAssignmentID reads the id the route reports, so every assertion below
// is about the row THIS call wrote rather than about whatever is newest.
func createdAssignmentID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("dispatch: expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode dispatch response %q: %v", w.Body.String(), err)
	}
	if payload["assignment_id"] == "" {
		t.Fatalf("dispatch response names no assignment_id: %s", w.Body.String())
	}
	return payload["assignment_id"]
}

func chainOriginOf(t *testing.T, f *delegationFixture, assignmentID string) sql.NullString {
	t.Helper()
	var origin sql.NullString
	if err := f.db.QueryRow(`SELECT chain_origin FROM assignments WHERE id = ?`, assignmentID).Scan(&origin); err != nil {
		t.Fatalf("read chain_origin of %s: %v", assignmentID, err)
	}
	return origin
}

func parentAssignmentOf(t *testing.T, f *delegationFixture, assignmentID string) sql.NullString {
	t.Helper()
	var parent sql.NullString
	if err := f.db.QueryRow(`SELECT parent_assignment_id FROM assignments WHERE id = ?`, assignmentID).Scan(&parent); err != nil {
		t.Fatalf("read parent of %s: %v", assignmentID, err)
	}
	return parent
}

// holdInFlight freezes a row the fixture's nil orchestrator has already failed
// back into a state resolveDelegationScope recognises as the caller's own run.
// ONLY the status is written: the chain_origin the next hop inherits is the one
// the route wrote.
func holdInFlight(t *testing.T, f *delegationFixture, assignmentID string) {
	t.Helper()
	f.h.WaitDispatches()
	if _, err := f.db.Exec(`UPDATE assignments SET status='RUNNING' WHERE id = ?`, assignmentID); err != nil {
		t.Fatalf("hold %s in flight: %v", assignmentID, err)
	}
}

// ── The routine hop: a run dispatches an agent ──────────────────────────────

// A routine that is itself part of a chain hands that chain to the agent it
// dispatches. Without this the process a closed issue kicks off reads as a
// routine chain that stops dead at the moment real work began.
func TestAssignmentChainOrigin_RoutineDispatchInheritsTheCausingRunsChain(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))

	origin := chainOriginOf(t, f, id)
	if !origin.Valid || origin.String != "run_the_root" {
		t.Errorf("chain_origin = %v, want %q — the assignment must join the chain its "+
			"causing run belongs to, not start a fresh one; a chain that renumbers itself "+
			"at the agent boundary is why `crewship chain` returned one node",
			origin, "run_the_root")
	}
}

// When the causing run IS the root, the run names itself. Same rule
// internal/pipeline's chainOrigin applies one table over: inherit the
// ancestor's origin, else the parent is the origin.
func TestAssignmentChainOrigin_RoutineDispatchOffARootRunNamesThatRun(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_root", "")

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_root", "w1"))

	origin := chainOriginOf(t, f, id)
	if !origin.Valid || origin.String != "run_root" {
		t.Errorf("chain_origin = %v, want %q — a run with no origin of its own IS the root, "+
			"so the work it dispatches is rooted at it", origin, "run_root")
	}
}

// A run id that does not resolve in this workspace is not copied into the
// row. It is either a swept run or another tenant's, and a trace id nobody can
// resolve is worse than an absent one: it reads as evidence.
func TestAssignmentChainOrigin_UnresolvableCausingRunIsNotInvented(t *testing.T) {
	f := setupDelegationFixture(t)

	// A real run — in somebody else's workspace.
	if _, err := f.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other-chain', 'Other', 'other-chain')`); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	seedChainRun(t, f.db, "ws-other-chain", "run_of_another_tenant", "run_their_root")

	id := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_of_another_tenant", "w1"))

	if origin := chainOriginOf(t, f, id); origin.Valid {
		t.Errorf("chain_origin = %q, want NULL — a run outside this workspace must not be "+
			"resolved, and its chain must not be imported into this tenant's trace", origin.String)
	}
}

// ── The delegation hop: an agent dispatches an agent ────────────────────────

// The chain survives the agent-to-agent hop. The parent row here is written by
// the real routine dispatch above, so what is inherited is a value production
// produced.
func TestAssignmentChainOrigin_DelegationInheritsTheParentsOrigin(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")

	parent := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))
	if origin := chainOriginOf(t, f, parent); !origin.Valid || origin.String != "run_the_root" {
		t.Fatalf("precondition: parent chain_origin = %v, want %q", origin, "run_the_root")
	}
	holdInFlight(t, f, parent)

	child := createdAssignmentID(t, f.assign(t, f.work1, "w2", nil))

	if got := parentAssignmentOf(t, f, child); !got.Valid || got.String != parent {
		t.Fatalf("precondition: child's parent = %v, want %s — without the parent link this "+
			"test is not exercising the delegation hop at all", got, parent)
	}
	if origin := chainOriginOf(t, f, child); !origin.Valid || origin.String != "run_the_root" {
		t.Errorf("chain_origin = %v, want %q — a sub-agent's work belongs to the chain its "+
			"delegator was already part of", origin, "run_the_root")
	}
}

// A parent with no origin IS the root, so the child names the parent — not the
// parent's own (absent) origin, and not a fresh id.
func TestAssignmentChainOrigin_DelegationOffARootParentNamesTheParent(t *testing.T) {
	f := setupDelegationFixture(t)

	parent := createdAssignmentID(t, f.assign(t, f.lead, "w1", nil))
	if origin := chainOriginOf(t, f, parent); origin.Valid {
		t.Fatalf("precondition: a root dispatch must have no origin, got %q", origin.String)
	}
	holdInFlight(t, f, parent)

	child := createdAssignmentID(t, f.assign(t, f.work1, "w2", nil))

	if origin := chainOriginOf(t, f, child); !origin.Valid || origin.String != parent {
		t.Errorf("chain_origin = %v, want %q — the parent has no origin of its own, so the "+
			"parent is the root of this chain", origin, parent)
	}
}

// A delegation that ALSO carries a causing run must not re-root onto it. The
// tree already answered; taking the run instead would split one chain into two
// and lose the hops in between — the same renumbering bug the pipeline side
// fixed by resolving the ancestor's origin rather than the immediate parent's.
func TestAssignmentChainOrigin_DelegationDoesNotReRootOnACausingRun(t *testing.T) {
	f := setupDelegationFixture(t)
	seedChainRun(t, f.db, f.wsID, "run_hop_two", "run_the_root")
	seedChainRun(t, f.db, f.wsID, "run_unrelated", "run_some_other_root")

	parent := createdAssignmentID(t, f.routineDispatch(t, f.lead, "run_hop_two", "w1"))
	holdInFlight(t, f, parent)

	child := createdAssignmentID(t, f.routineDispatch(t, f.work1, "run_unrelated", "w2"))

	if got := parentAssignmentOf(t, f, child); !got.Valid || got.String != parent {
		t.Fatalf("precondition: child's parent = %v, want %s", got, parent)
	}
	if origin := chainOriginOf(t, f, child); !origin.Valid || origin.String != "run_the_root" {
		t.Errorf("chain_origin = %v, want %q — the delegating agent's own chain wins; a "+
			"causing run may only supply an origin the tree could not", origin, "run_the_root")
	}
}

// ── The root: no parent, no causing run ─────────────────────────────────────

// An assignment that nothing caused starts its own trace, and it does that by
// saying NOTHING. Stamping the row's own id here would be a fabricated origin
// that a reader cannot distinguish from an inherited one, and it would make
// every root look like a one-node chain that had already been walked.
func TestAssignmentChainOrigin_RootDispatchStartsItsOwnTraceBySayingNothing(t *testing.T) {
	f := setupDelegationFixture(t)

	id := createdAssignmentID(t, f.assign(t, f.lead, "w1", nil))

	if origin := chainOriginOf(t, f, id); origin.Valid {
		t.Errorf("chain_origin = %q, want NULL — a dispatch with no parent and no causing run "+
			"has no origin to inherit, and inventing one asserts a parentage that does not exist",
			origin.String)
	}
}

// The agent does not get to write its own trace id. Same rule as depth: a value
// the caller can supply is a value the caller can launder, and an origin chosen
// by the delegating agent would let a sub-chain hide inside somebody else's.
func TestAssignmentChainOrigin_AgentCannotSupplyItsOwn(t *testing.T) {
	f := setupDelegationFixture(t)

	id := createdAssignmentID(t, f.assign(t, f.lead, "w1", map[string]any{
		"chain_origin": "run_i_would_like_to_belong_to",
	}))

	if origin := chainOriginOf(t, f, id); origin.Valid {
		t.Errorf("chain_origin = %q, want NULL — the column is written from server state only",
			origin.String)
	}
}
