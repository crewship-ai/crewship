package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// validateOutput applies a step's Validation to the candidate output.
// Returns ok=true on success; otherwise reason describes which check
// failed.
//
// Order matters. Cheap byte-level checks first (length, must/not_contain)
// so a junk output fails fast without paying the JSON parse + schema
// compile cost. Schema validation runs only when the byte-level checks
// pass AND the schema field is non-empty.
//
// Schema gate semantics: when v.Schema is set, output MUST be parseable
// as JSON and MUST validate against the schema. A non-JSON output with
// a schema present fails the gate with a clear reason — this is the
// correct behaviour because routines that declare a schema do so
// because downstream steps consume the output as structured data.
func validateOutput(output string, v *Validation) (ok bool, reason string) {
	if v == nil {
		return true, ""
	}
	if v.MinLength != nil && len(output) < *v.MinLength {
		return false, fmt.Sprintf("output length %d below min %d", len(output), *v.MinLength)
	}
	if v.MaxLength != nil && len(output) > *v.MaxLength {
		return false, fmt.Sprintf("output length %d exceeds max %d", len(output), *v.MaxLength)
	}
	for _, banned := range v.MustNotContain {
		if banned == "" {
			continue
		}
		if containsCaseSensitive(output, banned) {
			return false, "output contains banned token: " + banned
		}
	}
	for _, required := range v.MustContain {
		if required == "" {
			continue
		}
		if !containsCaseSensitive(output, required) {
			return false, "output missing required token: " + required
		}
	}
	if len(v.Schema) > 0 {
		if ok, reason := validateAgainstSchema(output, v.Schema); !ok {
			return false, reason
		}
	}
	return true, ""
}

// validateAgainstSchema parses `output` as JSON and validates it
// against the supplied schema bytes (JSON Schema draft 2020-12 by
// default; library auto-detects $schema if specified).
//
// Failure modes return distinct reasons so a CodeRabbit-style review
// can tell at a glance which class of problem the run hit:
//
//   - "schema invalid"         — the schema itself can't compile.
//     Author bug; the routine should have been rejected at save time
//     once the parser-side schema validator lands. Returning false
//     here is correct: a misshapen schema can't accept anything.
//   - "output not valid JSON"  — output has no JSON structure but
//     a schema was declared. Worker model didn't follow the contract.
//   - "schema validation: ..." — output parsed but failed the schema.
//     Reason includes the first violation (limit at ~200 chars to
//     keep journal lines bounded).
//
// Compiled schemas are cached by sha256 of their bytes. The library
// is goroutine-safe so concurrent steps share the same compiled
// validator. Cache hit shaves the schema-compile cost (typically
// 100µs-1ms) per validation call; for a 10-step routine benched
// 10× that's a measurable win at near-zero memory cost (one
// Schema pointer per unique schema in the workspace).
//
// Cache eviction: never. The schema set in a workspace is bounded
// by the number of distinct routines + their schema-using steps,
// which is hundreds at the high end. A pointer per schema is bytes;
// the compiled trie itself is KBs. Even an aggressive workspace
// would top out under 10 MB cache footprint.
func validateAgainstSchema(output string, schemaBytes json.RawMessage) (ok bool, reason string) {
	schema, err := compiledSchemaForBytes(schemaBytes)
	if err != nil {
		return false, "schema invalid: " + truncate(err.Error(), 200)
	}
	var doc any
	if err := DecodeAgentJSON(output, &doc); err != nil {
		return false, "output not valid JSON: " + truncate(err.Error(), 200)
	}
	if err := schema.Validate(doc); err != nil {
		return false, "schema validation: " + truncate(err.Error(), 200)
	}
	return true, ""
}

// schemaCache holds compiled jsonschema.Schema pointers keyed by
// sha256(schemaBytes). sync.Map fits the access pattern (write-once,
// read-many; one entry per unique schema in the workspace) better
// than a mutex-guarded map — schemas are stable for the binary's
// lifetime.
var schemaCache sync.Map // map[string]*jsonschema.Schema, key = hex(sha256)

// compiledSchemaForBytes returns the compiled validator for the
// supplied schema bytes, compiling on first call and serving from
// cache thereafter. Errors propagate from the compiler; cache is
// only populated on successful compile so a transient error doesn't
// poison the cache.
func compiledSchemaForBytes(schemaBytes json.RawMessage) (*jsonschema.Schema, error) {
	sum := sha256.Sum256(schemaBytes)
	key := hex.EncodeToString(sum[:])
	if v, ok := schemaCache.Load(key); ok {
		return v.(*jsonschema.Schema), nil
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("inline://schema.json", strings.NewReader(string(schemaBytes))); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile("inline://schema.json")
	if err != nil {
		return nil, err
	}
	schemaCache.Store(key, schema)
	return schema, nil
}

// truncate returns s clipped to maxLen RUNES (not bytes) with an
// ellipsis if it got cut. Used by validateAgainstSchema to keep
// journal-line widths bounded; long jsonschema error chains can run
// several KB and would blow up the journal_entries.error_message
// column otherwise.
//
// Rune-based slicing matters when jsonschema returns non-ASCII
// content in errors (e.g. echoed input that contains multi-byte
// characters). Byte-slicing could split a UTF-8 sequence and
// produce invalid bytes in the journal entry, breaking downstream
// JSON serialisation. Caught by CodeRabbit review of #285.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// containsCaseSensitive is a thin wrapper over strings.Contains; kept
// as a function so we can swap in a normalisation pass (e.g. NFC) in
// Phase 2 without touching every call site.
func containsCaseSensitive(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// outcomesOnFail returns the OnFail action for outcomes failures,
// defaulting to abort. We don't reuse the step's OnFail because
// validation failures and outcomes failures may want different
// escalation strategies — a banned-token validation might warrant
// escalate_tier, but a rubric miss might warrant retry_step with
// grader feedback (when retry budgets land in Phase 2).
func outcomesOnFail(step Step) OnFailAction {
	if step.Outcomes != nil && step.Outcomes.OnFail != "" {
		return step.Outcomes.OnFail
	}
	return OnFailAbort
}
