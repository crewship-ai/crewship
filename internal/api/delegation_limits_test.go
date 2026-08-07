package api

// Delegation caps — the tests that define them.
//
// These are written as security tests, not as feature tests: the interesting
// cases are the ones where the caller is TRYING to get past the cap, not the
// ones where it stays inside. Four properties:
//
//  1. a chain deeper than the limit is refused;
//  2. a run that has already dispatched its fan-out is refused;
//  3. the refusal names the limit and the setting that moves it, so the agent
//     can report something an operator can act on;
//  4. the agent cannot launder its own position in the tree — neither by
//     sending a depth, nor by naming a parent, nor by omitting both.
//
// Each boundary test carries its own mutation: the same request that is
// refused at the configured limit is ACCEPTED once the instance setting is
// raised by one. A cap that refuses unconditionally passes (1) and (2) and
// fails those.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// delegationFixture is one crew with a lead and two workers, plus the lead's
// chat. Assignment rows are added per test to shape the tree.
type delegationFixture struct {
	db    *sql.DB
	wsID  string
	chat  string
	lead  string
	work1 string
	work2 string
	h     *AssignmentHandler
}

func setupDelegationFixture(t *testing.T) *delegationFixture {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crewD', ?, 'Eng', 'eng')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('leadD', 'crewD', ?, 'Lead', 'lead')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('w1', 'crewD', ?, 'Worker One', 'w1')`, wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('w2', 'crewD', ?, 'Worker Two', 'w2')`, wsID)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chatD', 'leadD', ?, 'CHAT', 'ACTIVE')`, wsID)

	return &delegationFixture{
		db: db, wsID: wsID, chat: "chatD", lead: "leadD", work1: "w1", work2: "w2",
		h: NewAssignmentHandler(db, nil, nil, "token", newTestLogger()),
	}
}

// insertDelegatedAssignment writes one row of an in-flight delegation chain.
func (f *delegationFixture) insertDelegatedAssignment(t *testing.T, id, by, to string, depth int, parent string) {
	t.Helper()
	var parentVal any
	if parent != "" {
		parentVal = parent
	}
	if _, err := f.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, parent_assignment_id, created_at)
		VALUES (?, ?, ?, ?, ?, 'chain', 'RUNNING', ?, ?, datetime('now'))`,
		id, f.wsID, f.chat, by, to, depth, parentVal); err != nil {
		t.Fatalf("insert delegated assignment %s: %v", id, err)
	}
}

// insertRootDispatch writes the row a ROOT /assign produces: depth 1, no
// parent. Distinct from insertAssignment (assignments_queue_test.go), which
// leaves depth at the column default of 0 — the value that marks a row as one
// NO capped door wrote (a mission-engine row, or one predating the column).
func (f *delegationFixture) insertRootDispatch(t *testing.T, id, by, to, status string) {
	t.Helper()
	if _, err := f.db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at)
		VALUES (?, ?, ?, ?, ?, 'root dispatch', ?, 1, datetime('now'))`,
		id, f.wsID, f.chat, by, to, status); err != nil {
		t.Fatalf("insert root dispatch %s: %v", id, err)
	}
}

func (f *delegationFixture) setLimit(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := f.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, fmt.Sprint(v)); err != nil {
		t.Fatalf("set %s=%d: %v", key, v, err)
	}
}

// assign drives POST /api/v1/internal/assignments exactly as the sidecar does,
// with the acting agent resolved from the caller's per-agent token. extra is
// merged into the JSON body so a test can try to smuggle fields.
func (f *delegationFixture) assign(t *testing.T, actorAgentID, target string, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{
		"target_slug":    target,
		"task":           "do the thing",
		"crew_id":        "crewD",
		"workspace_id":   f.wsID,
		"chat_id":        f.chat,
		"actor_agent_id": actorAgentID,
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/assignments", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.h.Create(w, req)
	t.Cleanup(f.h.WaitDispatches)
	return w
}

func assignmentCount(t *testing.T, f *delegationFixture) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments`).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	return n
}

func responseError(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	for _, k := range []string{"error", "detail", "title"} {
		if v, ok := payload[k].(string); ok && v != "" {
			return v
		}
	}
	return w.Body.String()
}

