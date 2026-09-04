package pipeline

// Golden-scenario tests for the recurring-work engine (PRD-ISSUES-AND-
// ROUTINES-2026.md §18, scenarios 11-12, plus the bonus circuit-breaker
// scenario). Unlike the rest of this package's schedule tests — which run
// against openScheduleTestDB, a hand-maintained schema fixture
// (schedules_test.go's scheduleSchemaSQL) — these run against
// testutil.MigratedSQLDB: the REAL migration chain, exactly what a
// production server runs. That is the point. A query of the live dev
// clone on 2026-09-01 found pipeline_schedules at 0 rows, pipeline_webhooks
// at 0 rows, automations at 0 rows: this scheduling/catch-up/wake-gate
// machinery is well-built and, as far as anyone can prove, has never
// executed once outside a unit test. These tests are the first evidence
// either way, driven end-to-end through the production schema + store +
// scheduler + executor, asserting on the real pipeline_runs table rather
// than a mock runner's call count.
//
// Clock note: PipelineScheduler.now() (schedules.go) IS injectable via the
// unexported nowFn field, but listDueSchedules (the "is this row due"
// filter) compares next_run_at against a fresh time.Now() every time —
// it is NOT injectable. So "no sleeping" here means: seed next_run_at
// (and any other due-timestamps) as real-clock offsets taken once per
// test, then invoke tick()/fireOne() immediately and synchronously. No
// call in this file sleeps, polls, or waits on wall-clock elapse; every
// due/missed-occurrence computation is anchored to a single time.Now()
// read at the top of the test. This is the same pattern the pre-existing
// schedules_catchup_test.go uses (proven not to flake).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

const goldenScheduleWS = "ws_golden_sched"

// newGoldenScheduleDB brings up a fully-migrated DB (production schema,
// v80+) with one workspace seeded so pipeline/schedule FK constraints
// (foreign_keys=ON, same as production) are satisfiable.
func newGoldenScheduleDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Golden', 'golden-sched')`, goldenScheduleWS); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return db
}

// goldenSeedAgentPipeline seeds a one-step agent_run pipeline (fires
// through the real AgentRunner interface via the mockRunner below) —
// the same shape internal/pipeline/schedules_test.go uses for its main
// (non-wake) fixtures.
func goldenSeedAgentPipeline(t *testing.T, db *sql.DB, id, slug, agentSlug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := fmt.Sprintf(`{"name":%q,"steps":[{"id":"s1","type":"agent_run","agent":%q,"prompt":"hi"}]}`, slug, agentSlug)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at, last_test_run_passed)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, ?, ?, 1)`,
		id, goldenScheduleWS, slug, slug, def, now, now, now); err != nil {
		t.Fatalf("seed pipeline %s: %v", slug, err)
	}
}

// goldenSeedTransformPipeline seeds an agentless probe pipeline whose
// single transform step deterministically echoes `output` — no
// AgentRunner call, so wake-probe tests never depend on mockRunner
// queue state.
func goldenSeedTransformPipeline(t *testing.T, db *sql.DB, id, slug, output string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	def := fmt.Sprintf(
		`{"dsl_version":"1.0","name":%q,"agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":%q,"expression":"."}}]}`,
		slug, output)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at, last_test_run_passed)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, ?, ?, 1)`,
		id, goldenScheduleWS, slug, slug, def, now, now, now); err != nil {
		t.Fatalf("seed pipeline %s: %v", slug, err)
	}
}

// goldenScheduleRow is the full column set of pipeline_schedules
// (production schema, v80+v115+v151+v153+v162) so every golden test
// controls every field explicitly rather than relying on defaults that
// could silently drift from what Save() would have produced.
type goldenScheduleRow struct {
	ID                     string
	TargetPipelineID       string
	CronExpr               string
	NextRunAt              time.Time
	CatchupPolicy          string
	WakePipelineID         string
	WakeFailClosed         bool
	MaxConsecutiveFailures int
	ConsecutiveFailures    int
}

