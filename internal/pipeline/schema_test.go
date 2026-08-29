package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/notify"
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
		StepAgentRun, StepCallPipeline, StepHTTP, StepCode, StepWait, StepTransform, StepNotify, StepScript, StepQuery, StepForeach, StepCrewship,
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

	// Every step BODY too — WaitStep, HTTPStep, CodeStep, TransformStep…
	//
	// Walking DSL and Step alone left the bodies unguarded, which is
	// where the fields actually get added: WaitStep.ApprovalTitle could
	// be introduced in Go, omitted from the schema, and ship green,
	// while `additionalProperties: false` on the $def silently rejected
	// every routine that used it for anyone validating against the
	// published schema.
	//
	// Derived from Step's own pointer fields rather than a hand-kept
	// list, so a NEW step body is covered the day it is declared and
	// cannot be forgotten here.
	stepType := reflect.TypeOf(Step{})
	for i := 0; i < stepType.NumField(); i++ {
		f := stepType.Field(i)
		if f.Type.Kind() != reflect.Pointer || f.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		bodyName := f.Type.Elem().Name()
		bodyDef, ok := defs[bodyName].(map[string]interface{})
		if !ok {
			t.Errorf("Step field %s is a *%s body but schemas/routine.v1.json has no $defs/%s",
				f.Name, bodyName, bodyName)
			continue
		}
		cases = append(cases, struct {
			name  string
			typ   reflect.Type
			props map[string]interface{}
		}{bodyName, f.Type.Elem(), propsOf(bodyDef)})
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

// TestRoutineSchema_EnumsPinnedToGo keeps every schema enum whose vocabulary
// has a Go source of truth from drifting away from it.
//
// AllFieldsCovered proves a field EXISTS in the schema. It says nothing about
// the field's accepted VALUES, and two of the enums here were hand-copied out
// of Go when they were written — so adding a notification category in Go would
// leave the published schema rejecting every routine that used it, which is the
// same silent-rejection failure AllFieldsCovered exists to prevent, one level
// down. Nothing caught that, including the change that added these enums.
//
// Each case names the schema location and the Go slice that governs it. A new
// enum backed by Go constants belongs here on the day it is added.
func TestRoutineSchema_EnumsPinnedToGo(t *testing.T) {
	raw, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	defs, _ := doc["$defs"].(map[string]interface{})

	// enumAt walks $defs/<def>/properties/<prop>/enum.
	enumAt := func(t *testing.T, def, prop string) []string {
		t.Helper()
		d, ok := defs[def].(map[string]interface{})
		if !ok {
			t.Fatalf("schemas/routine.v1.json has no $defs/%s", def)
		}
		props, _ := d["properties"].(map[string]interface{})
		p, ok := props[prop].(map[string]interface{})
		if !ok {
			t.Fatalf("$defs/%s has no property %q", def, prop)
		}
		rawEnum, ok := p["enum"].([]interface{})
		if !ok {
			t.Fatalf("$defs/%s/properties/%s has no enum", def, prop)
		}
		out := make([]string, 0, len(rawEnum))
		for _, v := range rawEnum {
			s, _ := v.(string)
			out = append(out, s)
		}
		return out
	}

	cases := []struct {
		name   string
		def    string
		prop   string
		want   []string
		source string
	}{
		{
			name:   "wait risk_level",
			def:    "WaitStep",
			prop:   "risk_level",
			want:   RiskLevels,
			source: "pipeline.RiskLevels",
		},
		{
			name:   "notify category",
			def:    "NotifyStep",
			prop:   "category",
			want:   notify.AllCategories,
			source: "notify.AllCategories",
		},
		{
			name: "notify priority",
			def:  "NotifyStep",
			prop: "priority",
			// isValidNotifyPriority is a switch rather than a slice, so this
			// is the one vocabulary with no Go value to read. Pinned as a
			// literal that the validator's own cases must match — if they
			// diverge, one of the two assertions below fails.
			want:   []string{"urgent", "high", "medium", "low"},
			source: "isValidNotifyPriority",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := enumAt(t, tc.def, tc.prop)

			inGo := make(map[string]bool, len(tc.want))
			for _, v := range tc.want {
				inGo[v] = true
			}
			inSchema := make(map[string]bool, len(got))
			for _, v := range got {
				inSchema[v] = true
			}

			for _, v := range tc.want {
				if !inSchema[v] {
					t.Errorf("%s accepts %q but $defs/%s/properties/%s does not list it — "+
						"a routine using it validates in Go and is rejected by the published schema; "+
						"add it to schemas/routine.v1.json", tc.source, v, tc.def, tc.prop)
				}
			}
			for _, v := range got {
				if !inGo[v] {
					t.Errorf("$defs/%s/properties/%s lists %q but %s does not accept it — "+
						"a routine using it passes schema validation and is refused at save",
						tc.def, tc.prop, v, tc.source)
				}
			}
		})
	}

	// The priority literal above is only meaningful if the validator agrees
	// with it, so check both directions against the real function.
	t.Run("notify priority literal matches the validator", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"urgent", "high", "medium", "low"} {
			if !isValidNotifyPriority(v) {
				t.Errorf("isValidNotifyPriority(%q) = false, but the schema and this test list it", v)
			}
		}
		for _, v := range []string{"", "URGENT", "critical", "none"} {
			if isValidNotifyPriority(v) {
				t.Errorf("isValidNotifyPriority(%q) = true, but it is not in the schema enum", v)
			}
		}
	})
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
