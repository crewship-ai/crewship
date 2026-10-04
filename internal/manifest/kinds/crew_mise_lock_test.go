package kinds

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"gopkg.in/yaml.v3"
)

func TestCrewMiseLockSurvivesYAMLAndServerRoundTrip(t *testing.T) {
	input := `spec:
  mise:
    tools:
      node: "22"
    lock:
      schema_version: 1
      files:
        mise.lock: |
          lockfile_version = 3
        .mise/locks/gemini-cli/1/package.json: '{}'
`
	var doc CrewDocument
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatal(err)
	}
	raw, err := doc.miseJSON()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := devcontainer.ParseMiseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Lock == nil || cfg.Lock.Files[".mise/locks/gemini-cli/1/package.json"] != "{}" {
		t.Fatalf("dropped lock: %s", raw)
	}
	decoded, err := parseMiseConfigJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := yaml.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	var again MiseConfig
	if err := yaml.Unmarshal(wire, &again); err != nil {
		t.Fatal(err)
	}
	raw2, err := (&CrewDocument{Spec: CrewSpec{Mise: &again}}).miseJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !jsonStringEqual(raw, raw2) {
		t.Fatalf("lock changed on roundtrip: %s != %s", raw, raw2)
	}
	// Preserve all bundle input through the adapter augmentation path too.
	plan, err := devcontainer.EnsureAdapterCLIs(&devcontainer.Config{}, raw, []string{"CODEX_CLI"})
	if err != nil {
		t.Fatal(err)
	}
	var configured map[string]any
	if err := json.Unmarshal([]byte(plan.MiseConfig), &configured); err != nil {
		t.Fatal(err)
	}
	if configured["lock"] == nil {
		t.Fatal("adapter planning discarded the lock")
	}
}
