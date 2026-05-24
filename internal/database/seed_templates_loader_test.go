package database

import (
	"testing"
)

// TestLoadBuiltinWorkflowTemplates_LoadsAllFour pins the contract
// that exactly four built-in workflow templates ship: sequential,
// parallel, dev-test-loop, pipeline. A future contributor adding
// or removing one must update this assertion deliberately — the
// templates show up in the workflow templates picker for every new
// workspace, so silent additions/removals are a UX surprise.
func TestLoadBuiltinWorkflowTemplates_LoadsAllFour(t *testing.T) {
	t.Parallel()
	docs, err := loadBuiltinWorkflowTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 4 {
		t.Fatalf("loaded %d templates, want 4 (sequential, parallel, dev-test-loop, pipeline)", len(docs))
	}
	want := map[string]bool{
		"sequential":    false,
		"parallel":      false,
		"dev-test-loop": false,
		"pipeline":      false,
	}
	for _, d := range docs {
		if _, ok := want[d.Name]; !ok {
			t.Errorf("unexpected template %q", d.Name)
			continue
		}
		want[d.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing template %q from embedded set", name)
		}
	}
}

// TestLoadBuiltinWorkflowTemplates_DeterministicOrder confirms the
// loader returns templates in lexicographic filename order so the
// SeedBuiltinTemplates pass runs deterministically. A future test
// that asserts "the first inserted row is sequential" depends on
// this contract.
func TestLoadBuiltinWorkflowTemplates_DeterministicOrder(t *testing.T) {
	t.Parallel()
	for i := 0; i < 5; i++ {
		docs, err := loadBuiltinWorkflowTemplates()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		want := []string{"dev-test-loop", "parallel", "pipeline", "sequential"}
		if len(docs) != len(want) {
			t.Fatalf("len mismatch: %d vs %d", len(docs), len(want))
		}
		for j, d := range docs {
			if d.Name != want[j] {
				t.Errorf("docs[%d].Name = %q, want %q (iteration %d)", j, d.Name, want[j], i)
			}
		}
	}
}

// TestLoadBuiltinWorkflowTemplates_ShapeIntegrity pins the per-
// template shape — every loaded template must have a non-empty name,
// description, AT LEAST one step, and each step must have ID + title.
// Catches a YAML edit that breaks the per-row INSERT downstream.
func TestLoadBuiltinWorkflowTemplates_ShapeIntegrity(t *testing.T) {
	t.Parallel()
	docs, err := loadBuiltinWorkflowTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, d := range docs {
		if d.Name == "" {
			t.Errorf("template missing name (file content: %+v)", d)
			continue
		}
		if d.Description == "" {
			t.Errorf("%s: description is empty", d.Name)
		}
		if d.Icon == "" {
			t.Errorf("%s: icon is empty", d.Name)
		}
		if d.Color == "" {
			t.Errorf("%s: color is empty", d.Name)
		}
		if d.Template.Name != d.Name {
			t.Errorf("%s: outer name vs template.name mismatch (template.name=%q)", d.Name, d.Template.Name)
		}
		if len(d.Template.Steps) == 0 {
			t.Errorf("%s: template has zero steps", d.Name)
		}
		for i, step := range d.Template.Steps {
			if step.ID == "" {
				t.Errorf("%s: step %d missing id", d.Name, i)
			}
			if step.Title == "" {
				t.Errorf("%s: step %d (%q) missing title", d.Name, i, step.ID)
			}
		}
	}
}

// TestLoadBuiltinWorkflowTemplates_DevTestLoopShape pins the
// canonical reference pattern — dev-test-loop's max_iterations + loop
// back wiring is the most complex of the four and the easiest to
// break in a YAML edit. Catches "I dropped max_iterations" or "the
// loop_back_to slug drifted from the step ID" silently.
func TestLoadBuiltinWorkflowTemplates_DevTestLoopShape(t *testing.T) {
	t.Parallel()
	docs, err := loadBuiltinWorkflowTemplates()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var dtl *templateDef
	for i := range docs {
		if docs[i].Name == "dev-test-loop" {
			dtl = &docs[i].Template
			break
		}
	}
	if dtl == nil {
		t.Fatal("dev-test-loop not in embedded set")
	}
	if len(dtl.Steps) != 2 {
		t.Fatalf("dev-test-loop has %d steps, want 2", len(dtl.Steps))
	}
	if dtl.Steps[0].ID != "develop" || dtl.Steps[0].MaxIterations != 3 {
		t.Errorf("step[0] = %+v, want develop/max=3", dtl.Steps[0])
	}
	if dtl.Steps[1].LoopBackTo != "develop" || dtl.Steps[1].MaxIterations != 3 {
		t.Errorf("step[1] = %+v, want loop_back_to=develop max=3", dtl.Steps[1])
	}
}
