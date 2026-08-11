package chain

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// Fixtures for the timing tests.
//
// These go through the PRODUCTION writers wherever one exists and can be
// imported, rather than through the raw-SQL helpers the rest of this file
// uses. That is a deliberate exception to the fixture rule at the top of
// chain_test.go, and it is specific to time.
//
// The other tests assert which ROWS the walk reaches, and for that a raw
// INSERT is the better fixture: it pins the schema rather than the convenience
// layer. Time is different, because the thing most likely to be wrong is not
// which row is read but what the STRING in the column looks like — and the
// only authority on that is the writer. chain_test.go's own seedRun stamps
// time.RFC3339Nano, which is NOT what pipeline.RunStore writes (it writes the
// fixed-width tsformat form), so a timing test built on it would pass while
// asserting a format production never produces.
//
// assignments is the one place a production writer cannot be reached: the
// transitions live in package api, which imports this package. Those fixtures
// therefore execute the production UPDATE statements VERBATIM, each citing the
// file and line it was copied from, which preserves the one property that
// matters here — the timestamp syntax.
// ---------------------------------------------------------------------------

// seedRealRun inserts a run the way the executor does: through
// pipeline.RunStore.Insert, so started_at carries the fixed-width tsformat
// string the store writes rather than a shape invented by the test.
func (r *rig) seedRealRun(t *testing.T, id, wsID, pipelineID, slug, via, byID string, startedAt time.Time) string {
	t.Helper()
	rec := &pipeline.RunRecord{
		ID:            id,
		WorkspaceID:   wsID,
		PipelineID:    pipelineID,
		PipelineSlug:  slug,
		Status:        pipeline.RunStatusRunning,
		StartedAt:     startedAt,
		TriggeredVia:  pipeline.TriggeredVia(via),
		TriggeredByID: byID,
	}
	if err := pipeline.NewRunStore(r.db).Insert(context.Background(), rec); err != nil {
		t.Fatalf("RunStore.Insert(%s): %v", id, err)
	}
	return id
}

// finishRealRun ends a run the way the executor does: through
// pipeline.RunStore.MarkTerminal, which is the only writer of ended_at.
func (r *rig) finishRealRun(t *testing.T, id string, endedAt time.Time, durationMs int64) {
	t.Helper()
	err := pipeline.NewRunStore(r.db).MarkTerminal(context.Background(), pipeline.MarkTerminalInput{
		RunID:      id,
		Status:     pipeline.RunStatusCompleted,
		EndedAt:    endedAt,
		DurationMs: durationMs,
	})
	if err != nil {
		t.Fatalf("RunStore.MarkTerminal(%s): %v", id, err)
	}
}

// startAssignmentDirect runs the statement internal/api/assignments_run.go:495
// runs when a dispatch goes straight to RUNNING (no crew-slot contention).
// Verbatim, including the RFC3339 second-precision stamp built at
// assignments_run.go:435.
func (r *rig) startAssignmentDirect(t *testing.T, id string, at time.Time) {
	t.Helper()
	now := at.UTC().Format(time.RFC3339)
	r.exec(t, `UPDATE assignments SET status='RUNNING', started_at=? WHERE id=?`, now, id)
}

// finishAssignmentDirect runs the statement internal/api/assignments_run.go:858
// runs on the terminal transition. Same RFC3339 stamp (assignments_run.go:840).
func (r *rig) finishAssignmentDirect(t *testing.T, id string, at time.Time) {
	t.Helper()
	now := at.UTC().Format(time.RFC3339)
	r.exec(t, `UPDATE assignments SET status='COMPLETED', finished_at=?
	           WHERE id=? AND status NOT IN ('COMPLETED','FAILED','CANCELLED')`, now, id)
}

// startAssignmentViaCrewSlot runs the stamp half of claimCrewSlot
// (internal/api/assignments_queue.go:126-130). The point of the fixture is the
// SYNTAX: this path writes SQLite's datetime('now','subsec') — "2026-08-10
// 09:41:02.317", with a space and no zone — while the direct path above writes
// RFC3339. Both are real rows in the same column.
func (r *rig) startAssignmentViaCrewSlot(t *testing.T, id string) {
	t.Helper()
	r.exec(t, `
		UPDATE assignments
		   SET status = 'RUNNING',
		       running_at = datetime('now','subsec'),
		       started_at = COALESCE(started_at, datetime('now','subsec'))
		 WHERE id = ?`, id)
}

