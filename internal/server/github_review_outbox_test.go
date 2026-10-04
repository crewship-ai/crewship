package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

func TestReviewManualStopWithoutOwnershipRetainsMarker(t *testing.T) {
	s := newTestServerWithDeps(t)
	seedStoppedOutbox(t, s, "manual")
	// Without an existing agent/workspace this legacy record cannot establish
	// ownership. Recovery must retain it for reconciliation, not discard it.
	if err := s.flushRecoveredStops(t.Context()); err == nil {
		t.Fatal("unconfirmed manual stop ownership accepted")
	}
	if !pendingStop(t, s, "manual") {
		t.Fatal("unconfirmed manual stop lost its retry marker")
	}
	if n := recoveryTerminalCount(t, s, "manual"); n != 0 {
		t.Fatal("invented manual history")
	}
}
func TestReviewOldPendingStopDoesNotProtectUnrelatedOrphan(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedStoppedOutbox(t, s, "old-manual")
	seedRecoveryTrace(t, s, "new-orphan", "a")
	s.recoverOrphanedRuns(t.Context())
	var status string
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("old marker protects unrelated orphan: %s", status)
	}
}

// Inject a terminal writer after the old acknowledgement's Get, or immediately
// before the new transaction. The acknowledgement must preserve that writer.
type reviewAckRaceState struct {
	provider.StateProvider
	atomic provider.AtomicStateProvider
	newer  []byte
}

func (s reviewAckRaceState) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	raw, err := s.StateProvider.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	if err := s.StateProvider.Set(ctx, bucket, key, s.newer); err != nil {
		return nil, err
	}
	return raw, nil
}
func (s reviewAckRaceState) Update(ctx context.Context, bucket, key string, fn func([]byte) ([]byte, error)) error {
	if err := s.StateProvider.Set(ctx, bucket, key, s.newer); err != nil {
		return err
	}
	return s.atomic.Update(ctx, bucket, key, fn)
}
func TestReviewStopAcknowledgementPreservesConcurrentTerminal(t *testing.T) {
	s := newTestServerWithDeps(t)
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	s.state = state
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	newer, _ := json.Marshal(orchestrator.RunState{ID: "stopped", AgentID: "a", Status: "completed", ExecID: "newer-result", StopJournalPending: true})
	s.state = reviewAckRaceState{StateProvider: state, atomic: state, newer: newer}
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := state.Get(t.Context(), "agent_runs", "stopped")
	if err != nil {
		t.Fatal(err)
	}
	var got orchestrator.RunState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.ExecID != "newer-result" || got.StopJournalPending {
		t.Fatalf("concurrent outcome overwritten: %+v", got)
	}
}

func TestReviewCorruptKnownOwnerDoesNotBlockOtherStops(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "stopped", "a")
	seedStoppedOutbox(t, s, "stopped")
	if err := s.state.Set(t.Context(), "agent_runs", "broken", []byte(`{"agent_id":"other","started_at":42}`)); err != nil {
		t.Fatal(err)
	}
	// The malformed row is still reported; it must not starve healthy owners.
	if err := s.flushRecoveredStops(t.Context()); err == nil {
		t.Fatal("corruption not reported")
	}
	if pendingStop(t, s, "stopped") {
		t.Fatal("other agent's corruption blocked projection")
	}
}

func TestReviewRecoveredAbsenceIsNotUserStop(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	seedRecoveryTrace(t, s, "gone", "a")
	raw, _ := json.Marshal(orchestrator.RunState{ID: "gone", AgentID: "a", ContainerID: "old", AgentSlug: "a", Status: "running"})
	if err := s.state.Set(t.Context(), "agent_runs", "gone", raw); err != nil {
		t.Fatal(err)
	}
	s.container = reviewRecoveryContainer{state: "stopped"}
	s.orchestrator = orchestrator.New(s.container, s.state, s.logger)
	s.recoverOrphanedRuns(t.Context())
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var origin, status string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.metadata.stop_origin') FROM journal_entries WHERE id='recovered-stop:gone'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != "recovered_absence" {
		t.Fatalf("restart absence invented stop origin %q", origin)
	}
	if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "IDLE" {
		t.Fatalf("restart absence projected %s", status)
	}
}
