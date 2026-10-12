package orchestrator

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

func TestPersistStoppedRunAlreadyDeleted(t *testing.T) {
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	o := New(nil, state, slog.Default())
	if err := o.persistStoppedRunWithOrigin(t.Context(), RunState{ID: "deleted", AgentID: "a", Status: "running"}, "agent_stop"); err != nil {
		t.Fatalf("already removed run failed stop: %v", err)
	}
	raw, err := state.Get(t.Context(), "agent_runs", "deleted")
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatal("stop recreated deleted state")
	}
}
