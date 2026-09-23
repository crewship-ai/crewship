package pipeline

import (
	"testing"
)

func TestReviewedDefinitionDoesNotApplyMutableStepOverrides(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	runner := newMockRunner()
	runner.outputsBySlug["s_a"] = []string{"out-a"}
	runner.outputsBySlug["s_b"] = []string{"out-b"}
	runner.outputsBySlug["s_c"] = []string{"out-c"}
	deps := fullExecutorDeps(t, db, runner)
	p := saveResumePipeline(t, deps.Store, "reviewed-definition", resumeLinearDSL)
	if err := NewStepOverrideStore(db).Set(t.Context(), "ws_test", p.ID, "b", "unreviewed live override", ""); err != nil {
		t.Fatal(err)
	}
	_, err := NewWiredExecutor(deps).Run(t.Context(), RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		UseReviewedDefinition: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		if call.AgentSlug == "s_b" {
			if call.Prompt == "unreviewed live override" {
				t.Fatal("mutable step override changed reviewed definition")
			}
			return
		}
	}
	t.Fatal("reviewed step b did not execute")
}
