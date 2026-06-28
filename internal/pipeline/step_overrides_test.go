package pipeline

import "testing"

func TestApplyStepOverrides(t *testing.T) {
	steps := []Step{
		{ID: "a", Type: StepAgentRun, Prompt: "authored A", ModelOverride: "claude-opus-4-8"},
		{ID: "b", Type: StepAgentRun, Prompt: "authored B"},
		{ID: "c", Type: StepAgentRun, Prompt: "authored C", ModelOverride: "claude-opus-4-8"},
	}
	applyStepOverrides(steps, map[string]StepOverride{
		"a":       {Prompt: "patched A"},               // prompt only — keep authored model
		"b":       {ModelOverride: "fast"},             // TIER → Complexity, not ModelOverride
		"c":       {ModelOverride: "claude-haiku-4-5"}, // concrete model id → ModelOverride
		"missing": {Prompt: "ignored"},                 // no such step — no-op
	})
	if steps[0].Prompt != "patched A" {
		t.Errorf("step a prompt: got %q", steps[0].Prompt)
	}
	if steps[0].ModelOverride != "claude-opus-4-8" {
		t.Errorf("step a model should stay authored, got %q", steps[0].ModelOverride)
	}
	if steps[1].Prompt != "authored B" {
		t.Errorf("step b prompt should stay authored, got %q", steps[1].Prompt)
	}
	// "fast" is a tier → routes to Complexity, clears ModelOverride.
	if steps[1].Complexity != ComplexityFast {
		t.Errorf("step b complexity: got %q, want fast", steps[1].Complexity)
	}
	if steps[1].ModelOverride != "" {
		t.Errorf("step b ModelOverride should be empty (tier override), got %q", steps[1].ModelOverride)
	}
	// concrete model id → ModelOverride, Complexity untouched.
	if steps[2].ModelOverride != "claude-haiku-4-5" {
		t.Errorf("step c model: got %q", steps[2].ModelOverride)
	}
}

func TestApplyStepOverrides_EmptyIsNoOp(t *testing.T) {
	steps := []Step{{ID: "a", Prompt: "x"}}
	applyStepOverrides(steps, nil)
	if steps[0].Prompt != "x" {
		t.Fatal("nil overrides must be a no-op")
	}
}
