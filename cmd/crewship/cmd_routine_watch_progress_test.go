package main

import (
	"strings"
	"testing"
	"time"
)

func ev(runID, entryType, stepID, ts string, payload map[string]any) watchEntry {
	if stepID != "" {
		if payload == nil {
			payload = map[string]any{}
		}
		payload["step_id"] = stepID
	}
	return watchEntry{RunID: runID, EntryType: entryType, Timestamp: ts, Payload: payload}
}

func TestComputeProgress_InFlightRun(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 30, 0, time.UTC)
	rows := []watchEntry{
		ev("run-1", "pipeline.run.started", "", "2026-07-07T12:00:00Z", nil),
		ev("run-1", "pipeline.step.started", "parse", "2026-07-07T12:00:01Z", nil),
		ev("run-1", "pipeline.step.completed", "parse", "2026-07-07T12:00:10Z", map[string]any{"cost_usd": 0.002}),
		ev("run-1", "pipeline.step.started", "verify", "2026-07-07T12:00:11Z", nil),
	}
	p := computeProgress(rows, 4, "", now)
	if p == nil {
		t.Fatal("nil progress")
	}
	if p.Completed != 1 || p.Total != 4 {
		t.Errorf("steps = %d/%d, want 1/4", p.Completed, p.Total)
	}
	if p.CurrentStep != "verify" {
		t.Errorf("current step = %q, want verify", p.CurrentStep)
	}
	if p.Status != "running" || p.Terminal {
		t.Errorf("status = %q terminal=%v, want running/false", p.Status, p.Terminal)
	}
	if p.CostUSD != 0.002 {
		t.Errorf("cost = %v, want 0.002", p.CostUSD)
	}
	if p.Elapsed != 30*time.Second {
		t.Errorf("elapsed = %v, want 30s", p.Elapsed)
	}

	line := formatProgressLine(p, "acct")
	for _, want := range []string{"acct", "step 1/4", "verify", "RUNNING", "$0.0020", "30"} {
		if !strings.Contains(line, want) {
			t.Errorf("progress line missing %q:\n%s", want, line)
		}
	}
}

func TestComputeProgress_SkippedStepCountsAsProcessed(t *testing.T) {
	// A skipped step must advance the step counter — otherwise "step N/total"
	// stalls below total whenever a conditional branch is skipped, reading as
	// a stuck run. Dedicated pipeline.step.skipped type, no cost.
	now := time.Date(2026, 7, 7, 12, 0, 30, 0, time.UTC)
	rows := []watchEntry{
		ev("run-1", "pipeline.run.started", "", "2026-07-07T12:00:00Z", nil),
		ev("run-1", "pipeline.step.completed", "parse", "2026-07-07T12:00:10Z", map[string]any{"cost_usd": 0.002}),
		ev("run-1", "pipeline.step.skipped", "notify", "2026-07-07T12:00:12Z", map[string]any{"kind": "skipped", "condition": "false"}),
	}
	p := computeProgress(rows, 2, "", now)
	if p == nil {
		t.Fatal("nil progress")
	}
	if p.Completed != 2 || p.Total != 2 {
		t.Errorf("steps = %d/%d, want 2/2 (skipped step counts)", p.Completed, p.Total)
	}
	if p.CostUSD != 0.002 {
		t.Errorf("cost = %v, want 0.002 (skip adds none)", p.CostUSD)
	}
}

func TestComputeProgress_TerminalUsesTotalCostAndEndTime(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 5, 0, 0, time.UTC) // long after end
	rows := []watchEntry{
		ev("run-1", "pipeline.run.started", "", "2026-07-07T12:00:00Z", nil),
		ev("run-1", "pipeline.step.completed", "parse", "2026-07-07T12:00:10Z", map[string]any{"cost_usd": 0.002}),
		ev("run-1", "pipeline.run.completed", "", "2026-07-07T12:00:20Z", map[string]any{"total_cost_usd": 0.0051}),
	}
	p := computeProgress(rows, 2, "", now)
	if !p.Terminal || p.Status != "completed" {
		t.Errorf("status = %q terminal=%v, want completed/true", p.Status, p.Terminal)
	}
	// Terminal cost prefers the run total, not the step sum.
	if p.CostUSD != 0.0051 {
		t.Errorf("cost = %v, want run total 0.0051", p.CostUSD)
	}
	// Elapsed pins to the end time (20s), not `now`.
	if p.Elapsed != 20*time.Second {
		t.Errorf("elapsed = %v, want 20s (end-anchored)", p.Elapsed)
	}
}

func TestComputeProgress_PicksLatestRunAndRunIDFilter(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 30, 0, time.UTC)
	rows := []watchEntry{
		ev("old", "pipeline.run.completed", "", "2026-07-07T11:00:00Z", nil),
		ev("new", "pipeline.run.started", "", "2026-07-07T12:00:00Z", nil),
	}
	if p := computeProgress(rows, 3, "", now); p == nil || p.RunID != "new" {
		t.Errorf("latest-run pick = %+v, want new", p)
	}
	if p := computeProgress(rows, 3, "old", now); p == nil || p.RunID != "old" {
		t.Errorf("run-id filter pick = %+v, want old", p)
	}
}

func TestComputeProgress_Failed(t *testing.T) {
	now := time.Now()
	rows := []watchEntry{
		ev("r", "pipeline.run.started", "", "2026-07-07T12:00:00Z", nil),
		ev("r", "pipeline.run.failed", "", "2026-07-07T12:00:05Z", nil),
	}
	p := computeProgress(rows, 1, "", now)
	if !p.Terminal || !p.Failed || p.Status != "failed" {
		t.Errorf("failed run = %+v", p)
	}
}
