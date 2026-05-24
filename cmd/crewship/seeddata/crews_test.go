package seeddata

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCrewDevcontainerConfigIsValidJSON guards against template-string typos
// that would silently break devcontainer provisioning at first crew create.
// Each crew's DevcontainerConfig ships as a string in builtin/crews.yaml —
// easy to land an unbalanced quote that only surfaces when the JSON is
// parsed deep inside the provisioner.
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

// TestPostCreateCommandInstallsAllFiveNewCLIs pins that EVERY seeded crew's
// postCreateCommand references each of the five CLIs we want available
// alongside Claude Code. If a future YAML edit drops one to "save
// provisioning time" without updating docs/UI, this test fails loudly.
//
// Pre-migration this test checked the shared baseCLIPostCreate const.
// Post-migration the postCreate string is inlined per-crew in
// builtin/crews.yaml — the test sweeps every crew so divergence between
// crews (a future YAML edit that touches only some of them) also surfaces.
func TestPostCreateCommandInstallsAllFiveNewCLIs(t *testing.T) {
	expected := []string{
		"@openai/codex",
		"@google/gemini-cli",
		"opencode-ai",
		"cursor.com/install",
		"app.factory.ai/cli", // Droid installer
	}
	for _, c := range Crews {
		postCreate := extractPostCreateCommand(t, c)
		for _, want := range expected {
			if !strings.Contains(postCreate, want) {
				t.Errorf("%s: postCreateCommand missing reference to %q — CLI would not be installed",
					c.Slug, want)
			}
		}
	}
}

// TestPostCreateCommandInstallsContainerDeps pins the apt packages the CLIs
// need (Droid needs xdg-utils, Cursor benefits from system ripgrep, all
// CLIs may shell out to python3). Same per-crew sweep as the CLI install
// test above.
func TestPostCreateCommandInstallsContainerDeps(t *testing.T) {
	expected := []string{
		"xdg-utils", // Droid Linux requirement
		"ripgrep",   // Cursor safety net + faster grep tool
		"python3",   // tool-sandbox runtime
	}
	for _, c := range Crews {
		postCreate := extractPostCreateCommand(t, c)
		for _, want := range expected {
			if !strings.Contains(postCreate, want) {
				t.Errorf("%s: postCreateCommand missing apt package %q", c.Slug, want)
			}
		}
	}
}

// extractPostCreateCommand parses a crew's DevcontainerConfig JSON and
// returns the postCreateCommand field as a string. Fails the test if the
// JSON is malformed or the field is missing — both upstream errors that
// the validJSON test above will report first, so failures here are rare.
func extractPostCreateCommand(t *testing.T, c CrewDef) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(c.DevcontainerConfig), &parsed); err != nil {
		t.Fatalf("%s: invalid devcontainer JSON: %v", c.Slug, err)
	}
	postCreate, _ := parsed["postCreateCommand"].(string)
	return postCreate
}
