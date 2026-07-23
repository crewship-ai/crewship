package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Wake gate — fail-closed policy (#1372).
//
// The default wake gate fails OPEN: a probe that errors / returns nil /
// finishes non-COMPLETED still lets the gated main routine fire, so a
// broken or tampered probe cannot suppress the autonomous run. For an
// unattended schedule that is the wrong default. An opt-in per-schedule
// `WakeFailClosed` flag flips the decision so any non-affirmative probe
// outcome HOLDS the run instead of proceeding.
// ---------------------------------------------------------------------------

// mustSaveFailClosedWakeSchedule mirrors mustSaveWakeSchedule but arms
// the fail-closed policy on the gate.
func mustSaveFailClosedWakeSchedule(t *testing.T, store *ScheduleStore, wakePipelineID string) *Schedule {
	t.Helper()
	sched, err := store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "gated-fc",
		TargetPipelineID: "pipe_main",
		CronExpr:         "* * * * *",
		WakePipelineID:   wakePipelineID,
		WakeInputs:       map[string]any{"threshold": "100"},
		WakeFailClosed:   true,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save fail-closed schedule: %v", err)
	}
	return sched
}

// TestEvalWakeProbe_FailClosed_HoldsOnNonAffirmative is the core
// security assertion: every non-affirmative probe shape (error, timeout,
// nil result, non-COMPLETED / empty status) must HOLD the run when the
// gate is fail-closed. These are exactly the shapes that fail OPEN today.
func TestEvalWakeProbe_FailClosed_HoldsOnNonAffirmative(t *testing.T) {
	cases := []struct {
		name string
		res  *RunResult
		err  error
	}{
		{"probe error", nil, errors.New("probe blew up")},
		{"probe timeout", nil, context.DeadlineExceeded},
		{"nil result, no error", nil, nil},
		{"non-completed status", &RunResult{Status: "FAILED"}, nil},
		{"empty/ambiguous status", &RunResult{Status: ""}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proceed, status := evalWakeProbe(tc.res, tc.err, true)
			if proceed {
				t.Fatalf("fail-closed must HOLD the run on %q, got proceed=true", tc.name)
			}
			if status != WakeStatusHeld {
				t.Errorf("status: got %q, want %q", status, WakeStatusHeld)
			}
		})
	}
}

// TestEvalWakeProbe_Default_FailsOpen pins the historical default: a
// non-gating probe that errors still proceeds (fail OPEN) and records
// ERROR so the breakage stays visible.
func TestEvalWakeProbe_Default_FailsOpen(t *testing.T) {
	cases := []struct {
		name string
		res  *RunResult
		err  error
	}{
		{"probe error", nil, errors.New("boom")},
		{"nil result", nil, nil},
		{"non-completed status", &RunResult{Status: "FAILED"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proceed, status := evalWakeProbe(tc.res, tc.err, false)
			if !proceed {
				t.Fatalf("default must fail OPEN on %q, got proceed=false", tc.name)
			}
			if status != WakeStatusError {
				t.Errorf("status: got %q, want %q", status, WakeStatusError)
			}
		})
	}
}

// TestEvalWakeProbe_Completed pins the affirmative paths — truthy WOKE,
// falsey SKIPPED — under both policies (fail-closed only changes the
// FAILURE branch, never the affirmative one).
func TestEvalWakeProbe_Completed(t *testing.T) {
	for _, failClosed := range []bool{false, true} {
		if proceed, status := evalWakeProbe(&RunResult{Status: "COMPLETED", Output: "true"}, nil, failClosed); !proceed || status != WakeStatusWoke {
			t.Errorf("truthy probe (failClosed=%v) should wake: proceed=%v status=%q", failClosed, proceed, status)
		}
		if proceed, status := evalWakeProbe(&RunResult{Status: "COMPLETED", Output: "false"}, nil, failClosed); proceed || status != WakeStatusSkipped {
			t.Errorf("falsey probe (failClosed=%v) should skip: proceed=%v status=%q", failClosed, proceed, status)
		}
	}
}

// TestScheduleStore_Save_WakeFailClosedRoundTrip proves the flag
// persists and that whole-row update semantics can toggle it off.
func TestScheduleStore_Save_WakeFailClosedRoundTrip(t *testing.T) {
	db, store, _ := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "true"))

	sched := mustSaveFailClosedWakeSchedule(t, store, "pipe_probe")
	if !sched.WakeFailClosed {
		t.Fatalf("wake_fail_closed should round-trip true, got false")
	}

	off, err := store.Save(context.Background(), SaveScheduleInput{
		ID:               sched.ID,
		WorkspaceID:      "ws_test",
		Name:             "gated-fc",
		TargetPipelineID: "pipe_main",
		CronExpr:         "* * * * *",
		WakePipelineID:   "pipe_probe",
		WakeFailClosed:   false,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if off.WakeFailClosed {
		t.Errorf("expected fail-closed cleared, still true")
	}
}

// TestPipelineScheduler_FireOne_WakeGateFailsClosed is the end-to-end
// assertion via the real executor: a ghost probe id makes the probe run
// error, and under fail-closed the main routine must NEVER fire.
func TestPipelineScheduler_FireOne_WakeGateFailsClosed(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	row := mustSaveFailClosedWakeSchedule(t, store, "pipe_ghost")

	sched.fireOne(context.Background(), row)

	got, _ := store.GetByID(context.Background(), row.ID)
	if got.LastWakeStatus != WakeStatusHeld {
		t.Errorf("last_wake_status: got %q, want HELD", got.LastWakeStatus)
	}
	// Main routine telemetry must stay empty — the run was held.
	if got.LastStatus != "" || got.LastRunID != "" {
		t.Errorf("fail-closed: main run must NOT fire, got status=%q run=%q", got.LastStatus, got.LastRunID)
	}
	if got.WakeCheckCount != 1 || got.WakeFireCount != 0 {
		t.Errorf("counters: got %d/%d, want 1/0", got.WakeCheckCount, got.WakeFireCount)
	}
	// A held tick still advances next_run_at (like a SKIPPED tick).
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
		t.Errorf("held tick must advance next_run_at, got %v", got.NextRunAt)
	}
}
