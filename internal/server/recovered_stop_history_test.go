package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Pending legacy runs without a known workspace must not repeatedly consume
// the batch budget while actionable stops wait behind them.
func TestRecoveredStopHistoryMakesBoundedProgressPastUnresolvedRuns(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("absent-%03d", i)
		raw, err := json.Marshal(orchestrator.RunState{ID: id, AgentID: "missing-agent", Status: "cancelled", StopJournalPending: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = s.state.Set(t.Context(), "agent_runs", id, raw); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("ready-%03d", i)
		seedRecoveryTrace(t, s, id, "a")
		seedStoppedOutbox(t, s, id)
	}
	// Three batches of 100 cover every one of these 201 pending runs,
	// even when unresolved entries retain their durable pending marker.
	for i := 0; i < 3; i++ {
		if err := s.flushRecoveredStops(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	remaining := 0
	for i := 0; i < 101; i++ {
		if pendingStop(t, s, fmt.Sprintf("ready-%03d", i)) {
			remaining++
		}
	}
	if remaining != 0 {
		t.Fatalf("%d actionable stops starved behind unresolved entries", remaining)
	}
	if !pendingStop(t, s, "absent-000") {
		t.Fatal("invented history for unresolved manual run")
	}
}

func TestRecoveredStopHistoryWithoutStartedEvent(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedStoppedOutbox(t, s, "manual-stop")
	other, err := json.Marshal(orchestrator.RunState{ID: "other-manual", AgentID: "a", WorkspaceID: "rw", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.Set(t.Context(), "agent_runs", "other-manual", other); err != nil {
		t.Fatal(err)
	}
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var agentStatus string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&agentStatus); err != nil {
		t.Fatal(err)
	}
	if agentStatus != "RUNNING" {
		t.Fatalf("other direct run hidden by status %s", agentStatus)
	}
	if pendingStop(t, s, "manual-stop") {
		t.Fatal("confirmed stop without run.started remains pending")
	}
	if n := recoveryTerminalCount(t, s, "manual-stop"); n != 1 {
		t.Fatalf("terminal entries=%d", n)
	}
	var starts int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='manual-stop' AND entry_type='run.started'`).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if starts != 0 {
		t.Fatal("fabricated a start event")
	}
	seedRecoveryTrace(t, s, "manual-stop", "a")
	s.recoverOrphanedRuns(t.Context())
	if n := recoveryTerminalCount(t, s, "manual-stop"); n != 1 {
		t.Fatalf("late start created duplicate terminal entries=%d", n)
	}
}

func TestRecoveredStopHistoryMissingStartScope(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw'),('other','Other','other')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING'),('b','other','Other','b','RUNNING')`)
	for _, tc := range []struct {
		name, agent, workspace string
		conflict               bool
	}{
		{"captured-after-agent-deletion", "gone", "rw", false},
		{"agent-scope-conflict", "a", "other", true},
		{"journal-scope-conflict", "a", "rw", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := orchestrator.RunState{ID: tc.name, AgentID: tc.agent, WorkspaceID: tc.workspace, Status: "cancelled", StopJournalPending: true}
			raw, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			if tc.name == "journal-scope-conflict" {
				mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES('foreign','other','b',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.started','info','sidecar','started','{}','{}',?,'normal')`, run.ID)
			}
			err = s.projectRecoveredStop(t.Context(), run)
			if (err != nil) != tc.conflict {
				t.Fatalf("projection error=%v want conflict=%v", err, tc.conflict)
			}
			if pendingStop(t, s, run.ID) != tc.conflict {
				t.Fatalf("pending marker disagrees with evidence")
			}
			var terminals int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.cancelled'`, run.ID).Scan(&terminals); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.conflict {
				want = 0
			}
			if terminals != want {
				t.Fatalf("terminals=%d want=%d", terminals, want)
			}
		})
	}
}
