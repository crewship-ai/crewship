package kinds

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A routine's DSL is the source of truth, and `crewship apply` must ship
// it whole. It did not: RoutineSpec modelled 12 keys and
// definitionJSONShape emitted 11, so every other top-level DSL field —
// guardrails, integrations_required, concurrency_key, max_concurrent,
// outputs, display_name, agentless, hooks, eval, resources,
// execution_tier, parallelism — was dropped between the file on disk
// and the body on the wire. No error, no warning, no plan diff.
//
// The drop bites twice, and the second one is the dangerous half:
//
//   - apply: the field never reaches the server, so a routine that
//     declares `agentless: true` or `max_concurrent: 1` lands without
//     the guarantee it was written to carry;
//   - export → edit → apply: ExportRoutines decodes the STORED
//     definition into the same closed struct, so a field authored via
//     `crewship routine save` is stripped on the way out and then
//     actively deleted from the live routine on the next apply.
//
// The fix is the pattern RoutineStep has always used: an inline
// catch-all. These tests pin both directions.

const passthroughRoutineYAML = `
apiVersion: crewship/v1
kind: Routine
metadata:
  name: Accounting pack
  slug: msn-etn-podklady
  labels:
    crew: uctarna
spec:
  dsl_version: "1.0"
  display_name: Měsíční účetní podklady
  agentless: false
  concurrency_key: "{{ inputs.obdobi }}"
  max_concurrent: 1
  integrations_required:
    - googledrive
    - gmail
  guardrails:
    input: sanitize
  outputs:
    - name: overeno_ok
      type: boolean
  inputs:
    - name: obdobi
      type: string
  steps:
    - id: a
      type: transform
  schedules:
    - name: monthly
      cron: "0 6 1 * *"
      timezone: Europe/Prague
`

func decodePassthroughRoutine(t *testing.T, src string) RoutineDocument {
	t.Helper()
	var doc RoutineDocument
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc
}

// saveBodyDefinition returns the `definition` sub-object of the body
// apply PUTs to /pipelines/save — the only bytes that decide what the
// routine actually becomes.
func saveBodyDefinition(t *testing.T, doc RoutineDocument) map[string]any {
	t.Helper()
	raw, err := json.Marshal(doc.buildSaveBody())
	if err != nil {
		t.Fatalf("marshal save body: %v", err)
	}
	var body struct {
		Definition map[string]any `json:"definition"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode save body: %v", err)
	}
	return body.Definition
}

func TestRoutineManifest_UnmodelledDSLFieldsReachTheServer(t *testing.T) {
	def := saveBodyDefinition(t, decodePassthroughRoutine(t, passthroughRoutineYAML))

	for _, key := range []string{
		"display_name",
		"concurrency_key",
		"max_concurrent",
		"integrations_required",
		"guardrails",
		"outputs",
	} {
		if _, ok := def[key]; !ok {
			t.Errorf("apply dropped spec key %q — it never reaches the server", key)
		}
	}

	// Spot-check values, not just presence: a key carrying the wrong
	// payload is the same outage wearing a passing test.
	if got := def["concurrency_key"]; got != "{{ inputs.obdobi }}" {
		t.Errorf("concurrency_key = %#v, want the template string", got)
	}
	if got, ok := def["max_concurrent"].(float64); !ok || got != 1 {
		t.Errorf("max_concurrent = %#v, want 1", def["max_concurrent"])
	}
	if got, ok := def["integrations_required"].([]any); !ok || len(got) != 2 {
		t.Errorf("integrations_required = %#v, want 2 entries", def["integrations_required"])
	}
}

// Manifest-only fields describe sibling tables, not the DSL. The
// catch-all must not sweep them into the definition: pipeline.Parse
// would carry `schedules` into definition_json, and the routine's
// stored DSL would grow a key the schema never had.
func TestRoutineManifest_ManifestOnlyFieldsStayOutOfTheDefinition(t *testing.T) {
	doc := decodePassthroughRoutine(t, passthroughRoutineYAML)
	if len(doc.Spec.Schedules) != 1 {
		t.Fatalf("schedules did not decode onto the typed field: %+v", doc.Spec.Schedules)
	}
	def := saveBodyDefinition(t, doc)
	for _, key := range []string{"schedules", "webhook"} {
		if _, ok := def[key]; ok {
			t.Errorf("manifest-only key %q leaked into the routine definition", key)
		}
	}
}

// The export → edit → apply round-trip. ExportRoutines decodes the
// stored definition_json into RoutineSpec; whatever that decode loses
// is deleted from the live routine by the next apply.
func TestRoutineManifest_ExportApplyRoundTripKeepsStoredFields(t *testing.T) {
	stored := []byte(`{
	  "dsl_version": "1.0",
	  "name": "msn-etn-podklady",
	  "agentless": true,
	  "concurrency_key": "acct",
	  "max_concurrent": 2,
	  "guardrails": {"input": "log"},
	  "integrations_required": ["googledrive"],
	  "steps": [{"id": "a", "type": "transform"}]
	}`)

	var spec RoutineSpec
	if err := json.Unmarshal(stored, &spec); err != nil {
		t.Fatalf("decode stored definition: %v", err)
	}
	doc := RoutineDocument{Spec: spec}
	doc.Metadata.Slug = "msn-etn-podklady"

	def := saveBodyDefinition(t, doc)
	for _, key := range []string{"agentless", "concurrency_key", "max_concurrent", "guardrails", "integrations_required"} {
		if _, ok := def[key]; !ok {
			t.Errorf("export → apply stripped %q from a routine that already had it", key)
		}
	}
	// agentless: true is a security guarantee — losing it silently
	// re-permits LLM spend on a routine documented as token-zero.
	if got := def["agentless"]; got != true {
		t.Errorf("agentless = %#v, want true", got)
	}

	// A YAML export of the same spec must carry them too, or the file a
	// human edits is already lossy before apply ever runs.
	out, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec to yaml: %v", err)
	}
	for _, key := range []string{"agentless", "concurrency_key", "guardrails"} {
		if !strings.Contains(string(out), key) {
			t.Errorf("exported YAML is missing %q:\n%s", key, out)
		}
	}
}
