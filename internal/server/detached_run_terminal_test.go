package server

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func seedDetachedGoneOutbox(t *testing.T, s *Server, id string) {
	t.Helper()
	raw, err := json.Marshal(orchestrator.RunState{ID: id, AgentID: "a", WorkspaceID: "rw", Status: "error",
		StopJournalPending: true, StopOrigin: "detached_confirmed_gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", id, raw); err != nil {
		t.Fatal(err)
	}
}

func terminalOf(t *testing.T, s *Server, id string) (entryType, reason string) {
	t.Helper()
	if err := s.journalWriter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT entry_type, COALESCE(json_extract(payload,'$.reason'),'') FROM journal_entries
 WHERE trace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, id).Scan(&entryType, &reason); err != nil {
		t.Fatal(err)
	}
	return entryType, reason
}

// #5511: a detached exec its hold confirmed gone was left RUNNING in the run
// list for ever. The durable outbox now projects it as run.failed.
func TestDetachedConfirmedGoneProjectsFailedRun(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "detached", "a")
	seedDetachedGoneOutbox(t, s, "detached")
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pendingStop(t, s, "detached") {
		t.Fatal("projected detached terminal not acknowledged")
	}
	if n := recoveryTerminalCount(t, s, "detached"); n != 1 {
		t.Fatalf("terminal entries = %d, want 1", n)
	}
	if kind, reason := terminalOf(t, s, "detached"); kind != "run.failed" || reason != "detached_exec_confirmed_gone" {
		t.Fatalf("terminal = %s/%s, want run.failed/detached_exec_confirmed_gone", kind, reason)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("agent status = %s, want IDLE", status)
	}
	// A second drain is idempotent.
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := recoveryTerminalCount(t, s, "detached"); n != 1 {
		t.Fatalf("terminal entries after second drain = %d, want 1", n)
	}
}

// A caller that already recorded the run's outcome keeps it.
func TestDetachedConfirmedGoneKeepsRecordedOutcome(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','IDLE')`)
	seedRecoveryTrace(t, s, "detached", "a")
	mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES('done','rw','a',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.timeout','warn','system','timeout','{}','{}','detached','normal')`)
	seedDetachedGoneOutbox(t, s, "detached")
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pendingStop(t, s, "detached") {
		t.Fatal("existing terminal not acknowledged")
	}
	if kind, _ := terminalOf(t, s, "detached"); kind != "run.timeout" {
		t.Fatalf("recorded outcome replaced by %s", kind)
	}
}

// Boot recovery must leave a pending detached projection to the outbox
// instead of cancelling the trace as a server-restart orphan.
func TestBootRecoveryLeavesDetachedProjectionToOutbox(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "detached", "a")
	seedDetachedGoneOutbox(t, s, "detached")
	s.recoverOrphanedRuns(t.Context())
	if n := recoveryTerminalCount(t, s, "detached"); n != 1 {
		t.Fatalf("terminal entries = %d, want 1", n)
	}
	if kind, reason := terminalOf(t, s, "detached"); kind != "run.failed" || reason != "detached_exec_confirmed_gone" {
		t.Fatalf("boot recovery wrote %s/%s, want the detached projection", kind, reason)
	}
}
