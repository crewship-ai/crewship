package pipeline

import (
	"strings"
	"testing"
)

// foreach is a declared StepType (types.go), it is in schemas/routine.v1.json,
// and validateForeachStep exists and runs — but validateStepEgress had no case
// for it, so every foreach step was refused at SAVE time by the default arm.
// The error did not even list foreach among the allowed kinds, so the author
// was told their step type did not exist.
//
// No test covered this because the foreach tests build Step structs directly
// and never go through Validate.
func TestValidateStepEgress_ForeachIsSavable(t *testing.T) {
	loop := Step{ID: "loop", Type: StepForeach, Foreach: &ForeachStep{
		Items: "{{ inputs.rows }}",
		Steps: []Step{{ID: "shape", Type: StepTransform, Transform: &TransformStep{Input: "{{ inputs.item }}", Expression: "."}}},
	}}
	if err := validateStepEgress(loop); err != nil {
		t.Fatalf("foreach must survive save-time validation, got: %v", err)
	}
}

// A foreach with no body is genuinely invalid — the loop has nothing to run.
// Pinned so the fix above cannot drift into accepting one.
func TestValidateStepEgress_ForeachWithoutABodyIsRefused(t *testing.T) {
	if err := validateStepEgress(Step{ID: "loop", Type: StepForeach}); err == nil {
		t.Fatal("a foreach with no body must be refused")
	}
}

// The body of a foreach is a step in its own right, and it is the body — not
// the loop — that can reach the network. Egress rules must therefore apply to
// what the loop runs, or foreach becomes a hole around them.
func TestValidateStepEgress_ForeachBodyIsStillChecked(t *testing.T) {
	bad := Step{ID: "fetch", Type: StepHTTP} // http with no url
	err := validateStepEgress(Step{ID: "loop", Type: StepForeach,
		Foreach: &ForeachStep{Items: "{{ inputs.rows }}", Steps: []Step{bad}}})
	if err == nil {
		t.Fatal("a foreach whose body is an invalid http step must be refused")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("the error must name the offending inner step, got: %v", err)
	}
}

// Regression pin: whatever the allowed-list says, it must name every type the
// function actually accepts. The original error omitted foreach while the
// switch also omitted it — two halves of one mistake that hid each other.
func TestValidateStepEgress_AllowedListNamesForeach(t *testing.T) {
	err := validateStepEgress(Step{ID: "x", Type: StepType("not-a-real-kind")})
	if err == nil {
		t.Fatal("an unknown step type must be refused")
	}
	if !strings.Contains(err.Error(), "foreach") {
		t.Fatalf("the allowed-list must name foreach, got: %v", err)
	}
}