// cancelAssignmentBeforeItStarted runs MissionEngine.cancelDeferredAssignment
// (internal/orchestrator/mission_tasks.go:605-610): a PENDING row is retired
// with finished_at set and started_at still NULL. This row is why a node's end
// cannot be reported without its beginning — the work never happened.
func (r *rig) cancelAssignmentBeforeItStarted(t *testing.T, id string, at time.Time) {
	t.Helper()
	now := at.UTC().Format(time.RFC3339)
	r.exec(t, `
		UPDATE assignments
		   SET status = 'CANCELLED', finished_at = ?, error_message = 'crew went away'
		 WHERE id = ? AND status = 'PENDING'`, now, id)
}

// seedRealInbox raises an inbox item through inbox.Insert, the writer every
// production caller goes through, so created_at is the tsformat string that
// writer stamps.
func (r *rig) seedRealInbox(t *testing.T, wsID, kind, sourceID, title string, payload map[string]any) string {
	t.Helper()
	if err := inbox.Insert(context.Background(), r.db, nil, inbox.Item{
		WorkspaceID: wsID,
		Kind:        kind,
		SourceID:    sourceID,
		Title:       title,
		Payload:     payload,
	}); err != nil {
		t.Fatalf("inbox.Insert(%s/%s): %v", kind, sourceID, err)
	}
	return "ibx_" + kind + "_" + sourceID
}

// ---------------------------------------------------------------------------
// Assertion helpers.
// ---------------------------------------------------------------------------

// assertNoTime is the NEGATIVE assertion, spelled once. Absent, never zero:
// a zero timestamp renders as 1970 and sorts to the top of every timeline,
// and a zero duration reads as "it was instant". Both are answers; the truth
// is that there is no answer.
func assertNoTime(t *testing.T, n Node, why string) {
	t.Helper()
	if n.OccurredAt != "" {
		t.Errorf("%s: occurred_at = %q, want absent — %s", n.ID, n.OccurredAt, why)
	}
	if n.EndedAt != "" {
		t.Errorf("%s: ended_at = %q, want absent — %s", n.ID, n.EndedAt, why)
	}
	if n.DurationMS != nil {
		t.Errorf("%s: duration_ms = %d, want absent (nil) — %s", n.ID, *n.DurationMS, why)
	}
}

// wantInstant compares an emitted instant against the time it should name,
// tolerating only the normalisation this package applies (UTC, fixed width).
func wantInstant(t *testing.T, got string, want time.Time, field string) {
	t.Helper()
	if got == "" {
		t.Fatalf("%s is absent, want %s", field, want.UTC().Format(time.RFC3339Nano))
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("%s = %q does not parse as RFC3339Nano: %v", field, got, err)
	}
	if d := parsed.Sub(want.UTC()); d > time.Second || d < -time.Second {
		t.Errorf("%s = %q, want %s (off by %v)", field, got, want.UTC().Format(time.RFC3339Nano), d)
	}
}

// ---------------------------------------------------------------------------
// run — pipeline_runs.started_at / ended_at.
// ---------------------------------------------------------------------------

// A run is an event: it began at started_at and stopped at ended_at, and the
// wall clock between them is how long it took.
//
// Asserted from three anchors because a field populated on one discovery path
// and missed on the others is the exact bug runColumns exists to prevent — the
// same trap TestWalk_RunNodeCarriesChainDepth covers for chain_depth.
func TestWalk_RunNodeCarriesWhenItRanAndHowLongItTook(t *testing.T) {
	r := newRig(t, "ws-time-run")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage", "run.failed", "triage", true)

	started := time.Date(2026, 8, 7, 9, 41, 2, 0, time.UTC)
	ended := started.Add(90 * time.Second)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1", started)
	r.finishRealRun(t, "run-1", ended, 90_000)

	for _, anchor := range []string{"run-1", "p1", "aut_1"} {
		g := walk(t, r, anchor, Options{MaxDepth: 4, MaxNodes: 100})
		n := nodeByID(t, g, "run:run-1")

		wantInstant(t, n.OccurredAt, started, "anchor "+anchor+": occurred_at")
		wantInstant(t, n.EndedAt, ended, "anchor "+anchor+": ended_at")
		if n.DurationMS == nil {
			t.Fatalf("anchor %s: duration_ms absent on a run that started and ended", anchor)
		}
		if *n.DurationMS != 90_000 {
			t.Errorf("anchor %s: duration_ms = %d, want 90000 (the wall clock between the two stamps)", anchor, *n.DurationMS)
		}
	}
}

