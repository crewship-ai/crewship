package pipeline

import "testing"

func TestApplyStepOverrides(t *testing.T) {
	steps := []Step{
		{ID: "a", Type: StepAgentRun, Prompt: "authored A", ModelOverride: "smart"},
		{ID: "b", Type: StepAgentRun, Prompt: "authored B"},
	}
	applyStepOverrides(steps, map[string]StepOverride{
		"a":       {Prompt: "patched A"},   // prompt only — keep authored model
		"b":       {ModelOverride: "fast"}, // model only — keep authored prompt
		"missing": {Prompt: "ignored"},     // no such step — no-op
	})
	if steps[0].Prompt != "patched A" {
		t.Errorf("step a prompt: got %q", steps[0].Prompt)
	}
	if steps[0].ModelOverride != "smart" {
		t.Errorf("step a model should stay authored, got %q", steps[0].ModelOverride)
	}
	if steps[1].Prompt != "authored B" {
		t.Errorf("step b prompt should stay authored, got %q", steps[1].Prompt)
	}
	if steps[1].ModelOverride != "fast" {
		t.Errorf("step b model: got %q", steps[1].ModelOverride)
	}
}

func TestApplyStepOverrides_EmptyIsNoOp(t *testing.T) {
	steps := []Step{{ID: "a", Prompt: "x"}}
	applyStepOverrides(steps, nil)
	if steps[0].Prompt != "x" {
		t.Fatal("nil overrides must be a no-op")
	}
}
