package api

// Tests for B3: one active turn per session (PRD-ISSUES-AND-ROUTINES-2026
// §9.4/§17, work package B3, #2339). The accept line:
//
//	"two comments 2s apart produce one run and two consumed deliveries;
//	 the index rejects a concurrent second insert at the DB level; no
//	 TOCTOU window between session lookup and insert."
//
// This file proves the DB-level and insert-path halves directly against
// resolveSessionAndInsertAssignment/insertCappedAssignment
// (delegation_limits.go) and the raw idx_assignments_one_active_per_session
// constraint. The delivery-consumption half of the accept line
// ("two consumed deliveries") is proven separately, at the mention-recorder
// level, in issue_mentions_session_busy_test.go.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// sessionExclusivityCaller/scope/lim mirror exactly what DispatchMention
// builds for a HUMAN mention (issue_mentions.go): no acting agent, so the
// row is filed under the target (dispatchCaller.selfFiled()), depth 1 (a
// human mention is a root), and a generous fan-out ceiling so these tests
// isolate the SESSION exclusivity index from the unrelated fan-out cap.
func sessionExclusivityCaller(targetID string) dispatchCaller {
	return dispatchCaller{ActorAgentID: "", FanoutSubjectID: targetID}
}

var sessionExclusivityScope = delegationScope{Depth: 1}
var sessionExclusivityLim = delegationLimits{MaxDepth: 5, MaxFanout: 50}

// TestSessionExclusivity_SecondInsertAttachesToActiveRun is the B3 accept
// line's "the index rejects a concurrent second insert at the DB level"
// half, driven through the actual Go entry point (resolveSessionAndInsertAssignment)
// rather than raw SQL: a second resolve-or-create-then-insert for the SAME
// (mission, agent) session, while the first assignment is still
// PENDING, must not write a second row — it must report the FIRST
// assignment's id instead, with attached=true, and no error.
func TestSessionExclusivity_SecondInsertAttachesToActiveRun(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	caller := sessionExclusivityCaller(f.target)

	a := cappedAssignment{
		WorkspaceID: f.wsID,
		ChatID:      f.missionID,
		TargetID:    f.target,
		Task:        "first mention",
		GroupID:     f.missionID,
		CreatedAt:   "2026-09-04T00:00:00Z",
		MissionID:   f.missionID,
	}

	id1, attached1, sid1, err := resolveSessionAndInsertAssignment(
		ctx, f.db, f.assign.logger, f.wsID, f.missionID, f.target,
		sessionExclusivityScope, sessionExclusivityLim, caller, a)
	if err != nil {
		t.Fatalf("first resolveSessionAndInsertAssignment: %v", err)
	}
	if sid1 == "" {
		t.Fatal("first call returned an empty session id")
	}
	if attached1 {
		t.Fatal("first call reported attached=true — nothing was running yet")
	}
	if id1 == "" {
		t.Fatal("first call returned an empty assignment id")
	}

	var status string
	if err := f.db.QueryRow(`SELECT status FROM assignments WHERE id = ?`, id1).Scan(&status); err != nil {
		t.Fatalf("read first assignment status: %v", err)
	}
	if status != "PENDING" {
		t.Fatalf("first assignment status = %q, want PENDING (still in flight for this test)", status)
	}

	a.Task = "second mention, 2s later"
	id2, attached2, sid2, err := resolveSessionAndInsertAssignment(
		ctx, f.db, f.assign.logger, f.wsID, f.missionID, f.target,
		sessionExclusivityScope, sessionExclusivityLim, caller, a)
	if err != nil {
		t.Fatalf("second resolveSessionAndInsertAssignment surfaced a raw error instead of attaching: %v", err)
	}
	if sid2 != sid1 {
		t.Fatalf("second call's session id = %q, want the same session %q", sid2, sid1)
	}
	if !attached2 {
		t.Fatal("second call reported attached=false — it inserted a SECOND live run for the same session")
	}
	if id2 != id1 {
		t.Fatalf("second call's id = %q, want the first run's id %q", id2, id1)
	}

	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE mission_id = ?`, f.missionID).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if n != 1 {
		t.Fatalf("assignments for this mission = %d, want 1 — one run, not two", n)
	}

	// The follow-up's brief reached the run that IS live (sessionBusyErrorFor's
	// task-append — see its doc comment for why this, not chatbridge.Steer,
	// is B3's answer to "the existing steering queue").
	var task string
	if err := f.db.QueryRow(`SELECT task FROM assignments WHERE id = ?`, id1).Scan(&task); err != nil {
		t.Fatalf("read active run's task: %v", err)
	}
	if !strings.Contains(task, "second mention, 2s later") {
		t.Errorf("active run's task does not contain the follow-up brief: %q", task)
	}
}

// TestSessionExclusivity_TerminalRunFreesTheSlot proves the index is
// partial: once the first run reaches a terminal status, the session's
// slot is free again and a new mention starts a genuinely new run rather
// than being folded into a dead one.
func TestSessionExclusivity_TerminalRunFreesTheSlot(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	caller := sessionExclusivityCaller(f.target)

	a := cappedAssignment{
		WorkspaceID: f.wsID, ChatID: f.missionID, TargetID: f.target,
		Task: "first", GroupID: f.missionID, CreatedAt: "2026-09-04T00:00:00Z", MissionID: f.missionID,
	}
	id1, attached1, sid1, err := resolveSessionAndInsertAssignment(
		ctx, f.db, f.assign.logger, f.wsID, f.missionID, f.target,
		sessionExclusivityScope, sessionExclusivityLim, caller, a)
	if err != nil || attached1 {
		t.Fatalf("first insert: id=%q attached=%v err=%v", id1, attached1, err)
	}
	if sid1 == "" {
		t.Fatal("first call returned an empty session id")
	}

	if _, err := f.db.Exec(`UPDATE assignments SET status = 'COMPLETED' WHERE id = ?`, id1); err != nil {
		t.Fatalf("mark first run terminal: %v", err)
	}

	a.Task = "second, after the first finished"
	id2, attached2, sid2, err := resolveSessionAndInsertAssignment(
		ctx, f.db, f.assign.logger, f.wsID, f.missionID, f.target,
		sessionExclusivityScope, sessionExclusivityLim, caller, a)
	if err != nil {
		t.Fatalf("second resolveSessionAndInsertAssignment: %v", err)
	}
	if sid2 != sid1 {
		t.Fatalf("second call's session id = %q, want the SAME session %q (a terminal run does not rotate the session)", sid2, sid1)
	}
	if attached2 {
		t.Fatal("second call reported attached=true even though the first run is COMPLETED")
	}
	if id2 == id1 || id2 == "" {
		t.Fatalf("second call's id = %q, want a fresh id distinct from %q", id2, id1)
	}

	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE mission_id = ?`, f.missionID).Scan(&n); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if n != 2 {
		t.Fatalf("assignments for this mission = %d, want 2 — a terminal run does not block a new one", n)
	}
}

