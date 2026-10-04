package devcontainer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateMiseConfigInputBudgets(t *testing.T) {
	bundle := func(content string) map[string]any {
		return map[string]any{"schema_version": 1, "files": map[string]string{"mise.lock": content}}
	}
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"small legacy", `{"tools":{"node":"22"}}`, true},
		{"empty clear", "", true},
		{"native TOML", "[tools]\nnode = \"22\"", true},
		{"bounded lock", encode(map[string]any{"tools": map[string]string{"node": "22"}, "lock": bundle(strings.Repeat("#", 20<<10))}), true},
		{"legacy oversized", encode(map[string]any{"tools": map[string]string{"node": "22"}, "extra": strings.Repeat("x", 11<<10)}), false},
		{"lock does not expand extras", encode(map[string]any{"tools": map[string]string{"node": "22"}, "extra": strings.Repeat("x", 11<<10), "lock": bundle("lockfile_version=3")}), false},
		{"null lock no expansion", encode(map[string]any{"extra": strings.Repeat("x", 11<<10), "lock": nil}), false},
		{"file too large", encode(map[string]any{"tools": map[string]string{"node": "22"}, "lock": bundle(strings.Repeat("#", (128<<10)+1))}), false},
		{"total envelope bounded", strings.Repeat(" ", 4<<20) + `{}`, false},
		{"lock missing tools", encode(map[string]any{"lock": bundle("lockfile_version=3")}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMiseConfigInput(tc.raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
