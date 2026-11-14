package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// #5339 review: a routine step gets an artifact run capability only when a
// person started the routine directly. An agent starting a saved routine
// (call_pipeline, /internal/pipelines/run), a schedule (whoever created it),
// a webhook or an automation never does: otherwise agent A could borrow
// agent B's standing access and a fresh read cap per call (§7.1c item 8).
func routineArtifactDB(t *testing.T) *database.DB {
	t.Helper()
	db := testutil.MigratedDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws_art','Art','art')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus) VALUES('crew_art','ws_art','Crew','crew-art','free',4096,2.0)`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('agent_b','ws_art','crew_art','B','agent-b','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('art-flag','ff_artifact_agent_access_modes','ws_art',1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	return db
}

func TestRoutineArtifactRun_OnlyAPersonStartedRoutineGetsACapability(t *testing.T) {
	human := AgentStepRequest{WorkspaceID: "ws_art", PipelineID: "p", StepID: "s", InvokingUserID: "user_1", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual}
	cases := map[string]struct {
		edit func(*AgentStepRequest)
		want bool
	}{
		"person, manual":          {func(*AgentStepRequest) {}, true},
		"person, batch":           {func(r *AgentStepRequest) { r.InvocationAuthority = RoutineBatchAuthority }, true},
		"person, default trigger": {func(r *AgentStepRequest) { r.TriggeredVia = "" }, true},
		"agent call_pipeline": {func(r *AgentStepRequest) {
			r.InvokingUserID, r.InvocationAuthority, r.InvokingAgentID, r.TriggeredVia = "", "", "agent_a", TriggeredViaCallPipeline
		}, false},
		"nested call_pipeline of a person": {func(r *AgentStepRequest) {
			r.InvokingAgentID, r.TriggeredVia = "agent_author", TriggeredViaCallPipeline
		}, false},
		"call_pipeline carrying a person and admission": {func(r *AgentStepRequest) { r.TriggeredVia = TriggeredViaCallPipeline }, false},
		"agent invoker": {func(r *AgentStepRequest) { r.InvokingAgentID = "agent_a" }, false},
		"schedule": {func(r *AgentStepRequest) {
			r.InvokingUserID, r.InvocationAuthority, r.TriggeredVia = "", "", TriggeredViaSchedule
		}, false},
		"schedule claiming a person": {func(r *AgentStepRequest) { r.TriggeredVia = TriggeredViaSchedule }, false},
		"webhook": {func(r *AgentStepRequest) {
			r.InvokingUserID, r.InvocationAuthority, r.TriggeredVia = "", "", TriggeredViaWebhook
		}, false},
		"automation": {func(r *AgentStepRequest) {
			r.InvokingUserID, r.InvocationAuthority, r.TriggeredVia = "", "", TriggeredViaAutomation
		}, false},
		"wake check":                 {func(r *AgentStepRequest) { r.TriggeredVia = TriggeredViaWakeCheck }, false},
		"person without admission":   {func(r *AgentStepRequest) { r.InvocationAuthority = "" }, false},
		"admission without a person": {func(r *AgentStepRequest) { r.InvokingUserID = "" }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := routineArtifactDB(t)
			req := human
			tc.edit(&req)
			capability, end := recordRoutineArtifactRun(t.Context(), db.DB, nil, req, "crew_art", "agent_b", "run_"+strings.ReplaceAll(name, " ", "_"), 10*time.Minute)
			defer end()
			var rows int
			if err := db.QueryRow(`SELECT count(*) FROM artifact_unattended_runs`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if got := capability != ""; got != tc.want || (rows == 1) != tc.want {
				t.Fatalf("capability=%v rows=%d, want %v", got, rows, tc.want)
			}
		})
	}
}

// The capability lives as long as the run may (its timeout plus slack, at
// most the 2-hour bound), not a fixed guess.
func TestRoutineArtifactRun_TTLFollowsTheRunTimeout(t *testing.T) {
	db := routineArtifactDB(t)
	req := AgentStepRequest{WorkspaceID: "ws_art", InvokingUserID: "user_1", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual}
	for runID, timeout := range map[string]time.Duration{"run_ttl_90m": 90 * time.Minute, "run_ttl_zero": 0, "run_ttl_5h": 5 * time.Hour} {
		if c, _ := recordRoutineArtifactRun(t.Context(), db.DB, nil, req, "crew_art", "agent_b", runID, timeout); c == "" {
			t.Fatal(runID, "not recorded")
		}
	}
	for runID, want := range map[string]time.Duration{"run_ttl_90m": 95 * time.Minute, "run_ttl_zero": 2 * time.Hour, "run_ttl_5h": 2 * time.Hour} {
		var created, expires string
		if err := db.QueryRow(`SELECT created_at,expires_at FROM artifact_unattended_runs WHERE run_id=?`, runID).Scan(&created, &expires); err != nil {
			t.Fatal(err)
		}
		c, _ := time.Parse(time.RFC3339Nano, created)
		e, _ := time.Parse(time.RFC3339Nano, expires)
		if got := e.Sub(c); got != want {
			t.Fatalf("%s ttl %s, want %s", runID, got, want)
		}
	}
}

// The executor copies who started the run onto each step request, so the
// runner can tell a person's run from an agent's call_pipeline.
func TestRoutineArtifactRun_ExecutorCarriesTheStarter(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id) VALUES('human')`)
	mustExec(t, db, `CREATE TABLE workspace_members (workspace_id TEXT, user_id TEXT, access_mode TEXT NOT NULL DEFAULT 'trusted', role TEXT NOT NULL DEFAULT 'MANAGER', capabilities TEXT)`)
	mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id) VALUES ('ws_test','human')`)
	mock := newMockRunner()
	deps := fullExecutorDeps(t, db, mock)
	p := saveResumePipeline(t, deps.Store, "artifact-starter", resumeLinearDSL)
	in := RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", InvokingUserID: "human", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual, Mode: ModeRun}
	if res, err := NewWiredExecutor(deps).Run(t.Context(), in); err != nil || res.Status != "COMPLETED" {
		t.Fatalf("run: %+v %v", res, err)
	}
	if len(mock.calls) == 0 {
		t.Fatal("no step ran")
	}
	for _, c := range mock.calls {
		if c.InvokingUserID != "human" || c.InvocationAuthority != RoutineRunAuthority || c.TriggeredVia != TriggeredViaManual || c.InvokingAgentID != "" {
			t.Fatalf("step request lost its starter: %+v", c)
		}
	}
}