// TestSessionExclusivity_ConcurrentInsertsOnlyOneWins is the B3 accept
// line's "no TOCTOU window between session lookup and insert" half: many
// goroutines race resolveSessionAndInsertAssignment for the SAME (mission,
// agent) session at once. Exactly one may insert a fresh PENDING row; every
// other caller must report attached=true naming that SAME winner — never a
// second row, and never a raw constraint error escaping to the caller. Run
// with -race.
func TestSessionExclusivity_ConcurrentInsertsOnlyOneWins(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	caller := sessionExclusivityCaller(f.target)

	const n = 12
	ids := make([]string, n)
	attached := make([]bool, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			a := cappedAssignment{
				WorkspaceID: f.wsID, ChatID: f.missionID, TargetID: f.target,
				Task: "concurrent mention", GroupID: f.missionID,
				CreatedAt: "2026-09-04T00:00:00Z", MissionID: f.missionID,
			}
			id, att, _, err := resolveSessionAndInsertAssignment(
				context.Background(), f.db, f.assign.logger, f.wsID, f.missionID, f.target,
				sessionExclusivityScope, sessionExclusivityLim, caller, a)
			ids[i] = id
			attached[i] = att
			errs[i] = err
		}(i)
	}
	wg.Wait()

	var winners, losers int
	var winnerID string
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("call %d returned an error instead of attaching: %v", i, errs[i])
			continue
		}
		if ids[i] == "" {
			t.Errorf("call %d returned an empty id", i)
			continue
		}
		if attached[i] {
			losers++
		} else {
			winners++
			winnerID = ids[i]
		}
	}
	if winners != 1 {
		t.Fatalf("winners (fresh inserts) = %d, want exactly 1", winners)
	}
	if losers != n-1 {
		t.Fatalf("losers (attached to the active run) = %d, want %d", losers, n-1)
	}
	for i := 0; i < n; i++ {
		if attached[i] && ids[i] != winnerID {
			t.Errorf("call %d attached to %q, want the winner %q", i, ids[i], winnerID)
		}
	}

	var rows int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE mission_id = ?`, f.missionID).Scan(&rows); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if rows != 1 {
		t.Fatalf("assignments for this mission = %d, want 1 — %d concurrent callers must collapse to one run", rows, n)
	}
}

// TestRawIndex_RejectsConcurrentSecondInsert proves the constraint itself,
// independent of the Go wrapper above: a second raw INSERT naming the same
// session_id in a non-terminal status fails at the SQLite level with
// idx_assignments_one_active_per_session, and a terminal-status row never
// collides.
func TestRawIndex_RejectsConcurrentSecondInsert(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}

	insert := func(id, status string) error {
		_, err := f.db.Exec(`
			INSERT INTO assignments
			    (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
			     depth, created_at, mission_id, session_id)
			VALUES (?, ?, ?, ?, ?, 'raw index test', ?, 1, datetime('now'), ?, 'sess_raw_1')`,
			id, f.wsID, f.missionID, f.target, f.target, status, f.missionID)
		return err
	}

	// A session row is required for the FK: session_id references
	// issue_agent_sessions(id).
	if _, err := f.db.Exec(`
		INSERT INTO issue_agent_sessions
		    (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES ('sess_raw_1', ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		f.wsID, f.missionID, f.target); err != nil {
		t.Fatalf("seed session row: %v", err)
	}

	if err := insert("a_raw_1", "PENDING"); err != nil {
		t.Fatalf("first insert (PENDING): %v", err)
	}
	err := insert("a_raw_2", "RUNNING")
	if err == nil {
		t.Fatal("second insert for the same session, both non-terminal, succeeded — the exclusivity index did not fire")
	}
	// modernc.org/sqlite reports a partial-unique-index violation as
	// "UNIQUE constraint failed: <table>.<column>" — the indexed column,
	// never the index's own name (verified directly; see
	// isSessionExclusivityErr's doc comment in delegation_limits.go).
	if !strings.Contains(err.Error(), "UNIQUE constraint failed") || !strings.Contains(err.Error(), "assignments.session_id") {
		t.Errorf("second insert failed, but not on the exclusivity index: %v", err)
	}

	if _, err := f.db.Exec(`UPDATE assignments SET status = 'COMPLETED' WHERE id = 'a_raw_1'`); err != nil {
		t.Fatalf("terminate first row: %v", err)
	}
	if err := insert("a_raw_3", "PENDING"); err != nil {
		t.Fatalf("insert after the prior holder went terminal: %v", err)
	}
}

