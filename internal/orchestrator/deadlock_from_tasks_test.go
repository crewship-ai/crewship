package orchestrator

import "testing"

func ti(statuses ...string) []TaskInfo {
	out := make([]TaskInfo, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, TaskInfo{Status: s})
	}
	return out
}

// deadlockFromTasks is the pure core the tick loop now shares with the
// completion check. Pin its decision table so the shared-snapshot
// refactor cannot drift from the old per-call detectDeadlock behaviour.
func TestDeadlockFromTasks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		tasks  []TaskInfo
		expect bool
	}{
		{"empty", ti(), false},
		{"all completed", ti("COMPLETED", "COMPLETED"), false},
		{"one in progress", ti("BLOCKED", "IN_PROGRESS"), false},
		{"one pending", ti("BLOCKED", "PENDING"), false},
		{"awaiting approval is progress", ti("BLOCKED", "AWAITING_APPROVAL"), false},
		{"blocked among terminal is deadlock", ti("COMPLETED", "FAILED", "BLOCKED"), true},
		{"all blocked is deadlock", ti("BLOCKED", "BLOCKED"), true},
		{"terminal only no deadlock", ti("COMPLETED", "FAILED", "SKIPPED"), false},
	}
	for _, tc := range cases {
		if got := deadlockFromTasks(tc.tasks); got != tc.expect {
			t.Errorf("%s: deadlockFromTasks = %v, want %v", tc.name, got, tc.expect)
		}
	}
}
