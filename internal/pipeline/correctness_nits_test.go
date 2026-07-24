package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// #1430, 3.3 — a wake-probe whose final step if-skips must NOT surface the
// "<skipped>" sentinel as the run Output (evalIfCondition reads it as truthy,
// waking the schedule wrongly). The linear epilogue now mirrors the DAG's
// skip-aware fallback.
func TestExecutor_LinearEpilogue_SkipFinalNotTruthy(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	dsl := &DSL{
		Name: "probe",
		Steps: []Step{
			{ID: "decide", Type: StepAgentRun, AgentSlug: "a", Prompt: "x", If: "false"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StepOutputs["decide"] != "<skipped>" {
		t.Fatalf("expected the step to skip, got %q", res.StepOutputs["decide"])
	}
	if res.Output == "<skipped>" {
		t.Errorf("epilogue surfaced the skip sentinel as Output")
	}
	if evalIfCondition(res.Output) {
		t.Errorf("skip-final probe would wake the schedule wrongly: Output=%q", res.Output)
	}
}

// #1430, 3.5 — editing a due schedule without changing its cron must NOT
// swallow the pending occurrence by recomputing next_run_at into the future.
func TestScheduleStore_Save_PreservesDueOccurrence(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	store := NewScheduleStore(db)
	ctx := context.Background()

	created, err := store.Save(ctx, SaveScheduleInput{
		WorkspaceID: "ws_test", Name: "n", TargetPipelineID: "pipe_1",
		CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Force the row DUE: next_run_at two hours in the past, unfired.
	due := time.Now().Add(-2 * time.Hour).UTC()
	if _, err := db.ExecContext(ctx,
		`UPDATE pipeline_schedules SET next_run_at = ? WHERE id = ?`,
		tsformat.Format(due), created.ID); err != nil {
		t.Fatal(err)
	}

	// Unrelated edit (rename) with the SAME cron — must preserve the due bar.
	edited, err := store.Save(ctx, SaveScheduleInput{
		ID: created.ID, WorkspaceID: "ws_test", Name: "renamed", TargetPipelineID: "pipe_1",
		CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.NextRunAt == nil || edited.NextRunAt.After(time.Now()) {
		t.Errorf("due occurrence swallowed by an unrelated edit: next_run_at = %v", edited.NextRunAt)
	}

	// Changing the cron legitimately recomputes to the future.
	changed, err := store.Save(ctx, SaveScheduleInput{
		ID: created.ID, WorkspaceID: "ws_test", Name: "renamed", TargetPipelineID: "pipe_1",
		CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.NextRunAt == nil || changed.NextRunAt.Before(time.Now()) {
		t.Errorf("cron change should recompute next_run_at into the future, got %v", changed.NextRunAt)
	}
}

// #1430, 3.6 — a DEDUPED wake probe (a duplicate tick hitting its own
// idempotency key) must NOT fire the main routine, regardless of the
// fail-open/closed policy. A genuine probe failure still follows the policy.
func TestEvalWakeProbe_DedupedDoesNotDoubleFire(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		proceed, status := evalWakeProbe(&RunResult{Status: "DEDUPED"}, nil, failClosed)
		if proceed {
			t.Errorf("DEDUPED probe must not fire (failClosed=%v)", failClosed)
		}
		if status != WakeStatusSkipped {
			t.Errorf("DEDUPED probe status = %q, want SKIPPED (failClosed=%v)", status, failClosed)
		}
	}

	// Regression guard: a real non-COMPLETED probe still follows the policy.
	if proceed, st := evalWakeProbe(&RunResult{Status: "FAILED"}, nil, false); !proceed || st != WakeStatusError {
		t.Errorf("FAILED probe should fire OPEN by default, got proceed=%v status=%s", proceed, st)
	}
	if proceed, st := evalWakeProbe(&RunResult{Status: "FAILED"}, nil, true); proceed || st != WakeStatusHeld {
		t.Errorf("FAILED probe fail-closed should HOLD, got proceed=%v status=%s", proceed, st)
	}
	// A truthy COMPLETED probe still wakes.
	if proceed, st := evalWakeProbe(&RunResult{Status: "COMPLETED", Output: "true"}, nil, false); !proceed || st != WakeStatusWoke {
		t.Errorf("COMPLETED+truthy probe should WOKE, got proceed=%v status=%s", proceed, st)
	}
}
