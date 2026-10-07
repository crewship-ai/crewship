package quartermaster

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestComputeFromDBReturnsMetricsOnlyForRequestedWorkspace(t *testing.T) {
	db := openDB(t)
	seed(t, db, journal.Entry{WorkspaceID: "ws_test", MissionID: "mission", Type: journal.EntryExecCommand, Payload: map[string]any{"exit_code": 0}})
	metrics, steps, err := ComputeFromDB(context.Background(), db, "ws_test", "mission")
	if err != nil || len(steps) != 1 || metrics.ToolCallCount != 1 || metrics.ToolSuccessRate != 1 {
		t.Fatalf("metrics=%+v steps=%+v err=%v", metrics, steps, err)
	}
	metrics, steps, err = ComputeFromDB(context.Background(), db, "other", "mission")
	if err != nil || len(steps) != 0 || metrics.ToolCallCount != 0 {
		t.Fatalf("cross-workspace metrics=%+v steps=%+v err=%v", metrics, steps, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, steps, err = ComputeFromDB(context.Background(), db, "ws_test", "mission")
	if err == nil || steps != nil {
		t.Fatalf("closed DB returned partial trajectory: %+v %v", steps, err)
	}
	if runs, err := ListRuns(context.Background(), db, "ws_test", 20); err == nil || runs != nil {
		t.Fatalf("closed DB returned runs: %+v %v", runs, err)
	}
}
