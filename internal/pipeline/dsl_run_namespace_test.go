package pipeline

import "testing"

// {{ run.metadata.x }} / run.is_replay must pass template validation
// (Wave 2.4) — the run namespace resolves at render time.
func TestValidate_RunNamespaceAllowed(t *testing.T) {
	dsl := &DSL{Name: "run-ns", Steps: []Step{
		{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "{{ run.metadata.threshold }} > 5"}},
	}}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("run namespace should validate: %v", err)
	}
}
