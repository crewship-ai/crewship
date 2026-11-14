package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
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
		"agent invoker":                        {func(r *AgentStepRequest) { r.InvokingAgentID = "agent_a" }, false},
		"agent-authored routine a person runs": {func(r *AgentStepRequest) { r.AuthorAgentID = "agent_lead" }, false},
		"schedule": {func(r *AgentStepRequest) {
			r.InvokingUserID, r.InvocationAuthority, r.TriggeredVia = "", "", TriggeredViaSchedule
		}, false},
		"schedule claiming a person": {func(r *AgentStepRequest) { r.TriggeredVia = TriggeredViaSchedule }, false},
		"schedule attribution without the host's fire": {func(r *AgentStepRequest) {
			r.TriggeredVia, r.TriggeredByID = TriggeredViaSchedule, "sched_human"
		}, false},
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
			capability, end := recordRoutineArtifactRun(t.Context(), db.DB, nil, nil, req, "crew_art", "agent_b", "run_"+strings.ReplaceAll(name, " ", "_"), 10*time.Minute)
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
		if c, _ := recordRoutineArtifactRun(t.Context(), db.DB, nil, nil, req, "crew_art", "agent_b", runID, timeout); c == "" {
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

// The executor hands each step the run's real starter, trigger and author
// (round-2 review): a schedule (including a person's "run now" of one), a
// person's nested call_pipeline step and an agent-authored routine reach the
// runner as what they are, and only the person's own manual run records.
func TestRoutineArtifactRun_ExecutorCarriesTheStarter(t *testing.T) {
	const childDSL = `{"dsl_version":"1.0","name":"artifact-child","steps":[{"id":"c1","type":"agent_run","agent_slug":"s_c","prompt":"child"}]}`
	const parentDSL = `{"dsl_version":"1.0","name":"artifact-parent","steps":[{"id":"call","type":"call_pipeline","pipeline_slug":"artifact-child"}]}`
	cases := []struct {
		name        string
		dsl         string
		author      string
		in          RunInput
		wantVia     TriggeredVia
		wantRecords bool
	}{
		{"manual run by a person", resumeLinearDSL, "", RunInput{InvokingUserID: "human", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual}, TriggeredViaManual, true},
		{"schedule", resumeLinearDSL, "", RunInput{TriggeredVia: TriggeredViaSchedule, TriggeredByID: "sched_1"}, TriggeredViaSchedule, false},
		{"schedule run now by a person", resumeLinearDSL, "", RunInput{InvokingUserID: "human", InvocationAuthority: RoutineBatchAuthority, TriggeredVia: TriggeredViaSchedule, TriggeredByID: "sched_1"}, TriggeredViaSchedule, false},
		{"person's run, nested call_pipeline step", parentDSL, "", RunInput{InvokingUserID: "human", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual}, TriggeredViaCallPipeline, false},
		{"agent-authored routine a person runs", resumeLinearDSL, "agent_lead", RunInput{InvokingUserID: "human", InvocationAuthority: RoutineRunAuthority, TriggeredVia: TriggeredViaManual}, TriggeredViaManual, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openFactoryTestDB(t)
			defer db.Close()
			mustExec(t, db, `INSERT INTO users(id) VALUES('human')`)
			mustExec(t, db, `CREATE TABLE workspace_members (workspace_id TEXT, user_id TEXT, access_mode TEXT NOT NULL DEFAULT 'trusted', role TEXT NOT NULL DEFAULT 'MANAGER', capabilities TEXT)`)
			mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id) VALUES ('ws_test','human')`)
			mock := newMockRunner()
			deps := fullExecutorDeps(t, db, mock)
			child := fakePipeline(t, "artifact-child", childDSL, "crew_a", "", deps.Store)
			p := fakePipeline(t, "artifact-routine", tc.dsl, "crew_a", tc.author, deps.Store)
			exec := NewWiredExecutor(deps).WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
				if slug == "artifact-child" {
					return child, nil
				}
				return nil, ErrNotFound
			}))
			in := tc.in
			in.PipelineID, in.WorkspaceID, in.Mode = p.ID, "ws_test", ModeRun
			if res, err := exec.Run(t.Context(), in); err != nil || res.Status != "COMPLETED" {
				t.Fatalf("run: %+v %v", res, err)
			}
			if len(mock.calls) == 0 {
				t.Fatal("no step ran")
			}
			adb := routineArtifactDB(t)
			for i, c := range mock.calls {
				if c.TriggeredVia != tc.wantVia || c.InvokingUserID != tc.in.InvokingUserID || c.InvocationAuthority != tc.in.InvocationAuthority || c.AuthorAgentID != tc.author || c.InvokingAgentID != "" {
					t.Fatalf("step %d reached the runner as %+v", i, c)
				}
				c.WorkspaceID = "ws_art" // the recording fixture's flagged workspace
				capability, end := recordRoutineArtifactRun(t.Context(), adb.DB, nil, nil, c, "crew_art", "agent_b", fmt.Sprintf("run_%d", i), 10*time.Minute)
				end()
				if (capability != "") != tc.wantRecords {
					t.Fatalf("step %d: capability=%v, want %v", i, capability != "", tc.wantRecords)
				}
			}
		})
	}
}

// A schedule's own fire reaches artifacts only when a person created or last
// saved the schedule and is, at this run, still a member and not suspended
// (#5339 owner decision A). An agent-saved or legacy schedule never does; a
// person saving it again restores it; flag off records and journals nothing.
func TestRoutineArtifactRun_ScheduleCreatorDecidesAtEveryRun(t *testing.T) {
	db := routineArtifactDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('human_1','h1@ex.com','H1'),('human_2','h2@ex.com','H2')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m1','ws_art','human_1','MEMBER'),('m2','ws_art','human_2','MEMBER')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('pipe_art','ws_art','art','art','{}','h')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	store := NewScheduleStore(db.DB)
	save := func(id, kind, user string) *Schedule {
		t.Helper()
		sched, err := store.Save(t.Context(), SaveScheduleInput{ID: id, WorkspaceID: "ws_art", Name: "s", TargetPipelineID: "pipe_art", CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true, CreatorKind: kind, CreatorUserID: user})
		if err != nil {
			t.Fatal(err)
		}
		return sched
	}
	emitter := &captureEmitter{}
	n := 0
	fire := func(sched *Schedule, vouched bool) (bool, []journal.Entry) {
		t.Helper()
		n++
		req := AgentStepRequest{WorkspaceID: "ws_art", PipelineID: "pipe_art", PipelineRunID: "prun", StepID: "s", TriggeredVia: TriggeredViaSchedule, TriggeredByID: sched.ID}
		if vouched {
			req.ArtifactScheduleID = sched.ID
		}
		before := len(emitter.entries)
		capability, end := recordRoutineArtifactRun(t.Context(), db.DB, nil, emitter, req, "crew_art", "agent_b", fmt.Sprintf("run_sched_%d", n), 10*time.Minute)
		end()
		return capability != "", emitter.entries[before:]
	}
	reason := func(t *testing.T, entries []journal.Entry, want string) {
		t.Helper()
		if len(entries) != 1 || entries[0].Type != EntryArtifactRunCapabilityWithheld || entries[0].Payload["reason"] != want {
			t.Fatalf("journal %+v, want one %s entry with reason %s", entries, EntryArtifactRunCapabilityWithheld, want)
		}
	}

	human := save("", ScheduleCreatorHuman, "human_1")
	if human.CreatorKind != ScheduleCreatorHuman || human.CreatorUserID != "human_1" {
		t.Fatalf("creator not stored: %+v", human)
	}
	if ok, entries := fire(human, true); !ok || len(entries) != 0 {
		t.Fatalf("a person's schedule fire: capability=%v journal=%+v", ok, entries)
	}
	var trigger, source string
	if err := db.QueryRow(`SELECT trigger_type,source_ref FROM artifact_unattended_runs WHERE run_id='run_sched_1'`).Scan(&trigger, &source); err != nil || trigger != "SCHEDULED" || source != "schedule:"+human.ID+"/routine:pipe_art/s" {
		t.Fatalf("recorded as %q %q %v", trigger, source, err)
	}
	if ok, entries := fire(human, false); ok {
		t.Fatal("a schedule attribution the host did not vouch for got a capability")
	} else {
		reason(t, entries, withheldScheduleUnverified)
	}

	agent := save("", ScheduleCreatorAgent, "human_2")
	if agent.CreatorUserID != "" {
		t.Fatalf("an agent's save kept a user id: %+v", agent)
	}
	ok, entries := fire(agent, true)
	if ok {
		t.Fatal("an agent-saved schedule got a capability")
	}
	reason(t, entries, withheldScheduleCreatorAgent)

	legacy := save("", "", "")
	ok, entries = fire(legacy, true)
	if ok {
		t.Fatal("a schedule with no recorded creator got a capability")
	}
	reason(t, entries, withheldScheduleCreatorLegacy)
	save(legacy.ID, ScheduleCreatorHuman, "human_2")
	if ok, _ := fire(legacy, true); !ok {
		t.Fatal("a person's re-save did not restore the schedule")
	}
	save(legacy.ID, ScheduleCreatorAgent, "")
	if ok, _ := fire(legacy, true); ok {
		t.Fatal("an agent's re-save of a person's schedule kept its access")
	}

	mustExec(t, db.DB, `UPDATE users SET suspended_at=datetime('now') WHERE id='human_1'`)
	ok, entries = fire(human, true)
	if ok {
		t.Fatal("a suspended creator's schedule got a capability")
	}
	reason(t, entries, withheldScheduleCreatorFrozen)
	mustExec(t, db.DB, `UPDATE users SET suspended_at=NULL WHERE id='human_1'`)
	mustExec(t, db.DB, `DELETE FROM workspace_members WHERE id='m1'`)
	ok, entries = fire(human, true)
	if ok {
		t.Fatal("a creator who left the workspace still vouches")
	}
	reason(t, entries, withheldScheduleCreatorGone)

	mustExec(t, db.DB, `UPDATE feature_flag_overrides SET enabled=0 WHERE id='art-flag'`)
	mustExec(t, db.DB, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m1b','ws_art','human_1','MEMBER')`)
	if ok, entries := fire(human, true); ok || len(entries) != 0 {
		t.Fatalf("flag off: capability=%v journal=%+v", ok, entries)
	}
	if ok, entries := fire(agent, true); ok || len(entries) != 0 {
		t.Fatalf("flag off, agent schedule: capability=%v journal=%+v", ok, entries)
	}
}

// The cron scheduler vouches for its own fire, so the step sees the schedule
// it belongs to; a nested call_pipeline step of that fire does not.
func TestRoutineArtifactRun_SchedulerVouchesForItsOwnFire(t *testing.T) {
	db := openPinningTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	mock := newMockRunner()
	deps := fullExecutorDeps(t, db, mock)
	store := NewScheduleStore(db)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scheduler := NewPipelineScheduler(store, deps.Store, NewWiredExecutor(deps), logger)
	p := fakePipeline(t, "artifact-cron", resumeLinearDSL, "crew_a", "", deps.Store)
	sched, err := store.Save(t.Context(), SaveScheduleInput{WorkspaceID: "ws_test", Name: "cron", TargetPipelineID: p.ID, CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true, CreatorKind: ScheduleCreatorHuman, CreatorUserID: "human"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByID(t.Context(), sched.ID)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(-time.Minute).UTC()
	got.NextRunAt = &due
	scheduler.fireOne(t.Context(), got)
	if len(mock.calls) == 0 {
		t.Fatal("no step ran")
	}
	for _, c := range mock.calls {
		if c.TriggeredVia != TriggeredViaSchedule || c.TriggeredByID != sched.ID || c.ArtifactScheduleID != sched.ID {
			t.Fatalf("scheduled step reached the runner as %+v", c)
		}
	}
}
