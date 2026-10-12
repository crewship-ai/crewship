//go:build linux

package orchestrator

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// #2892: TestDirectProbeMissingKillNeverConfirmsAbsence failed once in CI with
// a bare "run never became alive" — the fixture had thrown away why. These
// reproduce startup failures deterministically and pin that the readiness
// error now carries the wrapper's exit status and output.
func TestAwaitDirectRunAliveReportsStartupFailure(t *testing.T) {
	tests := []struct {
		name    string
		argv    []string
		prepare func(t *testing.T, runID string)
		want    []string
	}{
		{
			// The wrapper refuses to overwrite a PID file (set -C) and exits 125.
			name: "pid file already exists",
			argv: []string{"sleep", "60"},
			prepare: func(t *testing.T, runID string) {
				if err := os.WriteFile(directRunPIDFile(runID), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"wrapper exited during startup", "exit status 125", "pid file: \"\""},
		},
		{
			name: "command exits before the probe sees it",
			argv: []string{"sh", "-c", "echo fixture-broke >&2; exit 3"},
			want: []string{"wrapper exited during startup", "exit status 3", "fixture-broke"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := New(stopProcessContainer{}, newMemState(), slog.Default())
			runID := NewRunID()
			if tc.prepare != nil {
				tc.prepare(t, runID)
			}
			_, done, output := startDirectRunProcess(t, runID, tc.argv)
			err := awaitDirectRunAlive(o, RunLocation{ContainerID: "test", AgentSlug: "a", RunID: runID}, done, output, 5*time.Second)
			if err == nil {
				t.Fatal("a fixture that died during startup read as alive")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("readiness error lacks %q:\n%v", want, err)
				}
			}
			// The exit status is put back for callers that wait on done.
			select {
			case <-done:
			default:
				t.Error("awaitDirectRunAlive consumed the exit status")
			}
		})
	}
}
