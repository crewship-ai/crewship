package journal

import (
	"context"
	"testing"
	"time"
)

// emitMissionRun is emitRun for a run dispatched against an issue: the
// dispatcher stamps mission_id on run.started (and, via ctx, on the
// terminal entry). The aggregate has to surface it, and RunsQuery has to
// filter on it, or an issue can never list its runs from the journal.
func emitMissionRun(t *testing.T, w *Writer, ws, agentID, missionID, runID string, finished bool, when time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: ws, AgentID: agentID, MissionID: missionID,
		Type: EntryRunStarted, ActorType: ActorOrchestrator, Summary: "started",
		Payload: map[string]any{"trigger_type": "ASSIGNMENT"}, TraceID: runID, TS: when,
	}); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if !finished {
		return
	}
	// Terminal entry deliberately WITHOUT mission_id: older dispatchers did
	// not stamp it there, and the filter must not turn such a run into a
	// "still running" one by pruning its terminal entry.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: ws, AgentID: agentID,
		Type: EntryRunCompleted, ActorType: ActorOrchestrator, Summary: "done",
		Payload: map[string]any{"exit_code": float64(0)}, TraceID: runID, TS: when.Add(time.Minute),
	}); err != nil {
		t.Fatalf("emit terminal: %v", err)
	}
}

func TestListRuns_MissionIDProjectedAndFiltered(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitMissionRun(t, w, "ws_test", "agent_a", "m_eng1", "run_1", true, now.Add(-10*time.Minute))
	emitMissionRun(t, w, "ws_test", "agent_a", "m_eng1", "run_2", false, now.Add(-5*time.Minute))
	emitMissionRun(t, w, "ws_test", "agent_b", "m_eng2", "run_3", true, now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_b", "run_chat", "COMPLETED", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	all, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4", total)
	}
	byID := map[string]RunAggregated{}
	for _, r := range all {
		byID[r.ID] = r
	}
	if byID["run_1"].MissionID != "m_eng1" || byID["run_3"].MissionID != "m_eng2" {
		t.Fatalf("mission_id not projected: run_1=%q run_3=%q", byID["run_1"].MissionID, byID["run_3"].MissionID)
	}
	if byID["run_chat"].MissionID != "" {
		t.Fatalf("chat run carries a mission: %q", byID["run_chat"].MissionID)
	}

	only, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", MissionID: "m_eng1"})
	if err != nil {
		t.Fatalf("list by mission: %v", err)
	}
	if total != 2 || len(only) != 2 {
		t.Fatalf("mission filter: total=%d rows=%d, want 2/2", total, len(only))
	}
	for _, r := range only {
		if r.MissionID != "m_eng1" {
			t.Fatalf("row %s belongs to %q, not m_eng1", r.ID, r.MissionID)
		}
	}
	// run_1's terminal entry has no mission_id; the filter must still see
	// the run as finished.
	for _, r := range only {
		if r.ID == "run_1" && r.Status != RunStatusCompleted {
			t.Fatalf("run_1 status = %s, want COMPLETED (terminal entry pruned by the mission filter?)", r.Status)
		}
	}

	got, err := GetRunByID(context.Background(), db, "ws_test", "run_3")
	if err != nil || got == nil {
		t.Fatalf("get run_3: %v %v", got, err)
	}
	if got.MissionID != "m_eng2" {
		t.Fatalf("GetRunByID mission_id = %q, want m_eng2", got.MissionID)
	}
}
