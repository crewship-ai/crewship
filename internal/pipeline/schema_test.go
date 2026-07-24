package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestRoutineSchema_ValidJSON proves the published JSON Schema file is
// itself a valid JSON document. Cheap insurance against typos in the
// schema breaking IDE autocomplete users.
func TestRoutineSchema_ValidJSON(t *testing.T) {
	path := schemaPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	// Sanity-check a few must-have top-level keys.
	for _, k := range []string{"$schema", "$id", "title", "type", "properties", "$defs"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("schema missing top-level key %q", k)
		}
	}
	// Verify $id contains the version we committed to (v1).
	if id, ok := doc["$id"].(string); !ok || !strings.Contains(id, "routine.v1") {
		t.Errorf("schema $id should contain 'routine.v1', got %q", doc["$id"])
	}
}

// TestRoutineSchema_AllStepTypesCovered ensures the schema's step
// type enum stays in sync with the StepType constants. If we add a
// new step kind to the runtime without updating the schema, IDE
// users get a lying "this is not a valid type" warning — this test
// keeps the two surfaces aligned.
func TestRoutineSchema_AllStepTypesCovered(t *testing.T) {
	raw, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(raw, &doc)

	defs, _ := doc["$defs"].(map[string]interface{})
	step, _ := defs["Step"].(map[string]interface{})
	props, _ := step["properties"].(map[string]interface{})
	typeProp, _ := props["type"].(map[string]interface{})
	enum, _ := typeProp["enum"].([]interface{})

	expected := []StepType{
		StepAgentRun, StepCallPipeline, StepHTTP, StepCode, StepWait, StepTransform, StepNotify, StepScript, StepQuery, StepForeach,
	}
	if len(enum) != len(expected) {
		t.Errorf("step type count mismatch: schema enum=%d, runtime=%d", len(enum), len(expected))
	}
	have := make(map[string]bool, len(enum))
	for _, v := range enum {
		s, _ := v.(string)
		have[s] = true
	}
	for _, want := range expected {
		if !have[string(want)] {
			t.Errorf("schema enum missing step type %q", want)
		}
	}
}

// TestRoutineSchema_AllFieldsCovered is the regression guard for the #831
// defect class: an object in the schema is additionalProperties:false, so
// any Go field the parser accepts but the schema omits makes a valid,
// skill-authored routine fail schema validation (IDE + external linters)
// even though the server saves + runs it. Reflect over the source-of-truth
// structs at EVERY level that maps to an additionalProperties:false object
// (DSL top-level AND $defs.Step) and assert every json-tagged field has a
// matching schema property — so a new field can't drift out of the
// published contract again, at any depth.
func TestRoutineSchema_AllFieldsCovered(t *testing.T) {
	raw, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(raw, &doc)
	defs, _ := doc["$defs"].(map[string]interface{})

	propsOf := func(m map[string]interface{}) map[string]interface{} {
		p, _ := m["properties"].(map[string]interface{})
		return p
	}
	stepDef, _ := defs["Step"].(map[string]interface{})

	cases := []struct {
		name  string
		typ   reflect.Type
		props map[string]interface{}
	}{
		{"DSL", reflect.TypeOf(DSL{}), propsOf(doc)},
		{"Step", reflect.TypeOf(Step{}), propsOf(stepDef)},
	}
	for _, c := range cases {
		for i := 0; i < c.typ.NumField(); i++ {
			tag := c.typ.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue // internal / unserialized field
			}
			name := strings.Split(tag, ",")[0]
			if name == "" {
				continue
			}
			if _, ok := c.props[name]; !ok {
				t.Errorf("%s field %s (json:%q) has no schema property in %s — additionalProperties:false will reject a routine that uses it; add it to schemas/routine.v1.json",
					c.name, c.typ.Field(i).Name, name, c.name)
			}
		}
	}
}

// schemaPath returns the path to the routine.v1.json file relative
// to this test file. Resolves at test time so the test can run from
// any working directory.
func schemaPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, "schemas", "routine.v1.json")
}
