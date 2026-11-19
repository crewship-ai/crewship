//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

// CaptureHost records requested nonzero stops as execution_cancelled, rather
// than exited. Confirmation must retain the exact execution's stop proof.
func TestConfirmProtectedStopAcceptsExactCancelledCaptureReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, receipt string
		confirmed     bool
	}{
		{"exact", "exec", true},
		{"missing", "", false},
		{"foreign", "other-exec", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, run := retainedAnalysisFixture(t, "CLAUDE_CODE", "", "", 137, "execution_cancelled")
			o.container = &retainedContainerStatus{state: "running"}
			run.AgentSlug = "agent"
			raw, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			if tc.receipt != "" {
				if err := runoutput.RecordHostStopConfirmed(filepath.Dir(run.Output.Directory), tc.receipt); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			snapshot, err := o.confirmProtectedRunStop(ctx, RunLocation{ContainerID: run.ContainerID, AgentSlug: run.AgentSlug, RunID: run.ID}, run)
			if tc.confirmed {
				if err != nil || !snapshot.Complete || snapshot.Result == nil || snapshot.Result.ExitCode != 137 || snapshot.Result.Reason != "execution_cancelled" {
					t.Fatalf("exact stopped capture not confirmed: snapshot=%+v err=%v", snapshot, err)
				}
			} else if err == nil {
				t.Fatal("unproved or foreign stop receipt confirmed cancellation")
			}
		})
	}
}
