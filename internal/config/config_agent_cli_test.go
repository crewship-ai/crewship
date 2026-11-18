package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Configuration discovery must select the packaged Linux agent client beside
// the other runtime companions, including non-Linux server install layouts.
func TestRuntimeCompanionDiscoveryIncludesPackagedAgentCLI(t *testing.T) {
	for _, layout := range []string{"archive", "homebrew", "fhs"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			companions := bin
			if layout == "homebrew" {
				companions = filepath.Join(root, "libexec")
			}
			if layout == "fhs" {
				companions = filepath.Join(root, "libexec", "crewship")
			}
			if err := os.MkdirAll(companions, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"crewship-sidecar", "entrypoint.sh", "crewship-agent-cli"} {
				if err := os.WriteFile(filepath.Join(companions, name), []byte("discovery fixture; never executed"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := Default()
			if err := resolveSidecarPaths(cfg, bin, ""); err != nil {
				t.Fatal(err)
			}
			raw, err := yaml.Marshal(cfg.Container)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := yaml.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if got := fields["agent_cli_binary_path"]; got != filepath.Join(companions, "crewship-agent-cli") {
				t.Fatal("packaged agent Crewship CLI was not selected for native runtime delivery")
			}
		})
	}
}
