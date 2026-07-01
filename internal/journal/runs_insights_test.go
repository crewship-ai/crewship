package journal

import (
	"context"
	"testing"
	"time"
)

// emitRunFull writes a run.started (+optional terminal) with a controllable
// trigger, resolved model and duration so RunInsights aggregation can be
// asserted deterministically. status="" leaves the run RUNNING (no terminal);
// the model is then stamped on run.started metadata (the fallback source
// ListRuns/RunInsights read for live runs).
func emitRunFull(t *testing.T, w *Writer, ws, agentID, runID, status, trigger, model string, started time.Time, dur time.Duration) {
	t.Helper()
	ctx := context.Background()

	startedMeta := map[string]any{"trigger_type": trigger}
	if status == "" && model != "" {
		startedMeta["metadata"] = map[string]any{"model": model}
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        EntryRunStarted,
		ActorType:   ActorSidecar,
		Summary:     "started",
		Payload:     startedMeta,
		TraceID:     runID,
		TS:          started,
	}); err != nil {
		t.Fatalf("emit started %s: %v", runID, err)
	}
	if status == "" {
		return
	}

	var et EntryType
	switch status {
	case "COMPLETED":
		et = EntryRunCompleted
	case "FAILED":
		et = EntryRunFailed
	case "CANCELLED":
		et = EntryRunCancelled
	case "TIMEOUT":
		et = EntryRunTimeout
	default:
		t.Fatalf("unknown status %q", status)
	}
	payload := map[string]any{"exit_code": float64(0)}
	if model != "" {
		payload["metadata"] = map[string]any{"model": model}
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        et,
		ActorType:   ActorSidecar,
		Summary:     status,
		Payload:     payload,
		TraceID:     runID,
		TS:          started.Add(dur),
	}); err != nil {
		t.Fatalf("emit terminal %s: %v", runID, err)
	}
}

func catTotal(cats []CategoryCount, key string) (total, failed int, found bool) {
	for _, c := range cats {
		if c.Key == key {
			return c.Total, c.Failed, true
		}
	}
	return 0, 0, false
}

func agentTotal(rows []AgentCount, id string) (total, failed int, found bool) {
	for _, a := range rows {
		if a.AgentID == id {
			return a.Total, a.Failed, true
		}
	}
	return 0, 0, false
}

func TestRunInsights_AggregatesWindow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	// In-window runs (started within the last 24h).
	emitRunFull(t, w, "ws_i", "agent_a", "r_a", "COMPLETED", "USER", "claude-opus", now.Add(-5*time.Hour), 10*time.Second)
	emitRunFull(t, w, "ws_i", "agent_a", "r_b", "COMPLETED", "USER", "claude-opus", now.Add(-4*time.Hour), 20*time.Second)
	emitRunFull(t, w, "ws_i", "agent_b", "r_c", "COMPLETED", "CRON", "claude-sonnet", now.Add(-3*time.Hour), 30*time.Second)
	emitRunFull(t, w, "ws_i", "agent_b", "r_d", "FAILED", "WEBHOOK", "claude-sonnet", now.Add(-2*time.Hour), 40*time.Second)
	emitRunFull(t, w, "ws_i", "agent_a", "r_e", "", "AGENT", "claude-opus", now.Add(-1*time.Hour), 0) // running
	// Out-of-window run (2 days old) — must be excluded from the 24h window.
	emitRunFull(t, w, "ws_i", "agent_a", "r_old", "COMPLETED", "USER", "claude-opus", now.Add(-48*time.Hour), 10*time.Second)
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	res, err := RunInsights(context.Background(), db, "ws_i", RunWindow24h)
	if err != nil {
		t.Fatalf("insights: %v", err)
	}

	if res.Total != 5 {
		t.Errorf("Total = %d, want 5 (old run excluded)", res.Total)
	}
	if res.Succeeded != 3 {
		t.Errorf("Succeeded = %d, want 3", res.Succeeded)
	}
	if res.Failed != 1 {
		t.Errorf("Failed = %d, want 1", res.Failed)
	}
	if res.Running != 1 {
		t.Errorf("Running = %d, want 1", res.Running)
	}
	// durations of finished runs: 10s,20s,30s,40s → nearest-rank
	// p50 = sorted[ceil(.5*4)-1] = sorted[1] = 20s; p95 = sorted[3] = 40s
	if res.DurationP50Ms != 20000 {
		t.Errorf("DurationP50Ms = %d, want 20000", res.DurationP50Ms)
	}
	if res.DurationP95Ms != 40000 {
		t.Errorf("DurationP95Ms = %d, want 40000", res.DurationP95Ms)
	}

	// by_trigger
	if tot, fail, ok := catTotal(res.ByTrigger, "USER"); !ok || tot != 2 || fail != 0 {
		t.Errorf("ByTrigger[USER] = (%d,%d,%v), want (2,0,true)", tot, fail, ok)
	}
	if tot, fail, ok := catTotal(res.ByTrigger, "WEBHOOK"); !ok || tot != 1 || fail != 1 {
		t.Errorf("ByTrigger[WEBHOOK] = (%d,%d,%v), want (1,1,true)", tot, fail, ok)
	}
	// by_model
	if tot, fail, ok := catTotal(res.ByModel, "claude-opus"); !ok || tot != 3 || fail != 0 {
		t.Errorf("ByModel[opus] = (%d,%d,%v), want (3,0,true)", tot, fail, ok)
	}
	if tot, fail, ok := catTotal(res.ByModel, "claude-sonnet"); !ok || tot != 2 || fail != 1 {
		t.Errorf("ByModel[sonnet] = (%d,%d,%v), want (2,1,true)", tot, fail, ok)
	}
	// by_agent
	if tot, fail, ok := agentTotal(res.ByAgent, "agent_a"); !ok || tot != 3 || fail != 0 {
		t.Errorf("ByAgent[agent_a] = (%d,%d,%v), want (3,0,true)", tot, fail, ok)
	}
	if tot, fail, ok := agentTotal(res.ByAgent, "agent_b"); !ok || tot != 2 || fail != 1 {
		t.Errorf("ByAgent[agent_b] = (%d,%d,%v), want (2,1,true)", tot, fail, ok)
	}
	if res.Truncated {
		t.Errorf("Truncated = true, want false for a 6-row workspace")
	}
}

func TestRunInsights_EmptyWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	res, err := RunInsights(context.Background(), db, "ws_empty", RunWindow7d)
	if err != nil {
		t.Fatalf("insights: %v", err)
	}
	if res.Total != 0 || res.Succeeded != 0 || res.Failed != 0 || res.Running != 0 {
		t.Errorf("empty workspace should be all-zero, got %+v", res)
	}
	if res.DurationP50Ms != 0 || res.DurationP95Ms != 0 {
		t.Errorf("empty workspace percentiles should be 0, got p50=%d p95=%d", res.DurationP50Ms, res.DurationP95Ms)
	}
	if len(res.ByTrigger) != 0 || len(res.ByModel) != 0 || len(res.ByAgent) != 0 {
		t.Errorf("empty workspace breakdowns should be empty, got %+v", res)
	}
	if res.Window != "7d" {
		t.Errorf("Window = %q, want 7d", res.Window)
	}
}

func TestRunInsights_RequiresWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := RunInsights(context.Background(), db, "", RunWindow24h); err == nil {
		t.Fatal("expected error when workspace_id is empty")
	}
}
