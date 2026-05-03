package seeddata

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCrewDevcontainerConfigIsValidJSON guards against template-string typos
// that would silently break devcontainer provisioning at first crew create.
// Each crew's DevcontainerConfig is a string assembled from constants and
// concatenations — easy to land an unbalanced quote that only surfaces when
// the JSON is parsed deep inside the provisioner.
func TestCrewDevcontainerConfigIsValidJSON(t *testing.T) {
	for _, c := range Crews {
		t.Run(c.Slug, func(t *testing.T) {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(c.DevcontainerConfig), &parsed); err != nil {
				t.Fatalf("invalid JSON for %s: %v\nconfig: %s", c.Slug, err, c.DevcontainerConfig)
			}
			if parsed["image"] == nil {
				t.Errorf("%s: missing image", c.Slug)
			}
			if _, ok := parsed["features"].(map[string]any); !ok {
				t.Errorf("%s: missing or malformed features map", c.Slug)
			}
			if _, ok := parsed["postCreateCommand"].(string); !ok {
				t.Errorf("%s: missing postCreateCommand (multi-CLI install would be skipped)", c.Slug)
			}
			env, _ := parsed["containerEnv"].(map[string]any)
			if env == nil {
				t.Fatalf("%s: missing containerEnv", c.Slug)
			}
			path, _ := env["PATH"].(string)
			if !strings.Contains(path, "/home/agent/.local/bin") {
				t.Errorf("%s: PATH must include /home/agent/.local/bin so npm-installed CLIs resolve, got %q", c.Slug, path)
			}
		})
	}
}

// TestPostCreateCommandInstallsAllFourNewCLIs pins that the multi-CLI install
// script references each of the four CLIs we want available alongside Claude
// Code. If anyone removes one to "save provisioning time" without updating
// docs/UI, this test fails loudly.
func TestPostCreateCommandInstallsAllFourNewCLIs(t *testing.T) {
	expected := []string{
		"@openai/codex",
		"@google/gemini-cli",
		"opencode-ai",
		"cursor.com/install",
	}
	for _, want := range expected {
		if !strings.Contains(baseCLIPostCreate, want) {
			t.Errorf("baseCLIPostCreate missing reference to %q — CLI would not be installed", want)
		}
	}
}
