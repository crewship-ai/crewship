package main

import (
	"strings"
	"testing"
)

func TestAcceptance_RoutineExecutionWaitAndWork(t *testing.T) {
	cfg, db := startRoutineDetailAcceptanceServer(t)
	saveRoutineDetailV1(t, cfg)
	out, err := runRoutineDetailCLI(t, cfg, "routine", "run", "acceptance-detail", "--inputs", `{"who":"timing"}`)
	if err != nil {
		t.Fatalf("run: %v %s", err, out)
	}
	match := routineDetailRunIDRE.FindStringSubmatch(out)
	if len(match) < 2 {
		t.Fatal(out)
	}
	runID := match[1]
	// Synthetic timing on a leaf attempt; no inference or container is needed.
	if _, err := db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at,ended_at,agent_wait_ms) VALUES ('timed_attempt',?,'agent','/greet/agent',1,'agent_attempt','completed','2026-11-11T00:00:00Z','2026-11-11T00:00:05Z',3000)`, runID); err != nil {
		t.Fatal(err)
	}
	table, err := runRoutineDetailCLI(t, cfg, "routine", "executions", runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WAIT", "WORK", "3s", "2s"} {
		if !strings.Contains(table, want) {
			t.Fatalf("missing %s: %s", want, table)
		}
	}
	jsonOut, err := runRoutineDetailCLI(t, cfg, "routine", "executions", runID, "-f", "json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"agent_wait_ms": 3000`, `"work_duration_ms": 2000`, `"agent_wait_ms": null`} {
		if !strings.Contains(jsonOut, want) {
			t.Fatalf("missing %s: %s", want, jsonOut)
		}
	}
}
