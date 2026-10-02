package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

type failedStopAcknowledgement struct{ provider.StateProvider }

func (failedStopAcknowledgement) Set(context.Context, string, string, []byte) error {
	return errors.New("state acknowledgement unavailable")
}

func seedStoppedOutbox(t *testing.T, s *Server, id string) {
	t.Helper()
	raw, err := json.Marshal(orchestrator.RunState{ID: id, AgentID: "a", Status: "cancelled", StopJournalPending: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", id, raw); err != nil {
		t.Fatal(err)
	}
}
func pendingStop(t *testing.T, s *Server, id string) bool {
	t.Helper()
	raw, err := s.state.Get(t.Context(), "agent_runs", id)
	if err != nil {
		t.Fatal(err)
	}
	var run orchestrator.RunState
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	return run.StopJournalPending
}
func TestRecoveredStopHistoryRetriesJournalAndAcknowledgementFailures(t *testing.T) {
	for _, failure := range []string{"journal", "acknowledgement", "agent_projection"} {
		t.Run(failure, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			statePath := filepath.Join(t.TempDir(), "runtime.db")
			state, err := bbolt.New(statePath)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			s.state = state
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			seedRecoveryTrace(t, s, "stopped", "a")
			seedStoppedOutbox(t, s, "stopped")
			switch failure {
			case "journal":
				mustExec(t, s.db, `CREATE TRIGGER fail_stop BEFORE INSERT ON journal_entries WHEN NEW.entry_type='run.cancelled' BEGIN SELECT RAISE(FAIL,'journal unavailable'); END`)
			case "agent_projection":
				mustExec(t, s.db, `CREATE TRIGGER fail_agent BEFORE UPDATE ON agents BEGIN SELECT RAISE(FAIL,'agent projection unavailable'); END`)
			case "acknowledgement":
				s.state = failedStopAcknowledgement{state}
			}
			if err := s.flushRecoveredStops(t.Context()); err == nil {
				t.Fatal("failed projection acknowledged")
			}
			if !pendingStop(t, s, "stopped") {
				t.Fatal("retry lost")
			}
			if failure == "journal" {
				s.recoverOrphanedRuns(t.Context())
				if n := recoveryTerminalCount(t, s, "stopped"); n != 0 {
					t.Fatal("generic startup recovery bypassed pending stop projection")
				}
			}
			switch failure {
			case "journal":
				mustExec(t, s.db, `DROP TRIGGER fail_stop`)
			case "agent_projection":
				mustExec(t, s.db, `DROP TRIGGER fail_agent`)
			}
			// Close and reopen the durable state, discarding process-local state.
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := bbolt.New(statePath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			resumed := &Server{state: reopened, db: s.db, journalWriter: s.journalWriter}
			if err := resumed.flushRecoveredStops(t.Context()); err != nil {
				t.Fatal(err)
			}
			if pendingStop(t, resumed, "stopped") {
				t.Fatal("successful projection not acknowledged")
			}
			if n := recoveryTerminalCount(t, s, "stopped"); n != 1 {
				t.Fatalf("terminal entries=%d", n)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "STOPPED" {
				t.Fatalf("agent status=%s", status)
			}
		})
	}
}
func TestRecoveredStopHistoryPreservesOtherActiveRun(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedRecoveryTrace(t, s, "other", "a")
	seedStoppedOutbox(t, s, "stopped")
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := recoveryTerminalCount(t, s, "other"); n != 0 {
		t.Fatalf("other run terminalized: %d", n)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("agent status=%s", status)
	}
}

func TestRecoveredStopHistoryPreservesRecordedCompletion(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','IDLE')`)
	seedRecoveryTrace(t, s, "finished", "a")
	seedStoppedOutbox(t, s, "finished")
	mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES('done','rw','a',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.completed','info','sidecar','completed','{}','{}','finished','normal')`)
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pendingStop(t, s, "finished") {
		t.Fatal("existing terminal not acknowledged")
	}
	if n := recoveryTerminalCount(t, s, "finished"); n != 1 {
		t.Fatalf("completion overwritten: %d terminals", n)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("normal completion changed to %s", status)
	}
}
