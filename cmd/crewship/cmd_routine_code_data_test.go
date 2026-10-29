package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This must use a built CLI: invalid definitions exit the process in the
// command's error path, so a direct RunE call is not an acceptance test.
func TestAcceptance_RoutineValidate_CodeTemplates(t *testing.T) {
	bin := buildCrewshipBinary(t)
	for _, tc := range []struct {
		name, code string
		valid      bool
	}{
		{"typed", `inputs.value > 5`, true},
		{"legacy-simple", `{{ inputs.value }} > 5`, true},
		{"legacy-quoted", `"{{ inputs.value }}" == "approved"`, true},
		{"legacy-embedded", `"prefix {{ inputs.value }}" == "approved"`, false},
		{"legacy-complex", `{{ inputs.value + 1 }} > 5`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"dsl_version": "1.0", "name": "code-template-validation", "agentless": true,
				"inputs": []any{map[string]any{"name": "value", "type": "string"}},
				"steps": []any{map[string]any{"id": "predicate", "type": "code",
					"code": map[string]any{"runtime": "cel", "code": tc.code}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "routine.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := runCrewship(t, bin, trustCLIConfig(t), "http://127.0.0.1:0", "routine", "validate", path, "--no-color")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v output=%s", tc.valid, err, out)
			}
			if !tc.valid && !strings.Contains(out, "typed") {
				t.Fatalf("missing migration guidance: %s", out)
			}
		})
	}
}
