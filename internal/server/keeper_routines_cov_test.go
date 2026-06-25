package server

// Coverage tests for keeper_routines.go: the scheduler registration glue
// (registerKeeperPhase2Routines), the two daily sweeps
// (runSkillReviewSweep / runMemoryHealthSweep) end-to-end against a real
// migrated SQLite, and the SQL persisters' direct write paths.
//
// The LLM behind the evaluators is a canned static provider, the same
// pattern internal/keeper/routines/routines_test.go uses, so the sweep
// decision branches are deterministic and offline.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/skills"
)

// covStaticLLM returns the same canned response for every Complete call.
type covStaticLLM struct {
	content string
}

func (s *covStaticLLM) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{Content: s.content}, nil
}
func (s *covStaticLLM) Stream(ctx context.Context, req llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := s.Complete(ctx, req)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (s *covStaticLLM) Name() string { return "cov-static" }

func covSkillEval(content string) *gatekeeper.SkillReviewEvaluator {
	gk := gatekeeper.New(&covStaticLLM{content: content}, "test-model", krLogger())
	return gatekeeper.NewSkillReviewEvaluator(gk, krLogger())
}

func covMemEval(content string) *gatekeeper.MemoryHealthEvaluator {
	gk := gatekeeper.New(&covStaticLLM{content: content}, "test-model", krLogger())
	return gatekeeper.NewMemoryHealthEvaluator(gk, krLogger())
}

func covScheduler(t *testing.T, db *sql.DB) *scheduler.Scheduler {
	t.Helper()
	sched := scheduler.New(db, nil, nil, nil, nil, nil, scheduler.Config{}, krLogger())
	t.Cleanup(sched.Stop)
	return sched
}

func TestRegisterKeeperPhase2Routines_NilSchedOrDB(t *testing.T) {
	t.Parallel()
	db := krDB(t)

	skillReg, memReg := registerKeeperPhase2Routines(nil, db, covSkillEval(`{}`), covMemEval(`{}`), krLogger())
	if skillReg || memReg {
		t.Errorf("nil scheduler: got (%v,%v), want (false,false)", skillReg, memReg)
	}

	sched := covScheduler(t, db)
	skillReg, memReg = registerKeeperPhase2Routines(sched, nil, covSkillEval(`{}`), covMemEval(`{}`), krLogger())
	if skillReg || memReg {
		t.Errorf("nil db: got (%v,%v), want (false,false)", skillReg, memReg)
	}
}

func TestRegisterKeeperPhase2Routines_PerEvaluatorIndependence(t *testing.T) {
	t.Parallel()
	db := krDB(t)

	cases := []struct {
		name      string
		skill     *gatekeeper.SkillReviewEvaluator
		mem       *gatekeeper.MemoryHealthEvaluator
		wantSkill bool
		wantMem   bool
	}{
		{"both evaluators", covSkillEval(`{}`), covMemEval(`{}`), true, true},
		{"skill only", covSkillEval(`{}`), nil, true, false},
		{"memory only", nil, covMemEval(`{}`), false, true},
		{"neither", nil, nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sched := covScheduler(t, db)
			skillReg, memReg := registerKeeperPhase2Routines(sched, db, tc.skill, tc.mem, krLogger())
			if skillReg != tc.wantSkill || memReg != tc.wantMem {
				t.Errorf("got (%v,%v), want (%v,%v)", skillReg, memReg, tc.wantSkill, tc.wantMem)
			}
		})
	}
}

