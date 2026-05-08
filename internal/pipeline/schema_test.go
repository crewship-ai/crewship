package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		StepAgentRun, StepCallPipeline, StepHTTP, StepCode, StepWait, StepTransform,
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
