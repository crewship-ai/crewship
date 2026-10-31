package config

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// artifactsConfigPage is the published Artifacts configuration reference. Its
// YAML template must stay loadable: an unknown or misspelled key would be
// silently ignored by the server, so the example is decoded strictly here.
const artifactsConfigPage = "../../docs/configuration/artifacts.mdx"

// docPlaceholders are the qualification values the page tells operators to
// supply themselves. Each is replaced by a value of the shape the server
// requires (SHA-256 hex digests, a 40-character engine revision pin).
// Validate does not check those pins, so this test proves the template
// decodes and validates, not that its profiles qualify.
var docPlaceholders = strings.NewReplacer(
	"<bundle-sha256>", strings.Repeat("a", 64),
	"<cli-sha256>", strings.Repeat("b", 64),
	"<engine-revision>", strings.Repeat("c", 40),
)

var yamlFence = regexp.MustCompile("(?s)```yaml\n(.*?)```")

func strictDecode(t *testing.T, block string) (*Config, error) {
	t.Helper()
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader([]byte(block)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

func TestArtifactsConfigurationDocExamplesLoad(t *testing.T) {
	t.Setenv("CREWSHIP_ALLOWED_ORIGINS", "") // Validate reads it; keep the result independent of the host.
	page, err := os.ReadFile(artifactsConfigPage)
	if err != nil {
		t.Fatal(err)
	}
	blocks := yamlFence.FindAllStringSubmatch(string(page), -1)
	if len(blocks) == 0 {
		t.Fatal("no yaml example found in the Artifacts configuration page")
	}
	for i, m := range blocks {
		block := docPlaceholders.Replace(m[1])
		if left := regexp.MustCompile(`<[a-z0-9-]+>`).FindString(block); left != "" {
			t.Fatalf("example %d: placeholder %s has no test value; add it to docPlaceholders", i, left)
		}
		cfg, err := strictDecode(t, block)
		if err != nil {
			t.Fatalf("example %d does not load as documented: %v", i, err)
		}
		if i == 0 {
			// The template claims to start these services; keep the page honest.
			if !cfg.Documents.Enabled || !cfg.GridCraft.Enabled || !cfg.GridCraft.EditorEnabled ||
				!cfg.Slides.Enabled || !cfg.Mooring.Enabled || !cfg.CraftNative.Enabled {
				t.Fatalf("template no longer enables the documented services: %+v", cfg)
			}
			if _, ok := cfg.CraftNative.Profiles["wordcraft"]; !ok {
				t.Fatal("template no longer carries the wordcraft profile")
			}
		}
	}
}

func TestArtifactsConfigurationDocExamplesRejectUnknownKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
	}{
		{"misspelled section", "documment:\n  enabled: true\n"},
		{"misspelled key", "documents:\n  enabeld: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := strictDecode(t, tc.block); err == nil {
				t.Fatal("strict decoding accepted an unknown key")
			}
		})
	}
}
