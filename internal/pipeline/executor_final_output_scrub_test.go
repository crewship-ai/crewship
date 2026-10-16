package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecutorFinalOutputScrubbedBeforePersistence(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatal(err)
	}
	store := NewRunStore(db)
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "final-output-scrub", WorkspaceID: "ws_test", PipelineID: "pln_x", PipelineSlug: "x", Status: RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	secret := "ghp_" + strings.Repeat("a", 36)
	result := &RunResult{Status: "COMPLETED", Output: "final " + secret, StepOutputs: map[string]string{"answer": "step " + secret}}
	executor := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil).WithRunStore(store)
	executor.persistRunTerminal(ctx, "final-output-scrub", RunInput{}, "pln_x", result, time.Now())
	record, err := store.Get(ctx, "final-output-scrub")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(record.Output, secret) || !strings.Contains(record.Output, "REDACTED") {
		t.Fatalf("stored final output not scrubbed: %q", record.Output)
	}
	outputs, err := store.GetStepOutputs(ctx, "final-output-scrub")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outputs["answer"], secret) {
		t.Fatal("stored step output not scrubbed")
	}
	if !strings.Contains(result.Output, secret) || !strings.Contains(result.StepOutputs["answer"], secret) {
		t.Fatal("in-memory execution output was modified")
	}
}