// TestDispatchMention_SessionBusyErrorNamesTheRealSession is a regression
// test for a bug caught in review on #2342: DispatchMention's "attached"
// branch synthesized its own *sessionBusyError with SessionID left "",
// instead of using the session id resolveSessionAndInsertAssignment had
// already resolved — so the message persisted to
// mission_comment_mentions.dispatch_detail read "session  already has an
// active run (<id>)" with a blank id where the session id belongs.
//
// Drives the REAL DispatchMention entry point (not resolveSessionAndInsertAssignment
// directly, and not a stub) — the bug lived in the glue between that
// function's return values and the error DispatchMention builds around
// them, which only exercising DispatchMention itself can catch. The
// session/run pair is seeded directly rather than raced by two DispatchMention
// calls, so the busy path is hit deterministically instead of depending on
// whether a background runAssignment goroutine has finished yet.
func TestDispatchMention_SessionBusyErrorNamesTheRealSession(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()

	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions
		    (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES ('sess_busy_detail', ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		f.wsID, f.missionID, f.target)
	execOrFatal(t, f.db, `
		INSERT INTO assignments
		    (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES ('asg_busy_detail', ?, ?, ?, ?, 'already running', 'RUNNING', 1, datetime('now'), ?, 'sess_busy_detail')`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)

	_, err := f.assign.DispatchMention(ctx, mentionDispatchRequest{
		WorkspaceID:   f.wsID,
		MissionID:     f.missionID,
		Identifier:    f.ident,
		IssueTitle:    "Test issue",
		IssueCrewID:   f.crewID,
		CommentID:     "cmt_busy_detail",
		CommentBody:   "follow-up",
		AuthorType:    "user",
		AuthorID:      f.userID,
		TargetAgentID: f.target,
	})
	f.assign.WaitDispatches()

	var busy *sessionBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("DispatchMention error = %v, want a *sessionBusyError", err)
	}
	if busy.SessionID != "sess_busy_detail" {
		t.Errorf("sessionBusyError.SessionID = %q, want %q (the real session, not blank)", busy.SessionID, "sess_busy_detail")
	}
	if busy.ActiveAssignmentID != "asg_busy_detail" {
		t.Errorf("sessionBusyError.ActiveAssignmentID = %q, want %q", busy.ActiveAssignmentID, "asg_busy_detail")
	}
	if strings.Contains(busy.Error(), "session  already") {
		t.Errorf("error text has a blank session id (double space): %q", busy.Error())
	}
}
