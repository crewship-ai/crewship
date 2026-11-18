package docker

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func agentCLIProviderFixture(t *testing.T) Config {
	t.Helper()
	install, output := t.TempDir(), t.TempDir()
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	cfg := Config{OutputBasePath: output, SidecarBinaryPath: filepath.Join(install, "crewship-sidecar"), EntrypointPath: filepath.Join(install, "entrypoint.sh"), AgentCLIBinaryPath: filepath.Join(install, "crewship-agent-cli"), RequireAgentCLI: true}
	for _, path := range []string{cfg.SidecarBinaryPath, cfg.AgentCLIBinaryPath} {
		if err := os.WriteFile(path, raw, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cfg.EntrypointPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAgentCLIMountRequiresImmutableReadOnlyGeneration(t *testing.T) {
	cfg := stageRuntimeArtifacts(agentCLIProviderFixture(t), quietLogger())
	p := &Provider{cfg: cfg}
	mounts, err := p.buildMounts("crew-id", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range mounts {
		if m.Target == "/usr/local/bin/crewship" {
			found++
			if !m.ReadOnly || cfg.agentCLIArtifact == nil || m.Source != cfg.agentCLIArtifact.Path {
				t.Fatal("runtime SDK did not mount its qualified immutable generation read-only")
			}
		}
	}
	if found != 1 {
		t.Fatal("clean runtime lacks the native Crewship CLI bind")
	}
}

func TestAgentCLIMissingOrUnqualifiedArtifactFailsPreflight(t *testing.T) {
	for _, kind := range []string{"missing", "script"} {
		t.Run(kind, func(t *testing.T) {
			cfg := agentCLIProviderFixture(t)
			if kind == "missing" {
				cfg.AgentCLIBinaryPath = ""
			} else {
				if err := os.WriteFile(cfg.AgentCLIBinaryPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cfg = stageRuntimeArtifacts(cfg, quietLogger())
			p := &Provider{cfg: cfg}
			if _, err := p.buildMounts("crew-id", "", "", "", ""); err == nil {
				t.Fatal("required agent CLI was silently omitted or mounted without qualification")
			}
		})
	}
}

// The server may share numeric UID1001 with agents. The staged client must
// therefore have no writable alias reachable through crew data mounts.
func TestAgentCLIMountRejectsWritableStagingAliases(t *testing.T) {
	for _, kind := range []string{"ancestor", "symlink-alias", "private-tree"} {
		t.Run(kind, func(t *testing.T) {
			cfg := stageRuntimeArtifacts(agentCLIProviderFixture(t), quietLogger())
			p := &Provider{cfg: cfg}
			source := cfg.OutputBasePath
			if kind == "symlink-alias" {
				source = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(cfg.OutputBasePath, source); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "private-tree" {
				source = filepath.Dir(cfg.agentCLIArtifact.Path)
			}
			if _, err := p.buildMounts("crew-id", "", source, "", ""); err == nil {
				t.Fatal("runtime exposed trusted SDK staging through a writable crew bind")
			}
		})
	}
}
