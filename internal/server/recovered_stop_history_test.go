package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

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

// Pending manual IPC runs without a journal start must not repeatedly consume
// the batch budget while actionable stops wait behind them.
func TestRecoveredStopHistoryMakesBoundedProgressPastUnresolvedRuns(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	for i := 0; i < 100; i++ {
		seedStoppedOutbox(t, s, fmt.Sprintf("absent-%03d", i))
	}
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("ready-%03d", i)
		seedRecoveryTrace(t, s, id, "a")
		seedStoppedOutbox(t, s, id)
	}
	// Isolate the count/fairness policy from the production five-second
	// budget: a slow -race runner can legitimately exhaust that time budget
	// before consuming 100 entries. The production wrapper keeps its limit.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// Three batches of 100 cover every one of these 201 pending runs,
	// even when unresolved entries retain their durable pending marker.
	for i := 0; i < 3; i++ {
		if err := s.flushRecoveredStopsBatch(ctx); err != nil {
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
	if pendingStop(t, s, "absent-000") {
		t.Fatal("manual stop never acknowledged")
	}
	if n := recoveryTerminalCount(t, s, "absent-000"); n != 0 {
		t.Fatal("invented manual history")
	}
}

// A manual IPC invocation (or a delayed journal start) can have durable runtime
// ownership before its run.started entry exists. Projecting an older stop must
// not advertise STOPPED while that runtime is still owned.
func TestRecoveredStopHistoryPreservesUnjournaledRunningIdentity(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	raw, err := json.Marshal(orchestrator.RunState{ID: "unjournaled", AgentID: "a", AgentSlug: "a", ContainerID: "owned-container", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", "unjournaled", raw); err != nil {
		t.Fatal(err)
	}
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("durable live runtime projected as %s", status)
	}
	if pendingStop(t, s, "stopped") {
		t.Fatal("old stop projection was not acknowledged")
	}
	if n := recoveryTerminalCount(t, s, "unjournaled"); n != 0 {
		t.Fatalf("invented %d terminal entries for live runtime", n)
	}
}

func TestRecoveredStopHistoryRetainsOutboxWhenRuntimeOwnershipIsCorrupt(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	if err := s.state.Set(t.Context(), "agent_runs", "unknown", []byte("{")); err != nil {
		t.Fatal(err)
	}
	if err := s.flushRecoveredStops(t.Context()); err == nil {
		t.Fatal("corrupt runtime ownership accepted")
	}
	if !pendingStop(t, s, "stopped") {
		t.Fatal("unknown ownership lost the retry marker")
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("unknown runtime ownership projected as %s", status)
	}
}

func TestRecoveredStopHistoryExpiredBudgetPreservesPendingWork(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := s.flushRecoveredStops(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired budget: %v", err)
	}
	if !pendingStop(t, s, "stopped") {
		t.Fatal("expired budget lost pending work")
	}
	if n := recoveryTerminalCount(t, s, "stopped"); n != 0 {
		t.Fatalf("expired budget wrote %d terminal entries", n)
	}
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pendingStop(t, s, "stopped") {
		t.Fatal("next drain failed to acknowledge the stop")
	}
}

func (failedStopAcknowledgement) Update(context.Context, string, string, func([]byte) ([]byte, error)) error {
	return errors.New("state acknowledgement unavailable")
}

// A new invocation may publish its durable record after the outbox scan but
// before the old stop projects the agent status, without a journal start yet.
type admitAfterStopScan struct {
	provider.StateProvider
	admit func()
}

func (s *admitAfterStopScan) List(ctx context.Context, bucket string) (map[string][]byte, error) {
	records, err := s.StateProvider.List(ctx, bucket)
	if err == nil && bucket == "agent_runs" && s.admit != nil {
		admit := s.admit
		s.admit = nil
		admit()
	}
	return records, err
}
func (s *admitAfterStopScan) Update(ctx context.Context, bucket, key string, fn func([]byte) ([]byte, error)) error {
	return s.StateProvider.(provider.AtomicStateProvider).Update(ctx, bucket, key, fn)
}
func TestRecoveredStopHistoryPreservesRunAdmittedAfterScan(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	state := s.state
	s.state = &admitAfterStopScan{StateProvider: state, admit: func() {
		raw, err := json.Marshal(orchestrator.RunState{ID: "new", AgentID: "a", Status: "running"})
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Set(t.Context(), "agent_runs", "new", raw); err != nil {
			t.Fatal(err)
		}
	}}
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" {
		t.Fatalf("new invocation hidden by old stop: agent status=%s", status)
	}
	if pendingStop(t, s, "stopped") {
		t.Fatal("old stop not acknowledged")
	}
}