func goldenInsertSchedule(t *testing.T, db *sql.DB, r goldenScheduleRow) {
	t.Helper()
	if r.CatchupPolicy == "" {
		r.CatchupPolicy = CatchupOnce
	}
	if r.MaxConsecutiveFailures == 0 {
		r.MaxConsecutiveFailures = defaultMaxConsecutiveFailures
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled,
   next_run_at, catchup_policy, wake_pipeline_id, wake_fail_closed,
   max_consecutive_failures, consecutive_failures, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 'UTC', '{}', 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, goldenScheduleWS, r.ID, r.TargetPipelineID, r.CronExpr,
		tsformatForTest(r.NextRunAt), r.CatchupPolicy, nullIfEmpty(r.WakePipelineID), boolToInt(r.WakeFailClosed),
		r.MaxConsecutiveFailures, r.ConsecutiveFailures, now, now)
	if err != nil {
		t.Fatalf("insert schedule %s: %v", r.ID, err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newGoldenRig(t *testing.T) (*sql.DB, *ScheduleStore, *PipelineScheduler) {
	t.Helper()
	db := newGoldenScheduleDB(t)
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	// WithRunStore is what actually makes a fire land a pipeline_runs
	// row — a plain NewExecutor() (the shape every pre-existing
	// schedule test in this package uses) does NOT persist there at
	// all, which is why those tests only ever assert on the mock
	// runner's call count or the schedule store's own fields. That is
	// a real gap: it means the pre-existing suite has never actually
	// proven a cron fire reaches pipeline_runs end-to-end. Production
	// (cmd/crewship/cmd_start.go) always wires RunStore via
	// NewWiredExecutor; mirror that here so these golden tests check
	// the same table an operator/CLI would query.
	exec := NewExecutor(pipelines, NewResolver(db), newMockRunner(), nil).WithRunStore(NewRunStore(db))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db, store, NewPipelineScheduler(store, pipelines, exec, logger)
}

func goldenRunCount(t *testing.T, db *sql.DB, pipelineID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = ?`, pipelineID).Scan(&n); err != nil {
		t.Fatalf("count pipeline_runs: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Scenario 11 — a schedule fires, and a wake gate can suppress it.
// ---------------------------------------------------------------------------

// TestGolden11_DueSchedule_FiresExactlyOneRun proves the base case: a
// schedule whose next_run_at has passed fires through
// tick -> listDueSchedules -> fireOne -> fireSingleOccurrence and leaves
// EXACTLY one row in pipeline_runs, with the schedule's own telemetry
// (last_status/last_run_id) matching it.
func TestGolden11_DueSchedule_FiresExactlyOneRun(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_main", "main", "agent_lead")
	due := time.Now().UTC().Add(-2 * time.Second)
	goldenInsertSchedule(t, db, goldenScheduleRow{ID: "psched_due", TargetPipelineID: "pipe_main", CronExpr: "* * * * *", NextRunAt: due})

	sched.tick(context.Background())

	if n := goldenRunCount(t, db, "pipe_main"); n != 1 {
		t.Fatalf("expected exactly 1 pipeline_runs row, got %d", n)
	}
	row, err := store.GetByID(context.Background(), "psched_due")
	if err != nil {
		t.Fatalf("reload schedule: %v", err)
	}
	if row.LastStatus != "COMPLETED" {
		t.Errorf("last_status: got %q, want COMPLETED", row.LastStatus)
	}
	if row.LastRunID == "" {
		t.Error("last_run_id: expected non-empty")
	}
}

// TestGolden11_WakeGate_ProbeFalse_SuppressesRun_RecordsTelemetry proves
// a wake gate whose probe returns falsey (a) produces NO main-routine
// run, and (b) records the suppression via wake_check_count /
// last_wake_status rather than failing silently.
func TestGolden11_WakeGate_ProbeFalse_SuppressesRun_RecordsTelemetry(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_main", "main", "agent_lead")
	goldenSeedTransformPipeline(t, db, "pipe_probe", "probe", "false")
	due := time.Now().UTC().Add(-2 * time.Second)
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_gated", TargetPipelineID: "pipe_main", CronExpr: "* * * * *",
		NextRunAt: due, WakePipelineID: "pipe_probe",
	})

	sched.tick(context.Background())

	if n := goldenRunCount(t, db, "pipe_main"); n != 0 {
		t.Fatalf("wake gate suppressed the probe but the main routine still produced %d run(s)", n)
	}
	row, err := store.GetByID(context.Background(), "psched_gated")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.WakeCheckCount != 1 {
		t.Errorf("wake_check_count: got %d, want 1 (suppression must be RECORDED, not silent)", row.WakeCheckCount)
	}
	if row.LastWakeStatus != WakeStatusSkipped {
		t.Errorf("last_wake_status: got %q, want %q", row.LastWakeStatus, WakeStatusSkipped)
	}
	if row.LastStatus != "" || row.LastRunID != "" {
		t.Errorf("main-run telemetry must stay untouched on a skip: last_status=%q last_run_id=%q", row.LastStatus, row.LastRunID)
	}
}

// TestGolden11_WakeGate_FailClosed_ProbeErrors_Suppresses proves the
// fail-closed half of #1372: a probe that cannot run at all (target
// pipeline row does not exist) HOLDS the main routine when
// wake_fail_closed=1, and last_wake_status records HELD.
func TestGolden11_WakeGate_FailClosed_ProbeErrors_Suppresses(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_main", "main", "agent_lead")
	// Deliberately no pipe_ghost row — the probe run itself errors.
	due := time.Now().UTC().Add(-2 * time.Second)
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_fc", TargetPipelineID: "pipe_main", CronExpr: "* * * * *",
		NextRunAt: due, WakePipelineID: "pipe_ghost", WakeFailClosed: true,
	})

	sched.tick(context.Background())

	if n := goldenRunCount(t, db, "pipe_main"); n != 0 {
		t.Fatalf("fail-closed gate must hold on a broken probe, but the main routine produced %d run(s)", n)
	}
	row, err := store.GetByID(context.Background(), "psched_fc")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.LastWakeStatus != WakeStatusHeld {
		t.Errorf("last_wake_status: got %q, want %q", row.LastWakeStatus, WakeStatusHeld)
	}
}

// TestGolden11_WakeGate_FailOpen_ProbeErrors_Fires proves the DEFAULT
// (fail-open) half of the same gate: the identical broken-probe setup,
// but wake_fail_closed left at its default (false), fires the main
// routine anyway and records ERROR (not WOKE) so the breakage stays
// visible without silently blinding a monitoring schedule.
func TestGolden11_WakeGate_FailOpen_ProbeErrors_Fires(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_main", "main", "agent_lead")
	due := time.Now().UTC().Add(-2 * time.Second)
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_fo", TargetPipelineID: "pipe_main", CronExpr: "* * * * *",
		NextRunAt: due, WakePipelineID: "pipe_ghost", WakeFailClosed: false,
	})

	sched.tick(context.Background())

	if n := goldenRunCount(t, db, "pipe_main"); n != 1 {
		t.Fatalf("fail-open gate must fire the main routine on a broken probe, got %d run(s)", n)
	}
	row, err := store.GetByID(context.Background(), "psched_fo")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.LastWakeStatus != WakeStatusError {
		t.Errorf("last_wake_status: got %q, want %q (a fired-anyway probe failure must not read as a normal WOKE)", row.LastWakeStatus, WakeStatusError)
	}
}

// ---------------------------------------------------------------------------
// Scenario 12 — catch-up after downtime, all three policies.
// ---------------------------------------------------------------------------

// TestGolden12_Catchup_AllThreePolicies simulates a process down across
// exactly 3 due occurrences of a per-minute cron (dueAt, dueAt+1m,
// dueAt+2m == "now", minute-aligned so the count is exact, not
// approximate) and asserts the documented policy semantics: skip -> 0
// runs, once -> exactly 1, all -> exactly 3. Also asserts
// last_missed_count reflects reality for each policy.
func TestGolden12_Catchup_AllThreePolicies(t *testing.T) {
	nowTrunc := time.Now().UTC().Truncate(time.Minute)
	dueAt := nowTrunc.Add(-2 * time.Minute)

	cases := []struct {
		policy       string
		wantRuns     int
		wantMissed   int
		wantLastStat string
	}{
		{CatchupSkip, 0, 3, ""}, // dueAt itself + 2 extra = 3 dropped; last_status stamped SKIPPED by recordCatchupSkip
		{CatchupOnce, 1, 2, "COMPLETED"},
		{CatchupAll, 3, 0, "COMPLETED"},
	}
	for _, tc := range cases {
		t.Run(tc.policy, func(t *testing.T) {
			db, store, sched := newGoldenRig(t)
			pipeID := "pipe_" + tc.policy
			schedID := "psched_" + tc.policy
			goldenSeedAgentPipeline(t, db, pipeID, tc.policy, "agent_lead")
			goldenInsertSchedule(t, db, goldenScheduleRow{
				ID: schedID, TargetPipelineID: pipeID, CronExpr: "* * * * *",
				NextRunAt: dueAt, CatchupPolicy: tc.policy,
			})

			sched.tick(context.Background())

			if n := goldenRunCount(t, db, pipeID); n != tc.wantRuns {
				t.Errorf("catchup=%s: pipeline_runs count = %d, want %d", tc.policy, n, tc.wantRuns)
			}
			row, err := store.GetByID(context.Background(), schedID)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			if row.LastMissedCount != tc.wantMissed {
				t.Errorf("catchup=%s: last_missed_count = %d, want %d", tc.policy, row.LastMissedCount, tc.wantMissed)
			}
			if tc.policy == CatchupSkip {
				if row.LastStatus != "SKIPPED" {
					t.Errorf("catchup=skip: last_status = %q, want SKIPPED", row.LastStatus)
				}
			} else if row.LastStatus != tc.wantLastStat {
				t.Errorf("catchup=%s: last_status = %q, want %q", tc.policy, row.LastStatus, tc.wantLastStat)
			}
		})
	}
}

// TestGolden12_Catchup_All_CapsAt20Occurrences simulates a schedule left
// down for 30 minutes on a per-minute cron under catchup=all and proves
// maxCatchupFireOccurrences (schedules.go, =20) actually bounds the
// fire-storm: total fires this tick must be capped at 1 (the original
// due occurrence) + 20 (the capped extras) = 21, not the 30 a naive
// implementation would produce.
func TestGolden12_Catchup_All_CapsAt20Occurrences(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_cap", "cap", "agent_lead")
	nowTrunc := time.Now().UTC().Truncate(time.Minute)
	dueAt := nowTrunc.Add(-30 * time.Minute)
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_cap", TargetPipelineID: "pipe_cap", CronExpr: "* * * * *",
		NextRunAt: dueAt, CatchupPolicy: CatchupAll,
	})

	sched.tick(context.Background())

	const want = 1 + maxCatchupFireOccurrences // 21
	if n := goldenRunCount(t, db, "pipe_cap"); n != want {
		t.Errorf("catchup=all with 30 missed occurrences: pipeline_runs count = %d, want %d (cap not enforced)", n, want)
	}
	row, err := store.GetByID(context.Background(), "psched_cap")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	// catchup=all always drops nothing (it fires everything up to the
	// cap) — last_missed_count should be 0 for this tick; the remaining
	// ~9 occurrences beyond the cap are picked up on a later tick, not
	// asserted here.
	if row.LastMissedCount != 0 {
		t.Errorf("last_missed_count = %d, want 0", row.LastMissedCount)
	}
}

// TestGolden12_WakeGatedSchedule_BypassesCatchupExpansion proves the
// documented exception: a wake-gated schedule ALWAYS fires the single
// due occurrence regardless of catchup_policy, because the probe is
// evaluated live and is never re-run against a backdated timestamp
// (schedules.go fireOne, ~756-772). Configured catchup_policy=all with a
// 5-minute backlog and a probe that says yes — must still produce
// exactly 1 run, not 5 — while last_missed_count still reports the real
// backlog size (telemetry, not silently dropped).
func TestGolden12_WakeGatedSchedule_BypassesCatchupExpansion(t *testing.T) {
	db, store, sched := newGoldenRig(t)
	goldenSeedAgentPipeline(t, db, "pipe_main", "main", "agent_lead")
	goldenSeedTransformPipeline(t, db, "pipe_probe", "probe", "true")
	nowTrunc := time.Now().UTC().Truncate(time.Minute)
	dueAt := nowTrunc.Add(-5 * time.Minute)
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_gated_backlog", TargetPipelineID: "pipe_main", CronExpr: "* * * * *",
		NextRunAt: dueAt, CatchupPolicy: CatchupAll, WakePipelineID: "pipe_probe",
	})

	sched.tick(context.Background())

	if n := goldenRunCount(t, db, "pipe_main"); n != 1 {
		t.Fatalf("wake-gated schedule must bypass catchup expansion and fire exactly 1 run, got %d", n)
	}
	row, err := store.GetByID(context.Background(), "psched_gated_backlog")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.LastMissedCount < 4 {
		t.Errorf("last_missed_count = %d, want >= 4 (the 5-minute backlog must still be REPORTED even though it wasn't fired)", row.LastMissedCount)
	}
}

// ---------------------------------------------------------------------------
// Bonus — circuit breaker (#1405).
// ---------------------------------------------------------------------------

// alwaysFailRunner is an AgentRunner that errors on every call — used
// instead of mockRunner's errBySlug queue so the circuit-breaker test
// doesn't have to pre-size the queue to the exact number of fireOne
// calls it makes.
type alwaysFailRunner struct{ n int }

func (r *alwaysFailRunner) RunStep(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
	r.n++
	return AgentStepResult{}, errors.New("golden: deliberate step failure")
}

// TestGoldenBonus_CircuitBreaker_TripsAfterNFailures proves the breaker
// actually disables the schedule after MaxConsecutiveFailures straight
// FAILED fires and records WHY (disabled_reason=circuit_breaker), not
// just that it disabled.
func TestGoldenBonus_CircuitBreaker_TripsAfterNFailures(t *testing.T) {
	db := newGoldenScheduleDB(t)
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	runner := &alwaysFailRunner{}
	exec := NewExecutor(pipelines, NewResolver(db), runner, nil).WithRunStore(NewRunStore(db))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := NewPipelineScheduler(store, pipelines, exec, logger)

	goldenSeedAgentPipeline(t, db, "pipe_flaky", "flaky", "agent_lead")
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_breaker", TargetPipelineID: "pipe_flaky", CronExpr: "* * * * *",
		NextRunAt: time.Now().UTC(), MaxConsecutiveFailures: 3,
	})

	for i := 0; i < 3; i++ {
		row, err := store.GetByID(context.Background(), "psched_breaker")
		if err != nil {
			t.Fatalf("reload before fire %d: %v", i, err)
		}
		sched.fireOne(context.Background(), row)
	}

	row, err := store.GetByID(context.Background(), "psched_breaker")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Enabled {
		t.Error("schedule should be disabled after 3 straight failures")
	}
	if row.DisabledReason != scheduleDisabledReasonCircuitBreaker {
		t.Errorf("disabled_reason = %q, want %q", row.DisabledReason, scheduleDisabledReasonCircuitBreaker)
	}
	if row.ConsecutiveFailures < 3 {
		t.Errorf("consecutive_failures = %d, want >= 3", row.ConsecutiveFailures)
	}
	if n := goldenRunCount(t, db, "pipe_flaky"); n != 3 {
		t.Errorf("expected 3 FAILED pipeline_runs rows (one per fire), got %d", n)
	}
}

// TestGoldenBonus_CircuitBreaker_SuccessResetsStreak proves a COMPLETED
// fire zeroes consecutive_failures rather than merely not incrementing
// it — a schedule that fails twice then succeeds must not be one
// failure away from tripping.
func TestGoldenBonus_CircuitBreaker_SuccessResetsStreak(t *testing.T) {
	db := newGoldenScheduleDB(t)
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	runner := newMockRunner()
	runner.errBySlug["agent_lead"] = []error{errors.New("boom1"), errors.New("boom2")} // 2 fails, then falls through to a default success
	exec := NewExecutor(pipelines, NewResolver(db), runner, nil).WithRunStore(NewRunStore(db))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := NewPipelineScheduler(store, pipelines, exec, logger)

	goldenSeedAgentPipeline(t, db, "pipe_recover", "recover", "agent_lead")
	goldenInsertSchedule(t, db, goldenScheduleRow{
		ID: "psched_recover", TargetPipelineID: "pipe_recover", CronExpr: "* * * * *",
		NextRunAt: time.Now().UTC(), MaxConsecutiveFailures: 5,
	})

	for i := 0; i < 3; i++ {
		row, err := store.GetByID(context.Background(), "psched_recover")
		if err != nil {
			t.Fatalf("reload before fire %d: %v", i, err)
		}
		sched.fireOne(context.Background(), row)
	}

	row, err := store.GetByID(context.Background(), "psched_recover")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !row.Enabled {
		t.Errorf("schedule should still be enabled (only 2 failures against a threshold of 5), disabled_reason=%q", row.DisabledReason)
	}
	if row.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures after a COMPLETED fire = %d, want 0 (a success must reset the streak)", row.ConsecutiveFailures)
	}
	if row.LastStatus != "COMPLETED" {
		t.Errorf("last_status = %q, want COMPLETED", row.LastStatus)
	}
}
