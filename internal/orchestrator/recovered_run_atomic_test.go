package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

// Interleave a completion after a non-atomic read, or before the transaction.
// Both implementations see the same competing writer; only Update preserves it.
type recoveredCompletionState struct {
	provider.StateProvider
	atomic    provider.AtomicStateProvider
	completed []byte
}

func (s recoveredCompletionState) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	raw, err := s.StateProvider.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	if err = s.StateProvider.Set(ctx, bucket, key, s.completed); err != nil {
		return nil, err
	}
	return raw, nil
}
func (s recoveredCompletionState) Update(ctx context.Context, bucket, key string, fn func([]byte) ([]byte, error)) error {
	if err := s.StateProvider.Set(ctx, bucket, key, s.completed); err != nil {
		return err
	}
	return s.atomic.Update(ctx, bucket, key, fn)
}
func TestRecoveredAbsencePreservesConcurrentCompletion(t *testing.T) {
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	expected := RunState{ID: "run", AgentID: "agent", ContainerID: "original", Status: "running"}
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Set(t.Context(), "agent_runs", expected.ID, raw); err != nil {
		t.Fatal(err)
	}
	completed := expected
	completed.Status = "completed"
	completed.ExecID = "final-result"
	done, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := recoveredCompletionState{StateProvider: state, atomic: state, completed: done}
	o := New(nil, wrapped, slog.Default())
	changed, err := o.RecordRecoveredAbsence(t.Context(), expected.ID, expected)
	if err != nil || changed {
		t.Fatalf("completion replaced by recovered absence: changed=%v err=%v", changed, err)
	}
	got, err := state.Get(t.Context(), "agent_runs", expected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(done) {
		t.Fatalf("concurrent completion overwritten: %s", got)
	}
}
func TestRecoveredAbsenceAlreadyDeleted(t *testing.T) {
	state := newMemState()
	o := New(nil, state, slog.Default())
	changed, err := o.RecordRecoveredAbsence(t.Context(), "gone", RunState{ID: "gone", Status: "running"})
	if err != nil || changed {
		t.Fatalf("deleted identity not settled: changed=%v err=%v", changed, err)
	}
	raw, err := state.Get(t.Context(), "agent_runs", "gone")
	if err != nil || raw != nil {
		t.Fatalf("deleted identity recreated: %s %v", raw, err)
	}
}
