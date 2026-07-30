package pipeline

// Explicit attempt-to-bypass tests for the fail-closed wake gate (#1486, #1372).
//
// The invariant: with WakeFailClosed set, the gated routine fires ONLY on a
// probe that ran to completion AND answered affirmatively. Everything else —
// including every way a probe can be broken rather than merely negative — holds
// the run.
//
// schedules_wake_failclosed_test.go already tables the obvious non-affirmative
// shapes (error, timeout, nil result, FAILED, empty status), all with an EMPTY
// Output. That is the gap these tests close: an empty Output is falsey, so a
// hypothetical regression that consulted the probe's output BEFORE its status
// would still be held by those cases and they would pass. The attacker's move is
// to supply a TRUTHY output on a run that did not complete — a probe that prints
// `true` and then crashes, or that is deduped, or that returns a result
// alongside an error. Each of those is written out below.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEvalWakeProbe_FailClosed_AffirmativeOutputCannotOverrideANonCompletedRun:
// the gate must key on the RUN, not on the bytes the run happened to leave
// behind. A probe that emits an affirmative answer and then fails is not an
// affirmative answer — it is a broken probe, and a fail-closed gate exists
// precisely to distrust broken probes.
func TestEvalWakeProbe_FailClosed_AffirmativeOutputCannotOverrideANonCompletedRun(t *testing.T) {
	// Every string evalIfCondition treats as true (executor_render.go:185-214),
	// so the test does not depend on which truthy spelling the probe emitted.
	truthy := []string{"true", "1", "yes", "on", "TRUE", "  true\n", "anything at all"}

	for _, status := range []string{"FAILED", "RUNNING", "CANCELLED", "TIMED_OUT", "", "completed", "PARTIAL"} {
		for _, out := range truthy {
			proceed, wake := evalWakeProbe(&RunResult{Status: status, Output: out}, nil, true)
			if proceed {
				t.Errorf("status=%q output=%q: fail-closed gate FIRED the gated routine on a run that "+
					"never completed — the gate is reading the probe's output instead of its outcome",
					status, out)
			}
			if wake != WakeStatusHeld {
				t.Errorf("status=%q output=%q: wake status = %q, want %q", status, out, wake, WakeStatusHeld)
			}
		}
	}
}

// TestEvalWakeProbe_FailClosed_ResultAlongsideAnErrorIsStillHeld: the executor
// can hand back both a result and an error (a run that produced output and then
// failed to persist, a partially-applied step). The error must win. Checking the
// result first — the natural-looking refactor, since it is the more specific
// value — would turn every such half-failure into a fire.
func TestEvalWakeProbe_FailClosed_ResultAlongsideAnErrorIsStillHeld(t *testing.T) {
	cases := []struct {
		name string
		res  *RunResult
		err  error
	}{
		{"completed result + transport error", &RunResult{Status: "COMPLETED", Output: "true"}, errors.New("store write failed")},
		{"completed result + deadline exceeded", &RunResult{Status: "COMPLETED", Output: "1"}, context.DeadlineExceeded},
		{"deduped result + error", &RunResult{Status: "DEDUPED", Output: "true"}, errors.New("boom")},
		{"cancelled context", &RunResult{Status: "COMPLETED", Output: "yes"}, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proceed, wake := evalWakeProbe(tc.res, tc.err, true)
			if proceed {
				t.Fatalf("the gate fired despite runErr=%v — an errored probe run must never be treated "+
					"as an affirmative answer, whatever result it carries", tc.err)
			}
			if wake != WakeStatusHeld {
				t.Errorf("wake status = %q, want %q", wake, WakeStatusHeld)
			}
		})
	}
}

// TestEvalWakeProbe_CompletedStatusIsNotFuzzyMatched: the affirmative branch is
// reached by an EXACT "COMPLETED". Anything else — a different case, stray
// whitespace, a superstring — must not qualify under either policy. Case-folding
// or a strings.Contains/HasPrefix comparison here would let a "COMPLETED_WITH_ERRORS"
// or a lowercase status from some other producer walk straight through the gate.
func TestEvalWakeProbe_CompletedStatusIsNotFuzzyMatched(t *testing.T) {
	nearMisses := []string{
		"completed", "Completed", "cOmPlEtEd",
		" COMPLETED", "COMPLETED ", "COMPLETED\n", "\tCOMPLETED",
		"COMPLETED_WITH_ERRORS", "NOT_COMPLETED", "PRECOMPLETED", "COMPLETE",
	}
	for _, status := range nearMisses {
		// Fail-closed: a near-miss must HOLD.
		if proceed, wake := evalWakeProbe(&RunResult{Status: status, Output: "true"}, nil, true); proceed || wake != WakeStatusHeld {
			t.Errorf("failClosed=true status=%q: proceed=%v wake=%q, want false/%s — the completed check "+
				"is matching loosely", status, proceed, wake, WakeStatusHeld)
		}
		// Fail-open: the run still fires (that is the documented default), but it
		// must be recorded as ERROR, not as a genuine WOKE. Mislabelling it would
		// hide a broken probe behind normal-looking telemetry.
		if _, wake := evalWakeProbe(&RunResult{Status: status, Output: "true"}, nil, false); wake != WakeStatusError {
			t.Errorf("failClosed=false status=%q: wake=%q, want %s — a near-miss status must be reported "+
				"as a probe failure, not as a normal wake", status, wake, WakeStatusError)
		}
	}
}

