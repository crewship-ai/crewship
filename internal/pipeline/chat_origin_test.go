package pipeline

import (
	"context"
	"fmt"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"testing"
)

func TestRoutineOperationKeyDistinguishesForeachItemsAndSurvivesRetry(t *testing.T) {
	first := context.WithValue(context.Background(), executionContextKey{}, executionContext{id: "attempt-one", path: "/loop/items/0/write"})
	retry := context.WithValue(context.Background(), executionContextKey{}, executionContext{id: "attempt-two", path: "/loop/items/0/write"})
	second := context.WithValue(context.Background(), executionContextKey{}, executionContext{id: "attempt-three", path: "/loop/items/1/write"})
	if routineOperationKey(first, "run", "write") != routineOperationKey(retry, "run", "write") {
		t.Fatal("retry changed key")
	}
	if routineOperationKey(first, "run", "write") == routineOperationKey(second, "run", "write") {
		t.Fatal("different foreach items collapse")
	}
}

func TestRoutineChildUnconfirmedDetachedRunIsNotRecordedTerminal(t *testing.T) {
	resolver := &orchCovResolver{}
	runner := &OrchestratorRunner{resolver: resolver}
	if err := runner.recordChildTerminal(t.Context(), "child", fmt.Errorf("wrapped: %w", orchestrator.ErrDetachedStillRunning)); err != nil {
		t.Fatal(err)
	}
	if len(resolver.terminalStatuses) != 0 {
		t.Fatal("unconfirmed detached run ended")
	}
	if err := runner.recordChildTerminal(t.Context(), "child", nil); err != nil {
		t.Fatal(err)
	}
	if len(resolver.terminalStatuses) != 1 || resolver.terminalStatuses[0] != "COMPLETED" {
		t.Fatal("confirmed completion missing")
	}
}
