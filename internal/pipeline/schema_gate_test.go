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

func TestCompiledSchemaForBytes_CachesByContentHash(t *testing.T) {
	// Two calls with byte-identical schemas must return the
	// SAME compiled instance — that's the whole point of the
	// cache. Pointer equality is the right test (not deep
	// equality, which would tolerate two separate compiles).
	schemaA := json.RawMessage(`{"type":"object","required":["x"]}`)
	schemaB := json.RawMessage(`{"type":"object","required":["x"]}`)

	first, err := compiledSchemaForBytes(schemaA)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	second, err := compiledSchemaForBytes(schemaB)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if first != second {
		t.Error("expected pointer-identical compiled schema on cache hit, got distinct instances (cache not working)")
	}
}

func TestCompiledSchemaForBytes_DistinctSchemasProduceDistinctEntries(t *testing.T) {
	// Two schemas that differ by even one byte must compile to
	// two separate cache entries. Otherwise a workspace with
	// 50 routines would silently use one schema for everyone.
	a := json.RawMessage(`{"type":"object","required":["x"]}`)
	b := json.RawMessage(`{"type":"object","required":["y"]}`)

	first, err := compiledSchemaForBytes(a)
	if err != nil {
		t.Fatalf("compile a: %v", err)
	}
	second, err := compiledSchemaForBytes(b)
	if err != nil {
		t.Fatalf("compile b: %v", err)
	}
	if first == second {
		t.Error("distinct schemas should compile to distinct instances")
	}
}

func TestCompiledSchemaForBytes_DoesNotPoisonOnFailure(t *testing.T) {
	// A schema that fails to compile must NOT land in the cache.
	// Otherwise a transient compile error (e.g. malformed bytes
	// during a partial write) would freeze a bad entry forever
	// and every subsequent valid call would hit the bad entry.
	bad := json.RawMessage(`{not valid json`)
	if _, err := compiledSchemaForBytes(bad); err == nil {
		t.Fatal("expected compile failure on malformed schema")
	}
	// A second call with the SAME bad bytes should re-attempt
	// compile (and re-fail), not return a cached error.
	if _, err := compiledSchemaForBytes(bad); err == nil {
		t.Error("expected compile failure on second call too — cache must not store failures")
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

// LLMs ignore "no prose outside the JSON" prompts in three reliable
// ways; the candidate extractor + json.Decoder combo accepts all of
// them so the validator stops being a per-pipeline whack-a-mole.

func TestValidateOutput_SchemaGate_AcceptsLLMQuirks(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["k"]}`)
	v := &Validation{Schema: schema}
	cases := map[string]string{
		"fenced with json tag":       "```json\n{\"k\":1}\n```",
		"fenced upper-case tag":      "```JSON\n{\"k\":1}\n```",
		"fenced no tag":              "```\n{\"k\":1}\n```",
		"fenced with leading space":  "  ```json\n{\"k\":1}\n```\n",
		"fenced unclosed (truncated)": "```json\n{\"k\":1}",
		"prose preamble":             "Here is the JSON you asked for:\n{\"k\":1}",
		"markdown bullet preamble":   "* Quick note: see below.\n{\"k\":1}",
		"prose suffix":               "{\"k\":1}\nLet me know if you need anything else.",
		"both preamble and fence":    "Sure!\n```json\n{\"k\":1}\n```",
		"bare json":                  `{"k":1}`,
	}
	for name, raw := range cases {
		ok, reason := validateOutput(raw, v)
		if !ok {
			t.Errorf("case %s: expected pass for %q, got reason=%q", name, raw, reason)
		}
	}
}

func TestValidateOutput_SchemaGate_StillRejectsRealNonJSON(t *testing.T) {
	// Regression guard: the extractor must not turn pure prose into
	// a passing case. Without a `{` or `[` anywhere, validation must
	// still fail with the "output not valid JSON" prefix.
	schema := json.RawMessage(`{"type":"object"}`)
	v := &Validation{Schema: schema}
	ok, reason := validateOutput("nothing here looks like JSON at all", v)
	if ok {
		t.Fatal("prose-only input must not pass validation")
	}
	if !strings.HasPrefix(reason, "output not valid JSON:") {
		t.Errorf("reason prefix wrong: %q", reason)
	}
}