// TestEvalWakeProbe_FailClosed_DedupedCannotFireTheGatedRoutine: DEDUPED means a
// duplicate tick hit the probe's own idempotency key, so the ORIGINAL tick owns
// the decision (#1430). An attacker who can force a duplicate tick — a restart
// before next_run_at advanced, a replayed fire — must not be able to convert
// "someone else is deciding" into "yes".
//
// Note the deliberately asymmetric assertion: DEDUPED is SKIPPED, never HELD,
// under both policies. It is not a failure, so it must not be laundered into the
// fail-open ERROR branch either (which WOULD fire the routine).
func TestEvalWakeProbe_FailClosed_DedupedCannotFireTheGatedRoutine(t *testing.T) {
	for _, failClosed := range []bool{true, false} {
		for _, out := range []string{"true", "1", "", "false"} {
			proceed, wake := evalWakeProbe(&RunResult{Status: "DEDUPED", Output: out}, nil, failClosed)
			if proceed {
				t.Errorf("failClosed=%v output=%q: a DEDUPED probe fired the gated routine — a duplicate "+
					"tick is now a free wake", failClosed, out)
			}
			if wake != WakeStatusSkipped {
				t.Errorf("failClosed=%v output=%q: wake=%q, want %s", failClosed, out, wake, WakeStatusSkipped)
			}
		}
	}
}

// TestPipelineScheduler_FireOne_WakeGateHoldsWhenTheProbeIsUnrunnable is the
// end-to-end half, and it forces the gate's DEPENDENCY to break rather than the
// probe to answer no.
//
// The existing e2e (TestPipelineScheduler_FireOne_WakeGateFailsClosed) points
// the gate at a ghost pipeline id, so the executor fails at LOOKUP. This one
// leaves the probe row present and well-referenced but makes its stored
// definition unusable, so the failure happens while running it. The distinction
// matters: a lookup miss and a run failure travel different code paths back to
// evalWakeProbe, and only one of them was covered.
func TestPipelineScheduler_FireOne_WakeGateHoldsWhenTheProbeIsUnrunnable(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()

	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	// A probe whose definition is not a usable routine. The row exists, the
	// schedule references it legitimately, and the resolver will find it — it
	// simply cannot be executed.
	seedPipelineDef(t, db, "pipe_probe_broken", "probe-broken", `{"dsl_version":"1.0","name":"broken"`)

	row := mustSaveFailClosedWakeSchedule(t, store, "pipe_probe_broken")
	sched.fireOne(context.Background(), row)

	got, err := store.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("reload schedule: %v", err)
	}
	if got.LastWakeStatus != WakeStatusHeld {
		t.Errorf("last_wake_status = %q, want %s — a probe that cannot run is not permission to run",
			got.LastWakeStatus, WakeStatusHeld)
	}
	if got.LastStatus != "" || got.LastRunID != "" {
		t.Fatalf("the gated routine FIRED behind an unrunnable probe: status=%q run=%q",
			got.LastStatus, got.LastRunID)
	}
	if got.WakeCheckCount != 1 {
		t.Errorf("wake_check_count = %d, want 1 — the held tick must still be recorded", got.WakeCheckCount)
	}
	if got.WakeFireCount != 0 {
		t.Errorf("wake_fire_count = %d, want 0 — a held tick fired nothing and must not claim otherwise",
			got.WakeFireCount)
	}
	// A held tick still has to move the clock forward, or the same occurrence is
	// re-evaluated every 30s and a broken probe becomes a retry storm.
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
		t.Errorf("next_run_at = %v, want a future time — a held tick must advance the schedule", got.NextRunAt)
	}
}

// TestPipelineScheduler_FireOne_WakeGateHoldsWhenTheProbeVanishes covers the
// "dependency removed out from under the gate" shape: the probe routine is
// deleted after the schedule was armed. A gate whose probe no longer exists must
// hold, not degrade to ungated firing — the failure mode being guarded against
// is "delete the probe, and the routine you gated runs unconditionally forever".
func TestPipelineScheduler_FireOne_WakeGateHoldsWhenTheProbeVanishes(t *testing.T) {
	db, store, sched := newWakeTestRig(t)
	defer db.Close()

	seedPipelineDef(t, db, "pipe_main", "main", transformPipelineDef("main", "ran"))
	seedPipelineDef(t, db, "pipe_probe", "probe", transformPipelineDef("probe", "true"))
	row := mustSaveFailClosedWakeSchedule(t, store, "pipe_probe")

	// Sanity: with the probe present and truthy, the gate does fire. Without
	// this the "held" assertion below could pass for the wrong reason.
	sched.fireOne(context.Background(), row)
	armed, err := store.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if armed.LastWakeStatus != WakeStatusWoke {
		t.Fatalf("precondition: a live truthy probe must wake, got %q", armed.LastWakeStatus)
	}

	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM pipelines WHERE id = 'pipe_probe'`); err != nil {
		t.Fatalf("delete probe: %v", err)
	}

	sched.fireOne(context.Background(), armed)
	got, err := store.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("reload after delete: %v", err)
	}
	if got.LastWakeStatus != WakeStatusHeld {
		t.Fatalf("last_wake_status = %q after the probe was deleted, want %s — deleting the probe must "+
			"not un-gate the routine", got.LastWakeStatus, WakeStatusHeld)
	}
	if got.WakeFireCount != armed.WakeFireCount {
		t.Errorf("wake_fire_count moved from %d to %d — the gate fired with no probe at all",
			armed.WakeFireCount, got.WakeFireCount)
	}
}
