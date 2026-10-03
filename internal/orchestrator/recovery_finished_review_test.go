package orchestrator

import (
	"errors"
	"log/slog"
	"testing"
)

func TestReconcileAgentActivityIgnoresFinishedControlAwaitingRemoval(t *testing.T) {
	o := New(nil, nil, slog.Default())
	done := make(chan struct{})
	close(done)
	o.agentRuns.Store("finished", &agentRunControl{agentID: "a", done: done})
	if err := o.ReconcileAgentActivity("a", func(active bool) error {
		if active {
			return errors.New("finished registry entry kept agent running")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
