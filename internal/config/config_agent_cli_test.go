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

func TestRuntimeAgentCLIExplicitOverrideSurvivesDiscovery(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(root, "operator-selected-client")
	t.Setenv("CREWSHIP_AGENT_CLI_PATH", override)
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	cfg := Default()
	applyEnvOverrides(cfg)
	if err := resolveSidecarPaths(cfg, root, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Container.AgentCLIBinaryPath != override {
		t.Fatal("agent CLI override was lost during runtime discovery")
	}
}

func TestRuntimeAgentCLIDiscoveryRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "crewship-agent-cli"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	if err := resolveSidecarPaths(cfg, root, ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Container.AgentCLIBinaryPath == filepath.Join(root, "crewship-agent-cli") {
		t.Fatal("a directory was accepted as the runtime CLI artifact")
	}
}
