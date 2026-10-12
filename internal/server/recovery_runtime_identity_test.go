package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestRecoveryPreservesDurableRunningAgent(t *testing.T) {
	s := newTestServerWithDeps(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s.state = state
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('recover-w','Recovery','recovery')`)
	for _, id := range []string{"live", "orphan"} {
		mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES(?,'recover-w',?,?,'RUNNING')`, id, id, id)
		mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES(?,'recover-w',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.started','info','sidecar','started','{}','{}',?,'normal')`, "j-"+id, id, "trace-"+id)
	}
	raw, err := json.Marshal(orchestrator.RunState{ID: "trace-live", AgentID: "live", AgentSlug: "live", ContainerID: "container-live", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", "trace-live", raw); err != nil {
		t.Fatal(err)
	}
	s.recoverOrphanedRuns(t.Context())
	if err := s.journalWriter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='live'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("unconfirmed runtime reported %s", status)
	}
	var canceled int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE agent_id='live' AND entry_type='run.cancelled'`).Scan(&canceled); err != nil {
		t.Fatal(err)
	}
	if canceled != 0 {
		t.Fatal("recovery invented process cancellation")
	}
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='orphan'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("journal-only orphan was not recovered: %s", status)
	}
}

func TestAgentStatusFindsRunIdentityAndPrefersActive(t *testing.T) {
	s := newTestServerWithDeps(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s.state = state
	for _, run := range []orchestrator.RunState{
		{ID: "run-active", AgentID: "a", Status: "running", StartedAt: time.Now().Add(-time.Hour)},
		{ID: "run-newer", AgentID: "a", Status: "completed", StartedAt: time.Now()},
		{ID: "run-other", AgentID: "b", Status: "running", StartedAt: time.Now()},
	} {
		raw, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("GET", "/agents/a/status", nil)
	out := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(out, req)
	var got orchestrator.RunState
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || got.ID != "run-active" || got.Status != "running" {
		t.Fatalf("active run hidden: %d %s", out.Code, out.Body.String())
	}
}

func seedRecoveryTrace(t *testing.T, s *Server, id, agent string) {
	t.Helper()
	mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES(?,'rw',?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.started','info','sidecar','started','{}','{}',?,'normal')`, "j-"+id, agent, id)
}
func recoveryTerminalCount(t *testing.T, s *Server, id string) int {
	t.Helper()
	if err := s.journalWriter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestRecoveryUsesExactRunIdentityWithLegacyFallback(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "run-keyed", true: "legacy-agent-keyed"}[legacy], func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			seedRecoveryTrace(t, s, "live-trace", "a")
			seedRecoveryTrace(t, s, "old-trace", "a")
			id := "live-trace"
			if legacy {
				id = "a"
			}
			raw, err := json.Marshal(orchestrator.RunState{ID: id, AgentID: "a", Status: "running"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.state.Set(t.Context(), "agent_runs", id, raw); err != nil {
				t.Fatal(err)
			}
			s.recoverOrphanedRuns(t.Context())
			if n := recoveryTerminalCount(t, s, "live-trace"); n != 0 {
				t.Fatalf("live run terminalized: %d", n)
			}
			want := 1
			if legacy {
				want = 0
			}
			if n := recoveryTerminalCount(t, s, "old-trace"); n != want {
				t.Fatalf("old trace terminals=%d want=%d", n, want)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "RUNNING" {
				t.Fatalf("live agent=%s", status)
			}
			s.recoverOrphanedRuns(t.Context())
			if n := recoveryTerminalCount(t, s, "old-trace"); n != want {
				t.Fatalf("repeat recovery terminals=%d want=%d", n, want)
			}
		})
	}
}
func TestRecoveryLeavesWorkOwnedRunToDispatcher(t *testing.T) {
	for _, withStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("started-%v", withStart), func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			store := work.NewStore(s.db)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := store.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: "rw", AgentID: "a", Source: work.SourceWebhook, Class: work.ClassBackground}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			attempt, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "old-server"})
			if err != nil {
				t.Fatal(err)
			}
			if attempt == nil {
				t.Fatal("no claim")
			}
			if withStart {
				seedRecoveryTrace(t, s, attempt.RunID, "a")
			}
			// Even a provider that confirms absence must not let generic startup
			// recovery settle a durable work attempt owned by its dispatcher.
			c := &recoveredProbeContainer{mockContainer: &mockContainer{}, answer: "ABSENT"}
			s.container = c
			s.orchestrator = orchestrator.New(c, s.state, s.logger)
			raw, err := json.Marshal(orchestrator.RunState{ID: attempt.RunID, AgentID: "a", AgentSlug: "a", ContainerID: "runtime", Status: "running"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.state.Set(t.Context(), "agent_runs", attempt.RunID, raw); err != nil {
				t.Fatal(err)
			}
			s.recoverOrphanedRuns(t.Context())
			raw, err = s.state.Get(t.Context(), "agent_runs", attempt.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var running orchestrator.RunState
			if err := json.Unmarshal(raw, &running); err != nil || running.Status != "running" {
				t.Fatalf("generic recovery changed work-owned runtime: %+v %v", running, err)
			}
			seedStoppedOutbox(t, s, attempt.RunID)
			if err := s.flushRecoveredStops(t.Context()); err != nil {
				t.Fatal(err)
			}
			if pendingStop(t, s, attempt.RunID) {
				t.Fatal("dispatcher-owned projection marker never acknowledged")
			}
			s.recoverOrphanedRuns(t.Context())
			// Owner decision: a missing-start confirmed stop leaves one
			// audit, but never settles the dispatcher's outcome.
			wantHistory := 0
			if !withStart {
				wantHistory = 1
			}
			if n := recoveryTerminalCount(t, s, attempt.RunID); n != wantHistory {
				t.Fatalf("recovery history=%d want=%d", n, wantHistory)
			}
			projection, owned, err := store.RunProjection(t.Context(), attempt.RunID)
			if err != nil || !owned || projection.Ready {
				t.Fatalf("work projection changed: %+v %v %v", projection, owned, err)
			}
		})
	}
}
