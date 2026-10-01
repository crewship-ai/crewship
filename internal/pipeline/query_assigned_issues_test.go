package pipeline

import (
	"context"
	"database/sql"
	"github.com/crewship-ai/crewship/internal/testutil"
	"strings"
	"testing"
	"time"
)

func assignedIssueFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := openResumeTestDB(t)
	t.Cleanup(func() { db.Close() })
	_, err := db.Exec(`ALTER TABLE workspaces ADD COLUMN deleted_at TEXT;
 ALTER TABLE crews ADD COLUMN deleted_at TEXT;
 ALTER TABLE agents ADD COLUMN workspace_id TEXT DEFAULT 'ws_test';
 ALTER TABLE agents ADD COLUMN deleted_at TEXT;
 CREATE TABLE missions(id TEXT PRIMARY KEY,workspace_id TEXT,crew_id TEXT,delegate_agent_id TEXT,mission_type TEXT DEFAULT 'issue',status TEXT);
 CREATE TABLE issue_work(mission_id TEXT PRIMARY KEY,mode TEXT);
 CREATE TABLE mission_relations(source_id TEXT,target_id TEXT,relation_type TEXT);
 CREATE TABLE assignments(mission_id TEXT,status TEXT);
 CREATE TABLE issue_executions(mission_id TEXT,routine_run_id TEXT);
 CREATE TABLE restricted_preflight_reservations(issue_id TEXT,workflow_id TEXT);
 CREATE TABLE restricted_workflow_jobs(id TEXT,state TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAssignedIssues_PreflightScopeAndBlockers(t *testing.T) {
	for _, tc := range []struct {
		name, seed string
		want       bool
	}{
		{"empty", "", false},
		{"private claim", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO');INSERT INTO restricted_preflight_reservations VALUES('own','job');INSERT INTO restricted_workflow_jobs VALUES('job','pending')`, false},
		{"ready", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO')`, true},
		{"other agent", `INSERT INTO missions VALUES('other','ws_test','crew_b','agent_b_lead','issue','TODO')`, false},
		{"foreign workspace", `INSERT INTO missions VALUES('foreign','ws_other','crew_a','agent_lead','issue','TODO')`, false},
		{"foreign crew", `INSERT INTO missions VALUES('foreign','ws_test','crew_b','agent_lead','issue','TODO')`, false},
		{"backlog", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','BACKLOG')`, false},
		{"already running", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','IN_PROGRESS')`, false},
		{"human mode", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO');INSERT INTO issue_work VALUES('own','human')`, false},
		{"blocked", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO'),('blocker','ws_test','crew_a','agent_lead','issue','BACKLOG');INSERT INTO mission_relations VALUES('blocker','own','blocks')`, false},
		{"legacy inverse blocked", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO'),('blocker','ws_test','crew_a','agent_lead','issue','BACKLOG');INSERT INTO mission_relations VALUES('own','blocker','blocked_by')`, false},
		{"resolved blocker", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO'),('blocker','ws_test','crew_a','agent_lead','issue','DONE');INSERT INTO mission_relations VALUES('blocker','own','blocks')`, true},
		{"queued assignment", `INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO');INSERT INTO assignments VALUES('own','QUEUED')`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := assignedIssueFixture(t)
			if tc.seed != "" {
				if _, err := db.Exec(tc.seed); err != nil {
					t.Fatal(err)
				}
			}
			got, err := NewRunStore(db).hasAssignedIssues(context.Background(), "ws_test", "crew_a", "agent_lead")
			if err != nil || got != tc.want {
				t.Fatalf("got %v %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestAssignedIssues_UnknownAuthorityAndDatabaseErrorsAreNotEmptyWork(t *testing.T) {
	db := assignedIssueFixture(t)
	s := NewRunStore(db)
	for _, args := range [][3]string{{"ws_test", "crew_a", ""}, {"ws_other", "crew_a", "agent_lead"}, {"ws_test", "crew_b", "agent_lead"}} {
		if _, err := s.hasAssignedIssues(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatal("unknown authority accepted")
		}
	}
	if _, err := db.Exec(`UPDATE agents SET deleted_at='deleted' WHERE id='agent_lead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.hasAssignedIssues(context.Background(), "ws_test", "crew_a", "agent_lead"); err == nil {
		t.Fatal("deleted agent accepted")
	}
	db.Close()
	if _, err := s.hasAssignedIssues(context.Background(), "ws_test", "crew_a", "agent_lead"); err == nil {
		t.Fatal("database failure became successful no-work")
	}
}

func TestAssignedIssues_EmptyRoutineDoesNotCallAgent(t *testing.T) {
	db := assignedIssueFixture(t)
	runner := &stubAgentRunner{}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	def := `{"name":"work-preflight","steps":[{"id":"check","type":"query","query":{"source":"assigned_issues"}},{"id":"work","type":"agent_run","needs":["check"],"if":"{{ steps.check.output.has_work }}","agent_slug":"agent_lead","prompt":"Review your assigned TODO issues; use the normal issue start mechanism."}]}`
	p := saveResumePipeline(t, deps.Store, "work-preflight", def)
	for _, hasWork := range []bool{false, true} {
		if hasWork {
			if _, err := db.Exec(`INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO')`); err != nil {
				t.Fatal(err)
			}
		}
		res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
		if err != nil || res.Status != "COMPLETED" {
			t.Fatalf("result %+v %v", res, err)
		}
		if !strings.Contains(res.StepOutputs["check"], `"has_work":`) {
			t.Fatal("missing preflight result")
		}
		want := int64(0)
		if hasWork {
			want = 1
		}
		if runner.calls.Load() != want {
			t.Fatalf("agent calls %d want %d", runner.calls.Load(), want)
		}
	}
}

func TestAssignedIssues_RequiresAuthorAtSave(t *testing.T) {
	dsl := &DSL{Name: "preflight", Steps: []Step{{ID: "check", Type: StepQuery, Query: &QueryStep{Source: "assigned_issues"}}}}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCrewshipActingAgent(dsl, false); err == nil {
		t.Fatal("saved preflight without author")
	}
	if err := ValidateCrewshipActingAgent(dsl, true); err != nil {
		t.Fatal(err)
	}
}

func TestAssignedIssues_ActiveRoutineAlreadyOwnsWork(t *testing.T) {
	db := assignedIssueFixture(t)
	seedDigestRun(t, db, "running-issue", "ws_test", "issue-work", "running", 0, time.Now())
	if _, err := db.Exec(`INSERT INTO missions VALUES('own','ws_test','crew_a','agent_lead','issue','TODO');INSERT INTO issue_executions VALUES('own','running-issue')`); err != nil {
		t.Fatal(err)
	}
	got, err := NewRunStore(db).hasAssignedIssues(context.Background(), "ws_test", "crew_a", "agent_lead")
	if err != nil || got {
		t.Fatalf("active routine duplicated: %v %v", got, err)
	}
}

func TestAssignedIssues_PrewarmDoesNotWakeContainerBeforeGate(t *testing.T) {
	db := assignedIssueFixture(t)
	runner := &prewarmRunner{}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "gated-prewarm", `{"name":"gated-prewarm","steps":[{"id":"check","type":"query","query":{"source":"assigned_issues"}},{"id":"work","type":"agent_run","needs":["check"],"if":"{{ steps.check.output.has_work }}","agent_slug":"agent_lead","prompt":"Review work"}]}`)
	exec.PrewarmForRun(context.Background(), p.ID, "ws_test")
	if got := runner.warmed(); len(got) != 0 {
		t.Fatalf("empty preflight woke container: %v", got)
	}
}

func migratedPreflightFixture(t testing.TB) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	_, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('preflight-ws','Preflight','preflight');
 INSERT INTO crews(id,workspace_id,name,slug) VALUES('preflight-crew','preflight-ws','Preflight','preflight');
 INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('preflight-agent','preflight-ws','preflight-crew','Preflight','preflight');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAssignedIssues_RealMigrationSchema(t *testing.T) {
	db := migratedPreflightFixture(t)
	s := NewRunStore(db)
	ctx := context.Background()
	got, err := s.hasAssignedIssues(ctx, "preflight-ws", "preflight-crew", "preflight-agent")
	if err != nil || got {
		t.Fatalf("empty actual schema: %v %v", got, err)
	}
	_, err = db.Exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,delegate_agent_id,trace_id,title,status,mission_type) VALUES('preflight-issue','preflight-ws','preflight-crew','preflight-agent','preflight-agent','preflight-trace','Synthetic','TODO','issue')`)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.hasAssignedIssues(ctx, "preflight-ws", "preflight-crew", "preflight-agent")
	if err != nil || !got {
		t.Fatalf("ready actual schema: %v %v", got, err)
	}
}

func BenchmarkAssignedIssuesPreflight(b *testing.B) {
	db := migratedPreflightFixture(b)
	_, err := db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<5000)
 INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,delegate_agent_id,trace_id,title,status,mission_type)
 SELECT 'history-'||x,'preflight-ws','preflight-crew','preflight-agent','preflight-agent','trace-'||x,'Historical synthetic issue','DONE','issue' FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	s := NewRunStore(db)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.hasAssignedIssues(ctx, "preflight-ws", "preflight-crew", "preflight-agent")
		if err != nil || got {
			b.Fatalf("%v %v", got, err)
		}
	}
}
