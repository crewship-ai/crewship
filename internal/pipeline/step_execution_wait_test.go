package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/journal"
)

type waitSignalEmitter struct{ waiting chan struct{} }

func (s *waitSignalEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	if e.Payload["phase"] == "waiting" {
		close(s.waiting)
	}
	return "wait-test", nil
}

func (s *waitSignalEmitter) Flush(context.Context) error { return nil }

type delayedWaitResolver struct{ chatbridge.ChatResolver }

func (r delayedWaitResolver) ResolveAgent(ctx context.Context, id, ws string) (*chatbridge.ChatInfo, error) {
	time.Sleep(25 * time.Millisecond)
	return r.ChatResolver.ResolveAgent(ctx, id, ws)
}

func TestRecordedAgentSeparatesBusyWaitFromWork(t *testing.T) {
	for _, outcome := range []string{"acquired", "timed_out", "cancelled", "free"} {
		t.Run(outcome, func(t *testing.T) {
			_, db := openRunsTestDB(t)
			defer db.Close()
			installExecutionSchema(t, db)
			if _, err := db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES ('run-busy','ws_runs','pln_a','test','running','2026-09-08T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
			store := NewExecutionStore(db)
			ctx, parent, err := store.start(context.Background(), "run-busy", "items", "foreach", "", "")
			if err != nil {
				t.Fatal(err)
			}
			r, lock := busyAgentRig(t)
			defer lock.End("agent_cov")
			r.resolver = delayedWaitResolver{r.resolver}
			waiting := make(chan struct{})
			r.journalE = &waitSignalEmitter{waiting: waiting}
			ctx = foreachExecutionContext(ctx, 2)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			switch outcome {
			case "acquired":
				go func() { <-waiting; time.Sleep(50 * time.Millisecond); lock.End("agent_cov") }()
			case "timed_out":
				r.agentBusyWait = 30 * time.Millisecond
			case "cancelled":
				go func() { <-waiting; time.Sleep(30 * time.Millisecond); cancel() }()
			case "free":
				lock.End("agent_cov")
			}
			e := &Executor{executionStore: store, runner: r}
			res, runErr := e.runRecordedAgent(ctx, busyAgentStep)
			if runErr == nil {
				t.Fatal("expected resolver error or wait cancellation")
			}
			var wait int64
			var start, end, status, path string
			if err := db.QueryRow(`SELECT agent_wait_ms,started_at,ended_at,status,execution_path FROM pipeline_step_executions WHERE parent_execution_id=?`, parent).Scan(&wait, &start, &end, &status, &path); err != nil {
				t.Fatal(err)
			}
			if wait != res.AgentWaitMs {
				t.Fatalf("stored wait %d != runner %d", wait, res.AgentWaitMs)
			}
			if outcome == "free" && wait != 0 {
				t.Fatalf("free wait = %d", wait)
			}
			if outcome != "free" && wait < 20 {
				t.Fatalf("busy wait = %d", wait)
			}
			a, _ := time.Parse(time.RFC3339Nano, start)
			b, _ := time.Parse(time.RFC3339Nano, end)
			work := b.Sub(a).Milliseconds() - wait
			if work < 0 {
				t.Fatalf("negative work %d", work)
			}
			if (outcome == "acquired" || outcome == "free") && work < 25 {
				t.Fatalf("work missing: %d", work)
			}
			if path != "/items/items/2/agent" {
				t.Fatalf("nested identity %s", path)
			}
			if outcome == "cancelled" && status != "cancelled" {
				t.Fatalf("status %s", status)
			}
			var parentWait any
			if err := db.QueryRow(`SELECT agent_wait_ms FROM pipeline_step_executions WHERE id=?`, parent).Scan(&parentWait); err != nil {
				t.Fatal(err)
			}
			if parentWait != nil {
				t.Fatal("parent must not claim child wait again")
			}
		})
	}
}

func TestRecordedAgentPanicKeepsWaitUnknown(t *testing.T) {
	_, db := openRunsTestDB(t)
	defer db.Close()
	installExecutionSchema(t, db)
	if _, err := db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES ('run-busy','ws_runs','pln_a','test','running','2026-09-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := NewExecutionStore(db)
	ctx, parent, err := store.start(context.Background(), "run-busy", "work", "agent_run", "", "")
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{executionStore: store, runner: runnerFunc(func(context.Context, AgentStepRequest) (AgentStepResult, error) { panic("lost result") })}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic swallowed")
			}
		}()
		_, _ = e.runRecordedAgent(ctx, busyAgentStep)
	}()
	var wait any
	if err := db.QueryRow(`SELECT agent_wait_ms FROM pipeline_step_executions WHERE parent_execution_id=?`, parent).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Fatalf("panic invented wait: %v", wait)
	}
}

func TestExecutionWaitMigrationPreservesLegacyRows(t *testing.T) {
	_, db := openRunsTestDB(t)
	defer db.Close()
	old, err := os.ReadFile("../database/migrations/20260908104844_pipeline_step_executions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES ('run-busy','ws_runs','pln_a','test','running','2026-09-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pipeline_step_executions(id,run_id,step_id,execution_path,attempt,kind,status,started_at) VALUES ('legacy','run-busy','agent','/agent',1,'agent_attempt','completed','2026-09-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261111104000_pipeline_execution_agent_wait.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var wait any
	if err = db.QueryRow(`SELECT agent_wait_ms FROM pipeline_step_executions WHERE id='legacy'`).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait != nil {
		t.Fatalf("legacy wait invented: %v", wait)
	}
	if _, err = db.Exec(`UPDATE pipeline_step_executions SET agent_wait_ms=-1 WHERE id='legacy'`); err == nil {
		t.Fatal("negative wait accepted")
	}
}
