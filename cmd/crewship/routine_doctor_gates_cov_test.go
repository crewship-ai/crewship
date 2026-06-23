package main

import (
	"strings"
	"testing"
)

func TestCheckValidationGates_NoSteps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		def  map[string]interface{}
	}{
		{"missing steps key", map[string]interface{}{}},
		{"steps wrong type", map[string]interface{}{"steps": "not-a-list"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkValidationGates(tc.def)
			if len(got) != 1 {
				t.Fatalf("want 1 check, got %d: %+v", len(got), got)
			}
			if got[0].Name != "validation_gates" || got[0].Level != doctorOK {
				t.Errorf("got %+v; want validation_gates OK", got[0])
			}
			if !strings.Contains(got[0].Message, "no validation blocks") {
				t.Errorf("message: got %q", got[0].Message)
			}
		})
	}
}

func TestCheckValidationGates_StepsWithoutValidation(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"steps": []interface{}{
			"not-a-map",                        // non-map entries are skipped
			map[string]interface{}{"id": "s1"}, // no validation block
			map[string]interface{}{"id": "s2", "validation": "wrong"}, // wrong shape
		},
	}
	got := checkValidationGates(def)
	if len(got) != 1 {
		t.Fatalf("want 1 fallback check, got %d: %+v", len(got), got)
	}
	if got[0].Name != "validation_gates" || got[0].Level != doctorOK {
		t.Errorf("got %+v; want validation_gates OK fallback", got[0])
	}
}

func TestCheckValidationGates_MinGreaterThanMax(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"id": "summarize",
				"validation": map[string]interface{}{
					"min_length": float64(100),
					"max_length": float64(10),
				},
			},
		},
	}
	got := checkValidationGates(def)
	if len(got) != 1 {
		t.Fatalf("want 1 check, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Name != "validation:summarize" {
		t.Errorf("Name: got %q", c.Name)
	}
	if c.Level != doctorFail {
		t.Errorf("Level: got %q want FAIL", c.Level)
	}
	if !strings.Contains(c.Message, "min_length 100 > max_length 10") {
		t.Errorf("Message: got %q", c.Message)
	}
	if !strings.Contains(c.Hint, "fix the bounds") {
		t.Errorf("Hint: got %q", c.Hint)
	}
}

func TestCheckValidationGates_ContradictoryContains(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"id": "draft",
				"validation": map[string]interface{}{
					"must_contain":     []interface{}{"SUMMARY", "TODO"},
					"must_not_contain": []interface{}{"TODO"},
				},
			},
		},
	}
	got := checkValidationGates(def)
	if len(got) != 1 {
		t.Fatalf("want 1 check (goto skips OK append), got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Level != doctorFail {
		t.Errorf("Level: got %q want FAIL", c.Level)
	}
	if !strings.Contains(c.Message, `"TODO" is in BOTH`) {
		t.Errorf("Message: got %q", c.Message)
	}
}

func TestCheckValidationGates_EmptyStringNotContradiction(t *testing.T) {
	t.Parallel()

	// "" appearing in both lists must NOT be flagged (cs != "" guard).
	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"id": "s1",
				"validation": map[string]interface{}{
					"must_contain":     []interface{}{""},
					"must_not_contain": []interface{}{""},
				},
			},
		},
	}
	got := checkValidationGates(def)
	if len(got) != 1 || got[0].Level != doctorOK {
		t.Fatalf("empty-string overlap should be satisfiable; got %+v", got)
	}
}

func TestCheckValidationGates_SatisfiableGate(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"id": "ok-step",
				"validation": map[string]interface{}{
					"min_length":       float64(10),
					"max_length":       float64(100),
					"must_contain":     []interface{}{"yes"},
					"must_not_contain": []interface{}{"no"},
				},
			},
		},
	}
	got := checkValidationGates(def)
	if len(got) != 1 {
		t.Fatalf("want 1 check, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Name != "validation:ok-step" || c.Level != doctorOK {
		t.Errorf("got %+v; want OK for ok-step", c)
	}
	if !strings.Contains(c.Message, "satisfiable") {
		t.Errorf("Message: got %q", c.Message)
	}
}

func TestCheckValidationGates_MixedSteps(t *testing.T) {
	t.Parallel()

	// One failing + one passing step in the same DSL: both reported.
	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{
				"id": "bad",
				"validation": map[string]interface{}{
					"min_length": float64(5),
					"max_length": float64(1),
				},
			},
			map[string]interface{}{
				"id": "good",
				"validation": map[string]interface{}{
					"min_length": float64(1),
				},
			},
		},
	}
	got := checkValidationGates(def)
	if len(got) != 2 {
		t.Fatalf("want 2 checks, got %d: %+v", len(got), got)
	}
	if got[0].Name != "validation:bad" || got[0].Level != doctorFail {
		t.Errorf("first: got %+v", got[0])
	}
	if got[1].Name != "validation:good" || got[1].Level != doctorOK {
		t.Errorf("second: got %+v", got[1])
	}
}