// The fourth discovery path for a run, which the three anchors above cannot
// reach: expandIssue's join to pipeline_runs is the one run query that cannot
// use the runColumns constant (it needs the `r.` alias) and so is the one that
// can silently fall out of step with it.
func TestWalk_RunReachedFromItsIssueStillCarriesItsTime(t *testing.T) {
	r := newRig(t, "ws-time-run-from-issue")
	r.seedIssue(t, "m1", r.ws, "ENG-8", "Ship the thing")
	r.seedRoutine(t, "p1", r.ws, "deploy")
	started := time.Date(2026, 8, 7, 14, 0, 0, 0, time.UTC)
	r.seedRealRun(t, "run-1", r.ws, "p1", "deploy", "issue", "ENG-8", started)
	r.finishRealRun(t, "run-1", started.Add(2*time.Minute), 120_000)

	g := walk(t, r, "ENG-8", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "run:run-1")

	wantInstant(t, n.OccurredAt, started, "occurred_at")
	wantInstant(t, n.EndedAt, started.Add(2*time.Minute), "ended_at")
	if n.DurationMS == nil || *n.DurationMS != 120_000 {
		t.Errorf("duration_ms = %v, want 120000", n.DurationMS)
	}
}

// A run still going has a beginning and no end. Reporting ended_at as the zero
// string or duration as 0 would say "it finished, instantly" about work that is
// still happening.
//
// pipeline_runs.duration_ms makes this specifically dangerous: it is NOT NULL
// DEFAULT 0 and is rewritten at every step boundary
// (RunStore.UpsertStepOutput), so an in-flight run carries a non-zero value
// that measures the steps so far. Reading that column would put a completed
// span on a node that has not completed.
func TestWalk_RunStillInFlightReportsNoEndAndNoDuration(t *testing.T) {
	r := newRig(t, "ws-time-run-inflight")
	r.seedRoutine(t, "p1", r.ws, "triage")
	started := time.Date(2026, 8, 7, 9, 41, 2, 0, time.UTC)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "manual", "", started)
	// The mid-run cost/duration flush the executor performs per step.
	r.exec(t, `UPDATE pipeline_runs SET duration_ms = 12345 WHERE id = 'run-1'`)

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "run:run-1")

	wantInstant(t, n.OccurredAt, started, "occurred_at")
	if n.EndedAt != "" {
		t.Errorf("ended_at = %q on a run that has not ended, want absent", n.EndedAt)
	}
	if n.DurationMS != nil {
		t.Errorf("duration_ms = %d on a run that has not ended, want absent — the column's mid-run value is not a span", *n.DurationMS)
	}
}

// ---------------------------------------------------------------------------
// assignment — assignments.started_at / finished_at.
// ---------------------------------------------------------------------------

// Of the four timestamps on assignments, (started_at, finished_at) is the only
// pair that names when the work happened on every row that did work:
//
//   - queued_at is stamped ONLY when the crew budget was full
//     (markAssignmentQueued, assignments_queue.go:166). NULL on a row that ran
//     straight away, and on a row that did wait it names the moment work was
//     BLOCKED, not the moment it happened.
//   - running_at is stamped ONLY by the crew-slot CAS path
//     (assignments_queue.go:128, 201). The direct dispatch path
//     (assignments_run.go:495) never writes it, so it is NULL on a large class
//     of real rows.
//   - created_at is when the work was REQUESTED. The gap to started_at is queue
//     dwell, which belongs to the queue, not to the work.
//   - started_at is written by BOTH paths — directly at assignments_run.go:495,
//     and via COALESCE(started_at, datetime('now','subsec')) in both crew-slot
//     CAS statements — so it is the one stamp every row that ran carries.
func TestWalk_AssignmentTimeIsStartedToFinished(t *testing.T) {
	r := newRig(t, "ws-time-asg")
	r.seedAssignment(t, "asg-1", r.ws, "summarise the thread", "")

	started := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	finished := started.Add(45 * time.Second)
	// The queue path also stamps queued_at and running_at. Both are set here so
	// a reader that picked either would visibly disagree with the assertion.
	r.exec(t, `UPDATE assignments SET queued_at = ?, running_at = ? WHERE id = 'asg-1'`,
		started.Add(-20*time.Minute).Format(time.RFC3339),
		started.Add(-7*time.Minute).Format(time.RFC3339))
	r.startAssignmentDirect(t, "asg-1", started)
	r.finishAssignmentDirect(t, "asg-1", finished)

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "assignment:asg-1")

	wantInstant(t, n.OccurredAt, started, "occurred_at")
	wantInstant(t, n.EndedAt, finished, "ended_at")
	if n.DurationMS == nil {
		t.Fatalf("duration_ms absent on an assignment that started and finished")
	}
	if *n.DurationMS != 45_000 {
		t.Errorf("duration_ms = %d, want 45000", *n.DurationMS)
	}
}