// A chain at the depth limit may not add another hop.
//
// The tree is lead → w1 (depth 1) → w2 (depth 2). With max_depth=2, w2's own
// /assign would be depth 3 and is refused. Raising the setting to 3 accepts
// the identical request — the mutation that proves the boundary is read and
// not hard-coded.
func TestDelegationCaps_DepthChainBeyondTheLimitIsRefused(t *testing.T) {
	f := setupDelegationFixture(t)
	f.setLimit(t, SettingDelegationMaxDepth, 2)

	f.insertDelegatedAssignment(t, "a1", f.lead, f.work1, 1, "")
	f.insertDelegatedAssignment(t, "a2", f.work1, f.work2, 2, "a1")

	before := assignmentCount(t, f)
	w := f.assign(t, f.work2, "w1", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("depth 3 dispatch: expected 403, got %d (%s)", w.Code, w.Body.String())
	}
	if got := assignmentCount(t, f); got != before {
		t.Errorf("refused dispatch still wrote %d assignment row(s)", got-before)
	}

	msg := responseError(t, w)
	for _, want := range []string{"depth", "2", SettingDelegationMaxDepth} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q so the agent can report it; got %q", want, msg)
		}
	}

	// Mutation: one more hop of headroom and the same call goes through.
	f.setLimit(t, SettingDelegationMaxDepth, 3)
	w = f.assign(t, f.work2, "w1", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("max_depth=3 must accept the depth-3 dispatch, got %d (%s)", w.Code, w.Body.String())
	}
}

