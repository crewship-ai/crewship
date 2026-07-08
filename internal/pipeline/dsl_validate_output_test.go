package pipeline

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dsl_validate_output.go — unsatisfiable output-gate detection (#832).
//
// A step's `validation` block can encode a gate that NO output can ever
// satisfy: min_length > max_length, or the same string in both must_contain
// and must_not_contain. These used to be caught only by `doctor` (post-save,
// live). Promote them into Validate as hard errors so an author's editor loop
// / `routine validate` / `save` all reject a self-contradicting gate before
// it ships and fails every run.
// ---------------------------------------------------------------------------

func outputGateProbeDSL() *DSL {
	return &DSL{
		Name: "gated",
		Steps: []Step{
			{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go"},
		},
	}
}

func intp(i int) *int { return &i }

func TestValidate_OutputGate_MinGreaterThanMaxRejected(t *testing.T) {
	dsl := outputGateProbeDSL()
	dsl.Steps[0].Validation = &Validation{MinLength: intp(100), MaxLength: intp(10)}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("validation with min_length > max_length is unsatisfiable and must be rejected")
	}
	if !strings.Contains(err.Error(), "work") {
		t.Errorf("error should name the offending step, got: %v", err)
	}
}

func TestValidate_OutputGate_MinEqualMaxOK(t *testing.T) {
	dsl := outputGateProbeDSL()
	dsl.Steps[0].Validation = &Validation{MinLength: intp(10), MaxLength: intp(10)}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("min_length == max_length is satisfiable (exact length), got: %v", err)
	}
}

func TestValidate_OutputGate_ContradictoryContainmentRejected(t *testing.T) {
	dsl := outputGateProbeDSL()
	dsl.Steps[0].Validation = &Validation{
		MustContain:    []string{"APPROVED"},
		MustNotContain: []string{"APPROVED"},
	}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("same string in must_contain and must_not_contain is unsatisfiable and must be rejected")
	}
	if !strings.Contains(err.Error(), "APPROVED") {
		t.Errorf("error should name the contradicting token, got: %v", err)
	}
}

func TestValidate_OutputGate_DisjointContainmentOK(t *testing.T) {
	dsl := outputGateProbeDSL()
	dsl.Steps[0].Validation = &Validation{
		MustContain:    []string{"APPROVED"},
		MustNotContain: []string{"ERROR", "DENIED"},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("disjoint must_contain/must_not_contain is satisfiable, got: %v", err)
	}
}

func TestValidate_OutputGate_NoValidationBlockOK(t *testing.T) {
	if err := Validate(outputGateProbeDSL(), nil, nil); err != nil {
		t.Fatalf("a step with no validation block must validate, got: %v", err)
	}
}