// The same assignment reached from the issue that dispatched it, from the run
// that dispatched it, and from its own parent — one query per discovery path,
// and a column added to only some of them is the bug this covers.
func TestWalk_AssignmentTimeIsCarriedOnEveryDiscoveryPath(t *testing.T) {
	r := newRig(t, "ws-time-asg-paths")
	r.seedIssue(t, "m1", r.ws, "ENG-3", "investigate")
	r.seedRoutine(t, "p1", r.ws, "triage")
	started := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "manual", "", started)

	// Reached from the run (parent_run_id) and from the issue (mission_tasks).
	r.seedDispatchedAssignment(t, "asg-1", r.ws, "dispatched work", "run-1")
	r.seedMissionTask(t, "mt1", "m1", "asg-1")
	// And reached as a delegate of another assignment (parent_assignment_id).
	r.seedAssignment(t, "asg-2", r.ws, "delegated work", "asg-1")

	for _, id := range []string{"asg-1", "asg-2"} {
		r.startAssignmentDirect(t, id, started)
		r.finishAssignmentDirect(t, id, started.Add(30*time.Second))
	}

	for _, anchor := range []string{"run-1", "ENG-3", "asg-1", "asg-2"} {
		g := walk(t, r, anchor, Options{MaxDepth: 4, MaxNodes: 100})
		for _, id := range []string{"assignment:asg-1", "assignment:asg-2"} {
			n := nodeByID(t, g, id)
			wantInstant(t, n.OccurredAt, started, "anchor "+anchor+" "+id+": occurred_at")
			if n.DurationMS == nil || *n.DurationMS != 30_000 {
				t.Errorf("anchor %s %s: duration_ms = %v, want 30000", anchor, id, n.DurationMS)
			}
		}
	}
}

// assignments carries TWO timestamp syntaxes in the same column, because two
// production paths write it: RFC3339 from Go (assignments_run.go) and SQLite's
// datetime('now','subsec') — "2026-08-10 09:41:02.317", space-separated and
// unzoned — from the crew-slot CAS (assignments_queue.go).
//
// A reader that only knows RFC3339 drops every assignment that ever contended
// for a crew slot, silently, and those are the busiest ones. The emitted value
// must also come back normalised, because a client doing `new Date(s)` on the
// space form gets a LOCAL-time reading in V8 and an Invalid Date in others —
// the node would land hours away from where it belongs, or nowhere.
func TestWalk_AssignmentStampedBySQLiteDatetimeIsStillReadable(t *testing.T) {
	r := newRig(t, "ws-time-asg-sqlite")
	r.seedAssignment(t, "asg-1", r.ws, "contended for a slot", "")
	before := time.Now().UTC().Add(-2 * time.Second)
	r.startAssignmentViaCrewSlot(t, "asg-1")

	// Confirm the fixture really wrote the space form; otherwise this test
	// proves nothing about the syntax it exists for.
	var raw string
	if err := r.db.QueryRow(`SELECT started_at FROM assignments WHERE id = 'asg-1'`).Scan(&raw); err != nil {
		t.Fatalf("read back started_at: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		t.Fatalf("test premise broken: the crew-slot path now writes RFC3339 (%q); this test no longer covers the space form", raw)
	}

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "assignment:asg-1")

	if n.OccurredAt == "" {
		t.Fatalf("occurred_at absent for started_at %q — every assignment that queued behind a full crew is missing from the timeline", raw)
	}
	parsed, err := time.Parse(time.RFC3339Nano, n.OccurredAt)
	if err != nil {
		t.Fatalf("occurred_at = %q is not RFC3339Nano; a client cannot place it", n.OccurredAt)
	}
	if parsed.Before(before) || parsed.After(time.Now().UTC().Add(2*time.Second)) {
		t.Errorf("occurred_at = %q is not the moment the slot was claimed (%v) — a timezone was invented", n.OccurredAt, before)
	}
}

