package pipeline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFixtureStepReusesTransformAndBlocksMissingEvidence(t *testing.T) {
	in := FixtureStepInput{Definition: json.RawMessage(`{"name":"fixtures","steps":[{"id":"fetch","type":"http","http":{"method":"GET","url":"https://example.com"}},{"id":"count","type":"transform","transform":{"input":"{{ steps.fetch.output }}","expression":".count"},"validation":{"must_contain":["42"]}}]}`), StepID: "count", StepOutputs: map[string]string{"fetch": `{"count":42}`}}
	result, err := TestStepWithFixtures(in)
	if err != nil || !result.Valid || result.Output != "42" || result.ExecutionMode != "fixtures" || result.OutputSource != "transform" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	firstHash := result.FixtureHash
	in.StepOutputs["fetch"] = `{"count":21}`
	result, err = TestStepWithFixtures(in)
	if err != nil || result.Valid || firstHash == result.FixtureHash {
		t.Fatalf("stale fixture: %+v %v", result, err)
	}
	in.StepOutputs = nil
	if _, err = TestStepWithFixtures(in); err == nil || !strings.Contains(err.Error(), "missing fixture") {
		t.Fatalf("missing output accepted: %v", err)
	}
}

func TestFixtureStepNeverMakesHTTPRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte("live")) }))
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"name": "fixtures", "steps": []any{map[string]any{"id": "send", "type": "http", "http": map[string]any{"method": "POST", "url": server.URL}}}})
	in := FixtureStepInput{Definition: raw, StepID: "send"}
	if _, err := TestStepWithFixtures(in); err == nil {
		t.Fatal("missing replacement accepted")
	}
	output := ""
	in.FixtureOutput = &output
	result, err := TestStepWithFixtures(in)
	if err != nil || !result.Valid || result.OutputSource != "fixture" || calls.Load() != 0 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, calls.Load())
	}
}

func TestFixtureStepBlocksUnknownEffectsAndDuplicateIDs(t *testing.T) {
	for _, raw := range []string{
		`{"name":"fixtures","steps":[{"id":"wait","type":"wait","wait":{"kind":"datetime","until":"2099-01-01T00:00:00Z"}}]}`,
		`{"name":"fixtures","steps":[{"id":"wait","type":"transform","transform":{"input":"a","expression":"."}},{"id":"wait","type":"transform","transform":{"input":"b","expression":"."}}]}`,
	} {
		if _, err := TestStepWithFixtures(FixtureStepInput{Definition: json.RawMessage(raw), StepID: "wait"}); err == nil {
			t.Fatal("unsupported fixture accepted", raw)
		}
	}
}

func TestFixtureValidationCannotReadExternalSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(path, []byte(`{"type":"integer"}`), 0600); err != nil {
		t.Fatal(err)
	}
	schema, _ := json.Marshal(map[string]any{"$ref": "file://" + path})
	validation := &Validation{Schema: schema}
	// The production loader can read this valid external schema. Fixture mode
	// must not reuse that loader OR its already-compiled cached result.
	if ok, reason := ValidateStepOutput("42", validation); !ok {
		t.Fatal("live schema control failed", reason)
	}
	if ok, reason := validateFixtureOutput("42", validation); ok || !strings.Contains(reason, "schema invalid") {
		t.Fatalf("external schema accepted: %v %s", ok, reason)
	}
	validation.Schema = json.RawMessage(`{"$defs":{"n":{"type":"integer"}},"$ref":"#/$defs/n"}`)
	if ok, reason := validateFixtureOutput("42", validation); !ok {
		t.Fatal("local schema reference rejected", reason)
	}
}

func TestFixtureProjectedReferencesRequireActualProperty(t *testing.T) {
	definition := json.RawMessage(`{"name":"projection","steps":[{"id":"fetch","type":"http","http":{"method":"GET","url":"https://example.com"}},{"id":"project","type":"transform","transform":{"input":"{{ steps.fetch.output.items }}","expression":"."}}]}`)
	for _, raw := range []string{`{}`, `{"other":0}`, `not JSON`} {
		if _, err := TestStepWithFixtures(FixtureStepInput{Definition: definition, StepID: "project", StepOutputs: map[string]string{"fetch": raw}}); err == nil || !strings.Contains(err.Error(), "missing fixture value") {
			t.Fatalf("missing projection accepted: %s %v", raw, err)
		}
	}
	for _, raw := range []string{`{"items":""}`, `{"items":null}`, `{"items":0}`, "```json\n{\"items\":false}\n```"} {
		if !referenceValueExists("steps.fetch.output.items", RenderContext{StepOutputs: map[string]string{"fetch": raw}}) {
			t.Fatalf("explicit property rejected: %s", raw)
		}
	}
	if referenceValueExists("inputs.data.missing", RenderContext{Inputs: map[string]any{"data": "text"}}) {
		t.Fatal("scalar treated as structured fixture")
	}
}
