package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

// Schema gate tests cover the four distinct outcomes of validateOutput
// when v.Schema is non-empty: pass, malformed schema, malformed JSON
// output, schema-violating output. Each case returns a different
// `reason` prefix so the executor's emit_validation_failed log line
// stays diagnostically useful.

func TestValidateOutput_SchemaGate_AcceptsConformingJSON(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["name", "qty"],
		"properties": {
			"name": {"type": "string", "minLength": 1},
			"qty":  {"type": "integer", "minimum": 0}
		}
	}`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput(`{"name":"widget","qty":3}`, v)
	if !ok {
		t.Fatalf("expected pass, got reason=%q", reason)
	}
}

func TestValidateOutput_SchemaGate_RejectsMissingRequiredKey(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["name", "qty"],
		"properties": {
			"name": {"type": "string"},
			"qty":  {"type": "integer"}
		}
	}`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput(`{"name":"widget"}`, v)
	if ok {
		t.Fatal("expected fail, got pass")
	}
	if !strings.HasPrefix(reason, "schema validation:") {
		t.Errorf("reason should be prefixed 'schema validation:', got %q", reason)
	}
	if !strings.Contains(reason, "qty") {
		t.Errorf("reason should mention missing 'qty' key, got %q", reason)
	}
}

func TestValidateOutput_SchemaGate_RejectsWrongType(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"qty": {"type": "integer"}
		}
	}`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput(`{"qty":"three"}`, v)
	if ok {
		t.Fatalf("expected fail (qty is string not integer)")
	}
	if !strings.HasPrefix(reason, "schema validation:") {
		t.Errorf("reason prefix wrong: %q", reason)
	}
}

func TestValidateOutput_SchemaGate_RejectsNonJSONOutput(t *testing.T) {
	// Worker model went off-script and wrote prose where a schema was
	// declared. The gate must fail with a distinct prefix so the
	// downstream operator can tell "model didn't follow JSON contract"
	// from "model returned valid JSON that didn't match schema".
	schema := json.RawMessage(`{"type": "object"}`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput("Here is the answer: not JSON", v)
	if ok {
		t.Fatal("expected fail")
	}
	if !strings.HasPrefix(reason, "output not valid JSON:") {
		t.Errorf("reason should be prefixed 'output not valid JSON:', got %q", reason)
	}
}

func TestValidateOutput_SchemaGate_RejectsMalformedSchema(t *testing.T) {
	// Schema is a parse-time author bug. The gate must fail
	// closed (not silently accept) so a routine with a typo'd
	// schema doesn't pass garbage downstream. Save-time validation
	// of schemas is a Phase 2 follow-up; until then this gate is
	// the last line of defence.
	schema := json.RawMessage(`{not valid json at all`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput(`{"x":1}`, v)
	if ok {
		t.Fatal("expected fail on malformed schema")
	}
	if !strings.HasPrefix(reason, "schema invalid:") {
		t.Errorf("reason should be prefixed 'schema invalid:', got %q", reason)
	}
}

func TestValidateOutput_SchemaGate_HonoursEnumConstraint(t *testing.T) {
	// JSON Schema enum is the canonical "classification with N
	// labels" gate. The eval-classify-sentiment scenario relies on
	// this — without a working enum check, weak-model freelance
	// labels ("very positive", "kinda neutral") would silently
	// pass. This test asserts the enum gate works as advertised.
	schema := json.RawMessage(`{
		"type": "string",
		"enum": ["positive", "negative", "neutral"]
	}`)
	v := &Validation{Schema: schema}

	if ok, _ := validateOutput(`"positive"`, v); !ok {
		t.Error("expected 'positive' to pass enum gate")
	}
	if ok, _ := validateOutput(`"neutral"`, v); !ok {
		t.Error("expected 'neutral' to pass enum gate")
	}
	if ok, _ := validateOutput(`"very positive"`, v); ok {
		t.Error("expected 'very positive' to fail enum gate")
	}
	if ok, _ := validateOutput(`"POSITIVE"`, v); ok {
		t.Error("expected uppercase 'POSITIVE' to fail enum gate (enums are case-sensitive)")
	}
}

func TestValidateOutput_SchemaGate_RunsAfterCheaperGates(t *testing.T) {
	// Length / must_contain / must_not_contain run BEFORE schema
	// validation so a cheap fail short-circuits without paying
	// the schema compile + JSON parse cost. The reason returned
	// must reflect the FIRST gate that tripped, not whichever
	// gate happens to be cheapest to evaluate.
	schema := json.RawMessage(`{"type":"object"}`)
	maxLen := 5
	v := &Validation{Schema: schema, MaxLength: &maxLen}
	ok, reason := validateOutput(`{"a":1}`, v) // 7 bytes > 5
	if ok {
		t.Fatal("expected length gate to trip first")
	}
	if !strings.HasPrefix(reason, "output length") {
		t.Errorf("expected length-gate reason, got schema-gate reason %q", reason)
	}
}

func TestValidateOutput_NilValidation_Passes(t *testing.T) {
	// Defensive — many step types have no validation declared.
	// The gate must accept anything (including the empty string)
	// when v == nil.
	if ok, reason := validateOutput("", nil); !ok {
		t.Fatalf("nil validation should accept empty output, got reason=%q", reason)
	}
	if ok, reason := validateOutput("anything goes", nil); !ok {
		t.Fatalf("nil validation should accept arbitrary output, got reason=%q", reason)
	}
}