// An assignment cancelled before it ever started has finished_at and no
// started_at (MissionEngine.cancelDeferredAssignment). It did no work, so it
// has no place on a timeline.
//
// The end is withheld along with the beginning, deliberately. A node with an
// end and no beginning is not a shorter bar, it is an unplaceable one: a
// renderer either drops it or anchors it at zero, and zero is 1970.
func TestWalk_AssignmentCancelledBeforeItStartedCarriesNoTime(t *testing.T) {
	r := newRig(t, "ws-time-asg-cancelled")
	r.seedAssignment(t, "asg-1", r.ws, "never got a crew", "")
	r.cancelAssignmentBeforeItStarted(t, "asg-1", time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC))

	// Precondition: the row really is end-without-beginning.
	var started, finished any
	if err := r.db.QueryRow(`SELECT started_at, finished_at FROM assignments WHERE id = 'asg-1'`).
		Scan(&started, &finished); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if started != nil {
		t.Fatalf("test premise broken: started_at = %v, want NULL", started)
	}
	if finished == nil {
		t.Fatal("test premise broken: finished_at is NULL, so this row is not the end-without-beginning case")
	}

	g := walk(t, r, "asg-1", Options{MaxDepth: 4, MaxNodes: 100})
	assertNoTime(t, nodeByID(t, g, "assignment:asg-1"),
		"an assignment that was cancelled before it started did no work, so it has no when and no how-long")
}

// ---------------------------------------------------------------------------
// inbox — inbox_items.created_at.
// ---------------------------------------------------------------------------

// An inbox item is an event, not a noun: the row is written at the instant of
// the thing it reports (inbox.Insert stamps created_at then), so created_at IS
// when it happened.
//
// It carries no duration. resolved_at exists, but "how long a human took to
// answer" is a different measurement from "how long the work took", and giving
// them one field name puts two incomparable bars on one axis.
func TestWalk_InboxNodeCarriesWhenItWasRaisedAndNoDuration(t *testing.T) {
	r := newRig(t, "ws-time-inbox")
	r.seedRoutine(t, "p1", r.ws, "deploy")
	r.seedRealRun(t, "run-1", r.ws, "p1", "deploy", "manual", "", time.Now().UTC())
	before := time.Now().UTC().Add(-2 * time.Second)
	id := r.seedRealInbox(t, r.ws, "failed_run", "run-1", "deploy failed", map[string]any{"run_id": "run-1"})

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})
	n := nodeByID(t, g, "inbox:"+id)

	if n.OccurredAt == "" {
		t.Fatal("occurred_at absent: an inbox item is raised at a knowable instant")
	}
	parsed, err := time.Parse(time.RFC3339Nano, n.OccurredAt)
	if err != nil {
		t.Fatalf("occurred_at = %q is not RFC3339Nano", n.OccurredAt)
	}
	if parsed.Before(before) {
		t.Errorf("occurred_at = %q predates the insert (%v)", n.OccurredAt, before)
	}
	if n.EndedAt != "" {
		t.Errorf("ended_at = %q: an inbox item has no span; resolved_at measures a human, not the work", n.EndedAt)
	}
	if n.DurationMS != nil {
		t.Errorf("duration_ms = %d: an inbox item has no span", *n.DurationMS)
	}
}

// ---------------------------------------------------------------------------
// The kinds that cannot answer.
// ---------------------------------------------------------------------------

