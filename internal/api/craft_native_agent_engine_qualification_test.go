//go:build craft_native_engine_qualification

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Existing CRAFT_QUALIFICATION_* variables are test-only release qualification
// inputs, not server startup configuration. This test must execute explicitly
// against each frozen original CLI; its build tag is not default-CI evidence.
func TestCraftNativeAgentCLIOriginalEngineQualification(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("qualification input %s is required", name)
		}
		return value
	}
	p := CraftNativeDocumentProfile{Application: required("CRAFT_QUALIFICATION_APP"), EngineRevision: required("CRAFT_QUALIFICATION_REVISION"), CLIPath: required("CRAFT_QUALIFICATION_ENGINE"), CLISHA256: required("CRAFT_QUALIFICATION_SHA256"), EditorBundle: strings.Repeat("c", 64)}
	raw, err := os.ReadFile(required("CRAFT_QUALIFICATION_FIRST"))
	if err != nil {
		t.Fatal(err)
	}
	a := NewCraftNativeAgentCLIAdapter()
	var commands json.RawMessage
	switch p.Application {
	case "wordcraft":
		commands = json.RawMessage(`[{"command":"caret.docStart","params":{}},{"command":"text.insert","params":{"text":"Native agent engine proof $(literal)\n"}},{"command":"caret.docEnd","params":{}},{"command":"text.insert","params":{"text":"\nNative engine suffix"}}]`)
	case "gridcraft":
		commands = json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"41"}},{"command":"cell.set","params":{"cell":"B1","input":"42"}}]`)
	default:
		t.Fatal("qualification requires an original Word or Grid CLI")
	}
	changed, err := a.Apply(t.Context(), p, raw, commands)
	if err != nil || bytes.Equal(raw, changed) {
		t.Fatalf("original engine did not mutate the native document: %v", err)
	}
	v := mooring.NativeCLIValidator{Application: p.Application, BinaryPath: p.CLIPath, SHA256: p.CLISHA256}
	if err := v.Validate(t.Context(), changed); err != nil {
		t.Fatalf("original engine rejected its mutated native candidate: %v", err)
	}
	if p.Application == "wordcraft" {
		before, err := a.Inspect(t.Context(), p, raw, json.RawMessage(`{"command":"text","params":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		after, err := a.Inspect(t.Context(), p, changed, json.RawMessage(`{"command":"text","params":{}}`))
		var first, second struct{ Text string }
		if err != nil || json.Unmarshal(before, &first) != nil || json.Unmarshal(after, &second) != nil || !strings.Contains(second.Text, "Native agent engine proof $(literal)") || !strings.Contains(second.Text, "Native engine suffix") || !strings.Contains(second.Text, first.Text) {
			t.Fatal("original Word read did not retain both inserted text and prior body")
		}
	} else {
		for cell, want := range map[string]float64{"A1": 41, "B1": 42} {
			query, _ := json.Marshal(map[string]any{"command": "cell.get", "params": map[string]string{"cell": cell}})
			result, err := a.Inspect(t.Context(), p, changed, query)
			var value struct {
				Command string `json:"command"`
				Result  struct {
					Cell  string  `json:"cell"`
					Value float64 `json:"value"`
				} `json:"result"`
			}
			if err != nil || json.Unmarshal(result, &value) != nil || value.Command != "cell.get" || value.Result.Cell != cell || value.Result.Value != want {
				t.Fatalf("original Grid read did not verify cell %s: %v", cell, err)
			}
		}
	}
	t.Logf("%s exact pinned original CLI apply+native validator+engine inspect PASS; native=%dB hash=%s", p.Application, len(changed), fmt.Sprintf("sha256:%x", sha256.Sum256(changed)))
}