// The depth a run carries is the one the DB derives, never the one the caller
// sends. Three laundering shapes, all from a caller whose in-flight assignment
// already sits at the limit.
func TestDelegationCaps_AgentCannotLaunderItsOwnDepth(t *testing.T) {
	forged := []struct {
		name  string
		extra map[string]any
	}{
		{"depth zero", map[string]any{"depth": 0}},
		{"negative depth", map[string]any{"depth": -5}},
		{"orphan parent", map[string]any{"parent_assignment_id": ""}},
		{"foreign parent", map[string]any{"parent_assignment_id": "a-does-not-exist", "depth": 1}},
	}
	for _, tc := range forged {
		t.Run(tc.name, func(t *testing.T) {
			f := setupDelegationFixture(t)
			f.setLimit(t, SettingDelegationMaxDepth, 1)
			// w1 is executing a depth-1 assignment: anything it dispatches is depth 2.
			f.insertDelegatedAssignment(t, "a1", f.lead, f.work1, 1, "")

			w := f.assign(t, f.work1, "w2", tc.extra)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s got past the depth cap: %d (%s)", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// The depth actually stored is derived from the parent, so the next hop is
// measured from a number the agent never touched.
func TestDelegationCaps_StoredDepthIsDerivedFromTheParent(t *testing.T) {
	f := setupDelegationFixture(t)
	f.setLimit(t, SettingDelegationMaxDepth, 5)
	f.insertDelegatedAssignment(t, "a1", f.lead, f.work1, 2, "")

	w := f.assign(t, f.work1, "w2", map[string]any{"depth": 0})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}

	var depth int
	var parent string
	if err := f.db.QueryRow(
		`SELECT depth, COALESCE(parent_assignment_id,'') FROM assignments WHERE assigned_to_id = 'w2'`,
	).Scan(&depth, &parent); err != nil {
		t.Fatalf("read stored assignment: %v", err)
	}
	if depth != 3 {
		t.Errorf("stored depth = %d, want 3 (parent depth 2 + 1) — the body's depth:0 must be ignored", depth)
	}
	if parent != "a1" {
		t.Errorf("stored parent = %q, want a1 — the chain has to be reconstructible server-side", parent)
	}
}

// One run may not dispatch more than the fan-out limit, whether it is a
// delegated run (children of one parent) or a lead working a chat (its
// in-flight dispatches).
func TestDelegationCaps_FanoutBeyondTheLimitIsRefused(t *testing.T) {
	t.Run("delegated run", func(t *testing.T) {
		f := setupDelegationFixture(t)
		f.setLimit(t, SettingDelegationMaxFanout, 2)
		f.insertDelegatedAssignment(t, "parent", f.lead, f.work1, 1, "")
		f.insertDelegatedAssignment(t, "c1", f.work1, f.work2, 2, "parent")
		f.insertDelegatedAssignment(t, "c2", f.work1, f.work2, 2, "parent")

		w := f.assign(t, f.work1, "w2", nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("third child past a fan-out of 2: expected 403, got %d (%s)", w.Code, w.Body.String())
		}
		msg := responseError(t, w)
		for _, want := range []string{"fan-out", "2", SettingDelegationMaxFanout} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal must name %q; got %q", want, msg)
			}
		}

		// Mutation: one more slot and the same call is accepted.
		f.setLimit(t, SettingDelegationMaxFanout, 3)
		if w := f.assign(t, f.work1, "w2", nil); w.Code != http.StatusCreated {
			t.Fatalf("max_fanout=3 must accept the third child, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("lead in a chat counts only what is still in flight", func(t *testing.T) {
		f := setupDelegationFixture(t)
		f.setLimit(t, SettingDelegationMaxFanout, 2)
		// depth 1 because that is what insertCappedAssignment writes for a
		// root dispatch, and the bucket now counts only rows a capped door
		// wrote (`depth > 0`). A depth-0 row is a mission-engine row or a
		// pre-migration one; the fixture used to write those and was
		// simulating /assign output with rows /assign cannot produce.
		f.insertRootDispatch(t, "t1", f.lead, f.work1, "RUNNING")
		f.insertRootDispatch(t, "t2", f.lead, f.work2, "QUEUED")

		if w := f.assign(t, f.lead, "w1", nil); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 at fan-out 2, got %d (%s)", w.Code, w.Body.String())
		}

		// A finished dispatch frees the slot: a lead working a long chat is
		// capped on concurrency, not on lifetime output.
		if _, err := f.db.Exec(`UPDATE assignments SET status='COMPLETED' WHERE id='t1'`); err != nil {
			t.Fatalf("complete t1: %v", err)
		}
		if w := f.assign(t, f.lead, "w1", nil); w.Code != http.StatusCreated {
			t.Fatalf("expected 201 once a slot freed, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// The cap has to hold when the dispatches arrive together — which is the only
// way a fork bomb ever arrives. A read-then-write check admits all of them;
// the predicate rides inside the INSERT precisely so it cannot.
func TestDelegationCaps_FanoutHoldsUnderConcurrentDispatch(t *testing.T) {
	f := setupDelegationFixture(t)
	const limit = 3
	f.setLimit(t, SettingDelegationMaxFanout, limit)
	f.setLimit(t, SettingDelegationMaxDepth, 4)
	f.insertDelegatedAssignment(t, "parent", f.lead, f.work1, 1, "")

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.assign(t, f.work1, "w2", nil)
		}()
	}
	wg.Wait()
	f.h.WaitDispatches()

	var children int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM assignments WHERE parent_assignment_id = 'parent'`).Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if children > limit {
		t.Fatalf("%d children admitted past a fan-out of %d — the cap is a read-then-write race", children, limit)
	}
	if children == 0 {
		t.Fatal("no dispatch got through at all; the test proves nothing about the boundary")
	}
}

// max_depth = 0 turns delegation off entirely. An operator needs one number
// that means "no agent dispatches anything", and a cap whose floor is 1 cannot
// express it.
func TestDelegationCaps_ZeroDepthDisablesDelegation(t *testing.T) {
	f := setupDelegationFixture(t)
	f.setLimit(t, SettingDelegationMaxDepth, 0)

	if w := f.assign(t, f.lead, "w1", nil); w.Code != http.StatusForbidden {
		t.Fatalf("max_depth=0 must refuse even a lead's first dispatch, got %d (%s)", w.Code, w.Body.String())
	}
}

// Defaults are a shipped policy, not an implementation detail: they are what
// every instance that never touches a setting runs on.
func TestDelegationLimits_Defaults(t *testing.T) {
	db := setupTestDB(t)
	lim := DelegationLimits(context.Background(), db)
	if lim.MaxDepth != defaultDelegationMaxDepth || lim.MaxFanout != defaultDelegationMaxFanout {
		t.Fatalf("defaults = depth %d / fan-out %d, want %d / %d",
			lim.MaxDepth, lim.MaxFanout, defaultDelegationMaxDepth, defaultDelegationMaxFanout)
	}
	if defaultDelegationMaxDepth < 1 {
		t.Error("a default of 0 would ship delegation switched off")
	}
}
