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
	// Guard against vacuous green: if Crews is somehow empty the
	// for-loop runs zero subtests and the whole suite passes without
	// asserting anything. mustLoadCrews() already panics on zero
	// entries, but pinning the count here means a future loader
	// change can't silently make this test a no-op either.
	if len(Crews) == 0 {
		t.Fatal("Crews is empty — loader regression or stale fixture")
	}
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

// Default demos need Codex and the Python fixture runtime.
func TestPostCreateCommandInstallsDemoCLI(t *testing.T) {
	if len(Crews) == 0 {
		t.Fatal("Crews is empty — loader regression or stale fixture")
	}
	expected := []string{
		"@openai/codex",
	}
	for _, c := range Crews {
		// Opt-in demo crews (e.g. local-ai) are intentionally minimal — they
		// bake only the CLI they need, so exempt them from the "every crew
		// installs all baseline CLIs/deps" contract.
		if c.RequiresEnv != "" {
			continue
		}
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
	if len(Crews) == 0 {
		t.Fatal("Crews is empty — loader regression or stale fixture")
	}
	expected := []string{
		"ripgrep", // Cursor safety net + faster grep tool
		"python3", // tool-sandbox runtime
	}
	for _, c := range Crews {
		// Opt-in demo crews (e.g. local-ai) are intentionally minimal — they
		// bake only the CLI they need, so exempt them from the "every crew
		// installs all baseline CLIs/deps" contract.
		if c.RequiresEnv != "" {
			continue
		}
		postCreate := extractPostCreateCommand(t, c)
		for _, want := range expected {
			if !strings.Contains(postCreate, want) {
				t.Errorf("%s: postCreateCommand missing apt package %q", c.Slug, want)
			}
		}
	}
}

// Business fixtures are local; the live callback host is added at seed time.
func TestSeedCrewsDoNotRequireExternalDemoSites(t *testing.T) {
	for _, c := range Crews {
		if c.RequiresEnv == "" && len(c.AllowedDomains) != 0 {
			t.Errorf("%s: default demo allows external sites: %v", c.Slug, c.AllowedDomains)
		}
	}
}

// extractPostCreateCommand parses a crew's DevcontainerConfig JSON and
// returns the postCreateCommand field as a string. Fails the test if
// the JSON is malformed, the field is missing, the field is a non-
// string type, or the value is blank — pre-fix the helper silently
// returned "" on the latter three cases, which the install-check
// tests below would then quietly pass against an empty string.
func extractPostCreateCommand(t *testing.T, c CrewDef) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(c.DevcontainerConfig), &parsed); err != nil {
		t.Fatalf("%s: invalid devcontainer JSON: %v", c.Slug, err)
	}
	raw, ok := parsed["postCreateCommand"]
	if !ok {
		t.Fatalf("%s: postCreateCommand missing from devcontainer JSON", c.Slug)
	}
	postCreate, ok := raw.(string)
	if !ok {
		t.Fatalf("%s: postCreateCommand is %T, want string", c.Slug, raw)
	}
	if strings.TrimSpace(postCreate) == "" {
		t.Fatalf("%s: postCreateCommand is blank", c.Slug)
	}
	return postCreate
}