// issue, routine, agent and automation are NOUNS. Each of their tables has a
// created_at, and every one of them would be a plausible-looking lie:
//
//   - missions.created_at is when the issue was FILED. The issue does not
//     "happen" then; it spans the whole chain, and the things that happened
//     inside it are the runs and assignments, which carry their own times.
//   - pipelines.created_at is when somebody WROTE the routine, which can be
//     months before the run in this chain.
//   - agents.created_at is when the agent was hired.
//   - automations.created_at is when the rule was written. When the rule FIRED
//     is the started_at of the run it caused, and that run node carries it.
//
// Putting any of those on a timeline moves a node to a moment it has nothing to
// do with, and the reader cannot tell — which is the whole failure mode this
// package exists to avoid. Absent is the honest answer.
func TestWalk_NounKindsCarryNoTimeAtAll(t *testing.T) {
	r := newRig(t, "ws-time-nouns")
	r.seedIssue(t, "m1", r.ws, "ENG-5", "Ship the thing")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.exec(t, `UPDATE missions SET routine_id = 'p1' WHERE id = 'm1'`)
	r.seedAutomation(t, "aut_1", r.ws, "Triage", "run.failed", "triage", true)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "automation", "aut_1", time.Now().UTC())
	r.seedAssignment(t, "asg-1", r.ws, "work", "")
	r.seedMissionTask(t, "mt1", "m1", "asg-1")

	g := walk(t, r, "ENG-5", Options{MaxDepth: 6, MaxNodes: 100})

	for _, tc := range []struct{ id, why string }{
		{"issue:m1", "missions.created_at is when the issue was filed, not when the issue happened"},
		{"routine:p1", "pipelines.created_at is when the routine was written, not when it ran"},
		{"agent:" + r.agent, "agents.created_at is when the agent was hired"},
		{"automation:aut_1", "automations.created_at is when the rule was written; when it fired is the run's started_at"},
	} {
		assertNoTime(t, nodeByID(t, g, tc.id), tc.why)
	}
}

// The same, from the automation's own anchor — a kind that reports nothing on
// one discovery path and a created_at on another is the same silent lie.
func TestWalk_AutomationAnchorCarriesNoTimeEither(t *testing.T) {
	r := newRig(t, "ws-time-aut-anchor")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedAutomation(t, "aut_1", r.ws, "Triage", "run.failed", "triage", true)

	g := walk(t, r, "aut_1", Options{MaxDepth: 4, MaxNodes: 100})
	assertNoTime(t, nodeByID(t, g, "automation:aut_1"),
		"a rule's created_at is not when anything in this chain happened")
}

// ---------------------------------------------------------------------------
// The fence.
// ---------------------------------------------------------------------------

