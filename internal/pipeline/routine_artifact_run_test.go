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
// changed what the schedule runs and, at this run, still holds that same
// membership with a MANAGER+ role, is trusted, is not suspended and has not
// had their access revoked since (#5339 owner decision A, review round 3). An
// agent-saved or legacy schedule never does; a person saving it again
// restores it. The journal names one generic reason for a person's standing,
// names the schedule only when the host vouched for the fire, and says it
// once per fire.
func TestRoutineArtifactRun_ScheduleCreatorDecidesAtEveryRun(t *testing.T) {
	db := routineArtifactDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('human_1','h1@ex.com','H1'),('human_2','h2@ex.com','H2')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('m1','ws_art','human_1','MANAGER','trusted'),('m2','ws_art','human_2','ADMIN','trusted')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('pipe_art','ws_art','art','art','{}','h'),('pipe_other','ws_art','other','other','{}','h')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	store := NewScheduleStore(db.DB)
	saveTo := func(id, target, kind, user string) *Schedule {
		t.Helper()
		sched, err := store.Save(t.Context(), SaveScheduleInput{ID: id, WorkspaceID: "ws_art", Name: "s", TargetPipelineID: target, CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true, CreatorKind: kind, CreatorUserID: user})
		if err != nil {
			t.Fatal(err)
		}
		return sched
	}
	save := func(id, kind, user string) *Schedule { t.Helper(); return saveTo(id, "pipe_art", kind, user) }
	emitter := &captureEmitter{}
	n := 0
	step := func(req AgentStepRequest) (bool, []journal.Entry) {
		t.Helper()
		n++
		before := len(emitter.entries)
		capability, end := recordRoutineArtifactRun(t.Context(), db.DB, nil, emitter, req, "crew_art", "agent_b", fmt.Sprintf("run_sched_%d", n), 10*time.Minute)
		end()
		return capability != "", emitter.entries[before:]
	}
	fireReq := func(sched *Schedule, vouched bool) AgentStepRequest {
		req := AgentStepRequest{WorkspaceID: "ws_art", PipelineID: "pipe_art", PipelineRunID: fmt.Sprintf("prun_%d", n+1), StepID: "s", TriggeredVia: TriggeredViaSchedule, TriggeredByID: sched.ID}
		if vouched {
			req.ArtifactScheduleID = sched.ID
		}
		return req
	}
	fire := func(sched *Schedule, vouched bool) (bool, []journal.Entry) {
		t.Helper()
		return step(fireReq(sched, vouched))
	}
	reason := func(t *testing.T, entries []journal.Entry, want string) {
		t.Helper()
		if len(entries) != 1 || entries[0].Type != EntryArtifactRunCapabilityWithheld || entries[0].Payload["reason"] != want {
			t.Fatalf("journal %+v, want one %s entry with reason %s", entries, EntryArtifactRunCapabilityWithheld, want)
		}
		for k, v := range entries[0].Payload {
			if k == "detail" || strings.Contains(fmt.Sprint(v), "suspend") || strings.Contains(fmt.Sprint(v), "revoked") || strings.Contains(fmt.Sprint(v), "restricted") {
				t.Fatalf("journal payload %s=%v says more than the generic reason", k, v)
			}
		}
	}
	refused := func(t *testing.T, sched *Schedule, want string) {
		t.Helper()
		ok, entries := fire(sched, true)
		if ok {
			t.Fatalf("got a capability, want refusal %s", want)
		}
		reason(t, entries, want)
		if entries[0].Payload["schedule_id"] != sched.ID {
			t.Fatalf("a vouched fire's journal names schedule %v, want %s", entries[0].Payload["schedule_id"], sched.ID)
		}
	}
	admitted := func(t *testing.T, sched *Schedule, why string) {
		t.Helper()
		if ok, entries := fire(sched, true); !ok || len(entries) != 0 {
			t.Fatalf("%s: capability=%v journal=%+v", why, ok, entries)
		}
	}

	human := save("", ScheduleCreatorHuman, "human_1")
	if human.CreatorKind != ScheduleCreatorHuman || human.CreatorUserID != "human_1" || human.CreatorSetAt == "" {
		t.Fatalf("creator not stored: %+v", human)
	}
	admitted(t, human, "a person's schedule fire")
	var trigger, source string
	if err := db.QueryRow(`SELECT trigger_type,source_ref FROM artifact_unattended_runs WHERE run_id='run_sched_1'`).Scan(&trigger, &source); err != nil || trigger != "SCHEDULED" || source != "schedule:"+human.ID+"/routine:pipe_art/s" {
		t.Fatalf("recorded as %q %q %v", trigger, source, err)
	}

	t.Run("an unvouched claim is refused and its claimed schedule is not journaled", func(t *testing.T) {
		ok, entries := fire(human, false)
		if ok {
			t.Fatal("a schedule attribution the host did not vouch for got a capability")
		}
		reason(t, entries, withheldScheduleUnverified)
		if _, named := entries[0].Payload["schedule_id"]; named {
			t.Fatalf("an unverified fire's journal names the caller's schedule id: %+v", entries[0].Payload)
		}
	})
	t.Run("the marker must be the schedule the fire claims", func(t *testing.T) {
		other := save("", ScheduleCreatorHuman, "human_1")
		req := fireReq(human, true)
		req.ArtifactScheduleID = other.ID
		ok, entries := step(req)
		if ok {
			t.Fatal("a fire marked for another schedule got a capability")
		}
		reason(t, entries, withheldScheduleUnverified)
	})
	t.Run("a schedule that targets another routine is refused", func(t *testing.T) {
		refused(t, saveTo("", "pipe_other", ScheduleCreatorHuman, "human_1"), withheldScheduleTarget)
	})
	t.Run("an agent invoker or an agent author is refused on the schedule path", func(t *testing.T) {
		for name, edit := range map[string]func(*AgentStepRequest){
			"agent invoker": func(r *AgentStepRequest) { r.InvokingAgentID = "agent_a" },
			"agent author":  func(r *AgentStepRequest) { r.AuthorAgentID = "agent_lead" },
		} {
			req := fireReq(human, true)
			edit(&req)
			if ok, _ := step(req); ok {
				t.Fatalf("%s: a vouched person's schedule fire got a capability", name)
			}
		}
	})
	t.Run("an agent's or an unknown creator vouches for nothing", func(t *testing.T) {
		agent := save("", ScheduleCreatorAgent, "human_2")
		if agent.CreatorUserID != "" {
			t.Fatalf("an agent's save kept a user id: %+v", agent)
		}
		refused(t, agent, withheldScheduleCreatorAgent)
		legacy := save("", "", "")
		refused(t, legacy, withheldScheduleCreatorLegacy)
		save(legacy.ID, ScheduleCreatorHuman, "human_2")
		admitted(t, legacy, "a person's re-save")
		save(legacy.ID, ScheduleCreatorAgent, "")
		refused(t, legacy, withheldScheduleCreatorAgent)
	})
	t.Run("suspended", func(t *testing.T) {
		mustExec(t, db.DB, `UPDATE users SET suspended_at=datetime('now') WHERE id='human_1'`)
		refused(t, human, withheldScheduleStanding)
		mustExec(t, db.DB, `UPDATE users SET suspended_at=NULL WHERE id='human_1'`)
		admitted(t, human, "unsuspended")
	})
	t.Run("access revoked after the save, not before", func(t *testing.T) {
		revokeAt := func(at time.Time) {
			t.Helper()
			if _, err := db.Exec(`UPDATE users SET access_revoked_at=? WHERE id='human_1'`, at.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
		}
		revokeAt(time.Now().UTC().Add(time.Second))
		refused(t, human, withheldScheduleStanding)
		revokeAt(time.Now().UTC().Add(-time.Hour))
		admitted(t, human, "a revocation older than the save")
		mustExec(t, db.DB, `UPDATE users SET access_revoked_at=NULL WHERE id='human_1'`)
	})
	t.Run("a role that can no longer create schedules", func(t *testing.T) {
		mustExec(t, db.DB, `UPDATE workspace_members SET role='MEMBER' WHERE id='m1'`)
		refused(t, human, withheldScheduleStanding)
		mustExec(t, db.DB, `UPDATE workspace_members SET role='OWNER' WHERE id='m1'`)
		admitted(t, human, "an owner")
		mustExec(t, db.DB, `UPDATE workspace_members SET role='MANAGER' WHERE id='m1'`)
	})
	t.Run("a restricted member", func(t *testing.T) {
		mustExec(t, db.DB, `UPDATE workspace_members SET access_mode='restricted' WHERE id='m1'`)
		refused(t, human, withheldScheduleStanding)
		mustExec(t, db.DB, `UPDATE workspace_members SET access_mode='trusted' WHERE id='m1'`)
	})
	t.Run("leaving, and leaving and rejoining", func(t *testing.T) {
		mustExec(t, db.DB, `DELETE FROM workspace_members WHERE id='m1'`)
		refused(t, human, withheldScheduleStanding)
		mustExec(t, db.DB, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('m1b','ws_art','human_1','MANAGER','trusted')`)
		refused(t, human, withheldScheduleStanding)
		save(human.ID, ScheduleCreatorHuman, "human_1")
		admitted(t, human, "a save by the rejoined member")
	})
	t.Run("one journal entry per fire, not per step", func(t *testing.T) {
		mustExec(t, db.DB, `UPDATE users SET suspended_at=datetime('now') WHERE id='human_1'`)
		req := fireReq(human, true)
		_, first := step(req)
		req.StepID = "s2"
		_, second := step(req)
		if len(first) != 1 || len(second) != 0 {
			t.Fatalf("one fire, two refused steps journaled %d and %d entries, want 1 and 0", len(first), len(second))
		}
		mustExec(t, db.DB, `UPDATE users SET suspended_at=NULL WHERE id='human_1'`)
	})
	t.Run("flag off: nothing is read, recorded or journaled", func(t *testing.T) {
		var logs strings.Builder
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		mustExec(t, db.DB, `ALTER TABLE pipeline_schedules RENAME TO pipeline_schedules_away`)
		defer mustExec(t, db.DB, `ALTER TABLE pipeline_schedules_away RENAME TO pipeline_schedules`)
		probe := func() (string, []journal.Entry) {
			logs.Reset()
			before := len(emitter.entries)
			n++
			capability, end := recordRoutineArtifactRun(t.Context(), db.DB, logger, emitter, fireReq(human, true), "crew_art", "agent_b", fmt.Sprintf("run_sched_%d", n), 10*time.Minute)
			end()
			if capability != "" {
				t.Fatal("a capability without its schedule table")
			}
			return logs.String(), emitter.entries[before:]
		}
		if out, _ := probe(); !strings.Contains(out, "admission failed") {
			t.Fatalf("flag on, the probe must reach the schedule table; log %q", out)
		}
		mustExec(t, db.DB, `UPDATE feature_flag_overrides SET enabled=0 WHERE id='art-flag'`)
		if out, entries := probe(); out != "" || len(entries) != 0 {
			t.Fatalf("flag off: log %q journal %+v", out, entries)
		}
	})
}

// The cron scheduler vouches for its own fire, so the step sees the schedule
// it belongs to; a nested call_pipeline step of that fire does not.
func TestRoutineArtifactRun_SchedulerVouchesForItsOwnFire(t *testing.T) {
	db := openPinningTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS workspace_members (id TEXT PRIMARY KEY, workspace_id TEXT, user_id TEXT, role TEXT, access_mode TEXT)`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('m_human','ws_test','human','MANAGER','trusted')`)
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
	if got.CreatorKind != ScheduleCreatorHuman || got.CreatorUserID != "human" {
		t.Fatalf("creator not stored: %+v", got)
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