// covSeedSkillFixtures inserts the workspace → crew → agent → skill →
// agent_skills chain plus one skill_invocations row inside the 30-day
// lookback, so loadSkillSweepInputs exercises its stats aggregation.
func covSeedSkillFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_cov','WS','ws-cov')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_cov','ws_cov','C','c-cov')`)
	mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag_cov','cr_cov','ws_cov','A','a-cov')`)
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name, description, lifecycle_state, last_used_at)
	                 VALUES ('sk_cov','cov skill','cov-skill','Cov Skill','does things','active',?)`, now)
	mustExec(t, db, `INSERT INTO agent_skills (agent_id, skill_id, enabled) VALUES ('ag_cov','sk_cov',1)`)
	mustExec(t, db, `INSERT INTO skill_invocations (id, skill_id, agent_id, workspace_id, invoked_at, exit_code)
	                 VALUES ('inv_cov','sk_cov','ag_cov','ws_cov',?,1)`, now)
}

func TestRunSkillReviewSweep_AllowMarksSkillVerified(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedSkillFixtures(t, db)

	runSkillReviewSweep(context.Background(), db,
		covSkillEval(`{"decision":"ALLOW","reason":"actively used","risk":1}`), krLogger())

	var verification string
	if err := db.QueryRow(`SELECT verification FROM skills WHERE id = 'sk_cov'`).Scan(&verification); err != nil {
		t.Fatalf("read verification: %v", err)
	}
	if verification != "VERIFIED" {
		t.Errorf("verification = %q, want VERIFIED after ALLOW sweep", verification)
	}
}

func TestRunSkillReviewSweep_EmptyCatalogIsNoOp(t *testing.T) {
	t.Parallel()
	db := krDB(t)

	runSkillReviewSweep(context.Background(), db,
		covSkillEval(`{"decision":"ALLOW","reason":"n/a","risk":1}`), krLogger())

	var inboxRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&inboxRows); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if inboxRows != 0 {
		t.Errorf("inbox rows = %d, want 0 on empty catalog", inboxRows)
	}
}

func TestRunSkillReviewSweep_LoadErrorAborts(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedSkillFixtures(t, db)
	_ = db.Close() // force loadSkillSweepInputs to fail

	// Contract: a load failure logs and returns without panicking — the
	// daily cron must survive a transiently broken DB.
	runSkillReviewSweep(context.Background(), db,
		covSkillEval(`{"decision":"ALLOW","reason":"n/a","risk":1}`), krLogger())
}

func TestSqlSkillPersister_VerificationAndLifecycleWrites(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_p','p','p','P')`)
	pers := &sqlSkillPersister{db: db, logger: krLogger()}
	ctx := context.Background()

	readSkill := func() (verification, lifecycle string) {
		t.Helper()
		if err := db.QueryRow(`SELECT verification, lifecycle_state FROM skills WHERE id = 'sk_p'`).
			Scan(&verification, &lifecycle); err != nil {
			t.Fatalf("read skill: %v", err)
		}
		return
	}

	if err := pers.MarkVerified(ctx, "sk_p"); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	if v, _ := readSkill(); v != "VERIFIED" {
		t.Errorf("after MarkVerified: verification = %q, want VERIFIED", v)
	}

	if err := pers.MarkUnverified(ctx, "sk_p"); err != nil {
		t.Fatalf("MarkUnverified: %v", err)
	}
	if v, _ := readSkill(); v != "UNVERIFIED" {
		t.Errorf("after MarkUnverified: verification = %q, want UNVERIFIED", v)
	}

	if err := pers.SetLifecycle(ctx, "sk_p", skills.LifecycleStale, "unused for 60d"); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if _, lc := readSkill(); lc != "stale" {
		t.Errorf("after SetLifecycle: lifecycle_state = %q, want stale", lc)
	}

	// Idempotent: setting the same state again is a no-op, not an error.
	if err := pers.SetLifecycle(ctx, "sk_p", skills.LifecycleStale, "again"); err != nil {
		t.Fatalf("SetLifecycle (idempotent): %v", err)
	}
	if _, lc := readSkill(); lc != "stale" {
		t.Errorf("idempotent SetLifecycle changed state: %q", lc)
	}
}

// covSeedCrews inserts two crews in two workspaces so
// loadMemoryHealthScopes returns one scope per (workspace, crew).
func covSeedCrews(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_m1','M1','ws-m1')`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_m2','M2','ws-m2')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_m1','ws_m1','Ops','ops-m')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_m2','ws_m2','Dev','dev-m')`)
}

func TestLoadMemoryHealthScopes_OneScopePerCrew(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedCrews(t, db)
	// A crew whose workspace_id is empty makes consolidate.ComputeHealth
	// fail ("workspace_id required") — the loader must skip it instead
	// of aborting the sweep.
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('','empty','ws-empty')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_bad','','Bad','bad-m')`)

	scopes, err := loadMemoryHealthScopes(context.Background(), db, krLogger())
	if err != nil {
		t.Fatalf("loadMemoryHealthScopes: %v", err)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes = %d, want 2", len(scopes))
	}
	// Ordered by workspace_id, crew id.
	if scopes[0].WorkspaceID != "ws_m1" || scopes[0].CrewID != "cr_m1" || scopes[0].CrewName != "Ops" {
		t.Errorf("scope[0] = %+v, want ws_m1/cr_m1/Ops", scopes[0])
	}
	if scopes[1].WorkspaceID != "ws_m2" || scopes[1].CrewID != "cr_m2" || scopes[1].CrewName != "Dev" {
		t.Errorf("scope[1] = %+v, want ws_m2/cr_m2/Dev", scopes[1])
	}
}