// Every new column is read through an existing workspace-scoped query, and the
// enumerating ones are where a dropped predicate does real damage: a run lists
// assignment ROWS rather than dereferencing one id, so a missing fence puts
// another tenant's work — and now its timestamps — inside this graph.
//
// The foreign rows are stamped in 1999 so a leak is unmistakable in the
// failure message rather than being a plausible-looking time.
func TestWalk_TimingNeverCrossesTheWorkspaceFence(t *testing.T) {
	r := newRig(t, "ws-time-fence")
	r.seedRoutine(t, "p1", r.ws, "triage")
	mine := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "manual", "", mine)

	r.seedWorkspace(t, "ws-other-tenant", "other-tenant")
	theirs := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	r.exec(t, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task,
		                         status, parent_run_id, started_at, finished_at)
		VALUES ('asg-theirs', 'ws-other-tenant', ?, ?, ?, 'their work', 'COMPLETED', 'run-1', ?, ?)`,
		r.chat, r.agent, r.agent,
		theirs.Format(time.RFC3339), theirs.Add(time.Hour).Format(time.RFC3339))
	r.seedDispatchedAssignment(t, "asg-mine", r.ws, "my work", "run-1")
	r.startAssignmentDirect(t, "asg-mine", mine)

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	for _, n := range g.Nodes {
		if n.OccurredAt != "" && n.OccurredAt < "2000" {
			t.Errorf("node %s carries occurred_at %q — a timestamp from ws-other-tenant is in this graph", n.ID, n.OccurredAt)
		}
	}
	// And the row that legitimately belongs here still reports its time, so the
	// assertion above cannot be satisfied by returning nothing at all.
	wantInstant(t, nodeByID(t, g, "assignment:asg-mine").OccurredAt, mine, "asg-mine occurred_at")
}

// ---------------------------------------------------------------------------
// Normalisation.
// ---------------------------------------------------------------------------

// Every instant on the wire is one syntax, whatever the column held. Four
// writers with three formats feed these columns; a client sorting or plotting
// them must not have to know that.
func TestWalk_EmittedInstantsAreOneNormalisedUTCForm(t *testing.T) {
	r := newRig(t, "ws-time-norm")
	r.seedRoutine(t, "p1", r.ws, "triage")
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "manual", "", time.Now().UTC())
	r.finishRealRun(t, "run-1", time.Now().UTC().Add(time.Second), 1000)
	r.seedDispatchedAssignment(t, "asg-1", r.ws, "work", "run-1")
	r.startAssignmentViaCrewSlot(t, "asg-1") // space-form stamp
	r.seedRealInbox(t, r.ws, "failed_run", "run-1", "failed", map[string]any{"run_id": "run-1"})

	g := walk(t, r, "run-1", Options{MaxDepth: 4, MaxNodes: 100})

	var seen int
	for _, n := range g.Nodes {
		for field, v := range map[string]string{"occurred_at": n.OccurredAt, "ended_at": n.EndedAt} {
			if v == "" {
				continue
			}
			seen++
			if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
				t.Errorf("%s.%s = %q does not parse as RFC3339Nano: %v", n.ID, field, v, err)
			}
			if v[len(v)-1] != 'Z' {
				t.Errorf("%s.%s = %q is not normalised to UTC", n.ID, field, v)
			}
		}
	}
	if seen < 4 {
		t.Fatalf("only %d instants emitted; this test is not exercising the formats it claims to", seen)
	}
}

// ---------------------------------------------------------------------------
// The derivation itself.
// ---------------------------------------------------------------------------

func TestNormaliseInstant(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"rfc3339 seconds (assignments_run.go)", "2026-08-07T09:41:02Z", "2026-08-07T09:41:02.000000000Z"},
		{"fixed-width tsformat (pipeline.RunStore)", "2026-08-07T09:41:02.500000000Z", "2026-08-07T09:41:02.500000000Z"},
		{"sqlite datetime('now','subsec')", "2026-08-07 09:41:02.317", "2026-08-07T09:41:02.317000000Z"},
		{"sqlite datetime('now')", "2026-08-07 09:41:02", "2026-08-07T09:41:02.000000000Z"},
		{"offset is normalised to UTC", "2026-08-07T11:41:02+02:00", "2026-08-07T09:41:02.000000000Z"},
		{"garbage", "not a time", ""},
		{"a zero time is not a time", "0001-01-01T00:00:00Z", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normaliseInstant(tc.in); got != tc.want {
				t.Errorf("normaliseInstant(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSpanMS(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name, start, end string
		want             *int64
	}{
		{"ordinary span", "2026-08-07T09:00:00Z", "2026-08-07T09:01:30Z", ms(90_000)},
		{"sub-millisecond but finished", "2026-08-07T09:00:00.000000000Z", "2026-08-07T09:00:00.000100000Z", ms(0)},
		{"no end yet", "2026-08-07T09:00:00Z", "", nil},
		{"no start", "", "2026-08-07T09:00:00Z", nil},
		{"neither", "", "", nil},
		{"end before start", "2026-08-07T09:01:00Z", "2026-08-07T09:00:00Z", nil},
		{"unparseable end", "2026-08-07T09:00:00Z", "whenever", nil},
		{"mixed syntaxes still subtract", "2026-08-07 09:00:00.000", "2026-08-07T09:00:01Z", ms(1_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := spanMS(tc.start, tc.end)
			switch {
			case got == nil && tc.want == nil:
			case got == nil || tc.want == nil:
				t.Fatalf("spanMS(%q, %q) = %v, want %v", tc.start, tc.end, got, tc.want)
			case *got != *tc.want:
				t.Errorf("spanMS(%q, %q) = %d, want %d", tc.start, tc.end, *got, *tc.want)
			}
		})
	}
}

// A duration of 0 is a real answer and must survive JSON: a run that finished
// inside a millisecond took 0ms, which is different from a run whose duration
// is unknown. Only a pointer can carry that distinction, and only if omitempty
// is not applied to the value.
func TestWalk_ZeroDurationIsEmittedNotOmitted(t *testing.T) {
	r := newRig(t, "ws-time-zero")
	r.seedRoutine(t, "p1", r.ws, "triage")
	at := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	r.seedRealRun(t, "run-1", r.ws, "p1", "triage", "manual", "", at)
	r.finishRealRun(t, "run-1", at.Add(100*time.Microsecond), 0)

	g := walk(t, r, "run-1", Options{MaxDepth: 2, MaxNodes: 100})
	n := nodeByID(t, g, "run:run-1")

	if n.DurationMS == nil {
		t.Fatal("duration_ms absent on a run that finished in under a millisecond; 0 is the answer, not 'unknown'")
	}
	if *n.DurationMS != 0 {
		t.Errorf("duration_ms = %d, want 0", *n.DurationMS)
	}
	if n.EndedAt == "" {
		t.Error("ended_at absent on a finished run")
	}
}
