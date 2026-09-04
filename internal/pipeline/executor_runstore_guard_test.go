package pipeline

// TestNewExecutorTripsRunStoreGuard is the static guard for #2283.
//
// pipeline_runs persistence is opt-in — Executor.WithRunStore
// (executor.go) / PipelineHandler.SetRunStore (internal/api/pipelines.go).
// Production always wires it (cmd/crewship/cmd_start.go's NewWiredExecutor
// call always sets ExecutorDeps.RunStore). Every schedule test in this
// package predating #2283 — schedules_test.go, schedules_catchup_test.go,
// schedules_wake_test.go, schedules_wake_bypass_test.go,
// schedules_wake_failclosed_test.go — built its Executor via a bare
// NewExecutor(...) call instead, which does NOT persist to pipeline_runs at
// all. That means none of those tests could ever have caught a regression
// that broke run persistence for a scheduled fire: they only ever asserted
// the mock runner's call count or the schedule row's own last_status /
// last_run_id fields, never the actual pipeline_runs table an
// operator/CLI would query. internal/api/pipeline_webhooks_test.go's
// webhookHandlerRig had the identical gap on the webhook trigger path.
//
// The fix wires a RunStore by default: newScheduleExecutor (schedules_test.go)
// is now the one way to build an Executor in these files, and
// webhookHandlerRig (internal/api/pipeline_webhooks_test.go) always calls
// SetRunStore. This guard is what keeps a *future* test in one of these
// files from quietly reintroducing the gap by reaching for a bare
// NewExecutor(...) again: it fails the build the same day, not the next
// time someone goes looking for why a fire "worked" but left no row.
//
// Detection mirrors TestIngressFenceGate (internal/api/webhook_fence_gate_test.go)
// — a small statement window around each match, rather than a single-line
// regex, so a call chained onto the following line(s) still counts as
// wired. Scoped to the specific files #2283 named rather than every
// _test.go in the package: a repo-wide ban would demand retrofitting
// executor constructions that have nothing to do with run-persistence
// regressions (evalWakeProbe is a pure function; some scheduler-mechanics
// tests assert only on in-memory registry state or the schedule row, never
// pipeline_runs). See newScheduleExecutor's doc comment for why those are
// out of scope.
//
// A legitimate future exception opts out explicitly with a
// `// runstore-guard:allow reason` comment on the NewExecutor(...) line,
// mirroring the sanctioned-marker pattern the fence gate uses — silently
// skipping this guard is not an option a contributor should have.

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// runStoreGuardedFiles are the files #2283 found never wired a RunStore.
// Every NewExecutor(...) call in one of these must chain .WithRunStore(...)
// (directly, or via newScheduleExecutor, which does) unless explicitly
// opted out — see the allow marker above.
var runStoreGuardedFiles = []string{
	"schedules_test.go",
	"schedules_catchup_test.go",
	"schedules_wake_test.go",
	"schedules_wake_bypass_test.go",
	"schedules_wake_failclosed_test.go",
}

const runStoreAllowMarker = "runstore-guard:allow"

func TestNewExecutorTripsRunStoreGuard(t *testing.T) {
	var violations []string
	for _, name := range runStoreGuardedFiles {
		data, err := os.ReadFile(name)
		if err != nil {
			// A renamed/removed file is a guard-maintenance problem, not a
			// silent pass — fail loudly rather than let the list rot.
			t.Fatalf("read %s: %v (update runStoreGuardedFiles if this file moved)", name, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "NewExecutor(") {
				continue
			}
			if strings.Contains(line, runStoreAllowMarker) {
				continue
			}
			// Statement window: this line plus the next few, so a call
			// chained across lines (NewExecutor(...).\n  WithRunStore(...))
			// still counts as wired. Four lines covers every existing shape
			// in this package with room to spare.
			end := i + 4
			if end > len(lines) {
				end = len(lines)
			}
			window := strings.Join(lines[i:end], "\n")
			if strings.Contains(window, "WithRunStore") {
				continue
			}
			violations = append(violations, name+":"+strconv.Itoa(i+1)+"  "+strings.TrimSpace(line))
		}
	}
	if len(violations) > 0 {
		t.Fatalf("NewExecutor(...) built without a RunStore reopens the #2283 gap — none of these tests could catch a "+
			"regression that broke pipeline_runs persistence for a scheduled/webhook-triggered run.\n"+
			"Use newScheduleExecutor(...) (schedules_test.go) instead, or chain .WithRunStore(NewRunStore(db)) "+
			"yourself, or opt out explicitly with a %q comment on the line.\n"+
			"Offending sites:\n  %s", runStoreAllowMarker, strings.Join(violations, "\n  "))
	}
}