func TestRunMemoryHealthSweep_DenyTriggersConsolidationInbox(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedCrews(t, db)

	runMemoryHealthSweep(context.Background(), db,
		covMemEval(`{"decision":"DENY","reason":"memory bloated","risk":6}`), krLogger())

	// DENY (no contradictions) → AutoConsolidate → one memory_consolidation
	// inbox row per crew, in the crew's own workspace.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items
		WHERE kind = 'memory_consolidation' AND sender_id = 'keeper_memory_health_routine'`).Scan(&rows); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if rows != 2 {
		t.Errorf("memory_consolidation inbox rows = %d, want 2 (one per crew)", rows)
	}
	var ws string
	if err := db.QueryRow(`SELECT workspace_id FROM inbox_items
		WHERE kind = 'memory_consolidation' AND source_id LIKE 'memory_health_cr_m1_%'`).Scan(&ws); err != nil {
		t.Fatalf("read cr_m1 inbox row: %v", err)
	}
	if ws != "ws_m1" {
		t.Errorf("cr_m1 consolidation inbox workspace = %q, want ws_m1", ws)
	}
}

func TestRunMemoryHealthSweep_EscalateWritesAdvisoryInbox(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedCrews(t, db)

	runMemoryHealthSweep(context.Background(), db,
		covMemEval(`{"decision":"ESCALATE","reason":"mixed signals","risk":5}`), krLogger())

	// The memory-health advisory is a system notification (kind=message),
	// NOT an escalation — it has no escalations-table row behind it, so as
	// kind=escalation it could never be cleared. One per crew.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items
		WHERE kind = 'message' AND source_id LIKE 'memory_health_advisory_%'`).Scan(&rows); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if rows != 2 {
		t.Errorf("advisory inbox rows = %d, want 2 (one per crew)", rows)
	}
	var body string
	if err := db.QueryRow(`SELECT body_md FROM inbox_items
		WHERE source_id LIKE 'memory_health_advisory_cr_m2_%'`).Scan(&body); err != nil {
		t.Fatalf("read cr_m2 advisory row: %v", err)
	}
	if !strings.Contains(body, "mixed signals") {
		t.Errorf("advisory body = %q, want the evaluator reason", body)
	}
}

func TestRunMemoryHealthSweep_NoCrewsIsNoOp(t *testing.T) {
	t.Parallel()
	db := krDB(t)

	runMemoryHealthSweep(context.Background(), db,
		covMemEval(`{"decision":"DENY","reason":"n/a","risk":5}`), krLogger())

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&rows); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if rows != 0 {
		t.Errorf("inbox rows = %d, want 0 with no crews", rows)
	}
}

func TestRunMemoryHealthSweep_LoadErrorAborts(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	_ = db.Close()

	// Same fail-soft contract as the skill sweep: log + return.
	runMemoryHealthSweep(context.Background(), db,
		covMemEval(`{"decision":"ALLOW","reason":"n/a","risk":1}`), krLogger())
}

func TestSqlMemoryHealthPersister_DirectWrites(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	covSeedCrews(t, db)
	pers := &sqlMemoryHealthPersister{db: db, logger: krLogger()}
	ctx := context.Background()

	if err := pers.TriggerConsolidation(ctx, "ws_m1", "cr_m1", "too many dailies"); err != nil {
		t.Fatalf("TriggerConsolidation: %v", err)
	}
	var kind, title string
	if err := db.QueryRow(`SELECT kind, title FROM inbox_items WHERE workspace_id = 'ws_m1'`).
		Scan(&kind, &title); err != nil {
		t.Fatalf("read consolidation row: %v", err)
	}
	if kind != "memory_consolidation" || title != "Memory consolidation suggested" {
		t.Errorf("row = (%q,%q), want memory_consolidation / suggestion title", kind, title)
	}

	// The advisory is written as a non-blocking message regardless of the
	// blocking arg — there is no decision to block on, and a blocking row
	// would be (correctly) protected from bulk-clear, re-creating the
	// pile-up. So even called with blocking=true it lands non-blocking.
	if err := pers.WriteInboxItem(ctx, "ws_m2", "cr_m2", "contradictions found", true); err != nil {
		t.Fatalf("WriteInboxItem: %v", err)
	}
	var kind2 string
	var blocking int
	if err := db.QueryRow(`SELECT kind, blocking FROM inbox_items
		WHERE workspace_id = 'ws_m2' AND sender_id = 'keeper_memory_health_routine'`).
		Scan(&kind2, &blocking); err != nil {
		t.Fatalf("read advisory row: %v", err)
	}
	if kind2 != "message" {
		t.Errorf("advisory kind = %q, want message (not escalation)", kind2)
	}
	if blocking != 0 {
		t.Errorf("blocking = %d, want 0 (advisory is a non-blocking notification)", blocking)
	}

	// inbox.Insert failures must propagate (regression guard mirrors the
	// CodeRabbit pin in keeper_routines_test.go, here for the memory
	// persister): a bogus workspace violates the FK and the error
	// surfaces instead of being swallowed.
	if err := pers.TriggerConsolidation(ctx, "ws_missing", "cr_x", "nope"); err == nil {
		t.Error("TriggerConsolidation with unknown workspace: want FK error, got nil")
	}
	if err := pers.WriteInboxItem(ctx, "ws_missing", "cr_x", "nope", false); err == nil {
		t.Error("WriteInboxItem with unknown workspace: want FK error, got nil")
	}
}
