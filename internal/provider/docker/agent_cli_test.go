package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
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

func TestAgentCLIUpgradeDefersWhileCrewRuntimeIsHeld(t *testing.T) {
	p, daemon, sidecarHash := newSidecarRefreshFixture(t, "", "running")
	// Keep the sidecar current so only the missing/stale SDK can request refresh.
	daemon.containers[refreshOldID].labels[crewSidecarHashLabel] = sidecarHash
	fixture := stageRuntimeArtifacts(agentCLIProviderFixture(t), quietLogger())
	p.cfg.AgentCLIBinaryPath = fixture.AgentCLIBinaryPath
	p.cfg.agentCLIArtifact = fixture.agentCLIArtifact
	p.cfg.RequireAgentCLI = true
	p.cfg.ExpectedAgentCLICommit = strings.Repeat("b", 40)
	p.cfg.RequiredAgentCLICommands = []string{"commands", "version"}
	daemon.sdk = &agentSDKFake{t: t, artifact: p.cfg.agentCLIArtifact, commit: p.cfg.ExpectedAgentCLICommit}
	p.SetRuntimeIdleVerifier(func(context.Context, string, string) error { return nil })
	release, err := p.runtimeUseGate(refreshCrewID).Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	use, err := p.AcquireCrewRuntimeUse(ctx, refreshCrew())
	if use != nil {
		use.Release()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale SDK must wait for existing crew use instead of admitting an old client: %v", err)
	}
	if daemon.count("stop ") != 0 || daemon.count("remove ") != 0 {
		t.Fatal("SDK upgrade interrupted active crew work")
	}
	release()
	released = true
	upgraded, err := p.AcquireCrewRuntimeUse(t.Context(), refreshCrew())
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Release()
	if upgraded.ContainerID() == refreshOldID {
		t.Fatal("SDK did not converge to a fresh runtime after the crew became idle")
	}
	current := daemon.crewContainer()
	if current == nil || current.labels["crewship.agent-cli-sha256"] != p.cfg.agentCLIArtifact.SHA256 {
		t.Fatal("replacement runtime lacks its complete SDK generation digest")
	}
}

func TestAgentCLIProbeRejectsStaleOrIncompleteClient(t *testing.T) {
	artifact := &runtimestage.AgentCLIArtifact{SHA256: strings.Repeat("a", 64), Arch: "amd64"}
	commit := strings.Repeat("b", 40)
	valid := buildinfo.AgentRuntimeProbe{Protocol: 1, SHA256: artifact.SHA256, OS: "linux", Arch: "amd64", Commit: commit, Version: "fixture", Commands: []string{"commands", "version"}}
	if err := validateAgentCLIProbe(valid, artifact, commit, []string{"version", "commands"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"protocol", "hash", "os", "arch", "commit", "unknown-commit", "missing-command", "duplicate-command", "invalid-command"} {
		t.Run(kind, func(t *testing.T) {
			probe := valid
			probe.Commands = append([]string(nil), valid.Commands...)
			expected := commit
			switch kind {
			case "protocol":
				probe.Protocol = 2
			case "hash":
				probe.SHA256 = strings.Repeat("c", 64)
			case "os":
				probe.OS = "darwin"
			case "arch":
				probe.Arch = "arm64"
			case "commit":
				probe.Commit = strings.Repeat("d", 40)
			case "unknown-commit":
				expected = ""
			case "missing-command":
				probe.Commands = []string{"commands"}
			case "duplicate-command":
				probe.Commands = append(probe.Commands, "version")
			case "invalid-command":
				probe.Commands = append(probe.Commands, "invalid\nmetadata")
			}
			if err := validateAgentCLIProbe(probe, artifact, expected, []string{"version", "commands"}); err == nil {
				t.Fatal("unqualified native CLI probe was admitted")
			}
		})
	}
	if err := validateAgentCLIProbe(valid, artifact, commit, []string{"keeper execute"}); err == nil {
		t.Fatal("backend advertised native Keeper on a client without that command")
	}
}

func TestAgentCLIRequiredMissingClientCannotReuseLiveRuntime(t *testing.T) {
	p, daemon, hash := newSidecarRefreshFixture(t, "", "running")
	daemon.containers[refreshOldID].labels[crewSidecarHashLabel] = hash
	p.cfg.RequireAgentCLI = true
	if _, err := p.EnsureCrewRuntime(t.Context(), refreshCrew()); err == nil {
		t.Fatal("existing runtime was advertised ready without its required native CLI")
	}
	if daemon.count("stop ") != 0 || daemon.count("remove ") != 0 {
		t.Fatal("missing installation artifact interrupted a live crew")
	}
}

func TestAgentCLIRuntimeAdmissionRejectsUnsafeActualResponses(t *testing.T) {
	for _, fault := range []string{"privileged", "capability", "missing-no-new-privileges", "writable-sdk", "writable-staging-volume", "writable-sdk-alias", "writable-sdk-volume-alias", "parent-alias", "image-architecture", "damaged-probe", "stale-probe-commit"} {
		t.Run(fault, func(t *testing.T) {
			p, daemon, hash := newSidecarRefreshFixture(t, "", "running")
			fixture := stageRuntimeArtifacts(agentCLIProviderFixture(t), quietLogger())
			p.cfg.AgentCLIBinaryPath = fixture.AgentCLIBinaryPath
			p.cfg.agentCLIArtifact = fixture.agentCLIArtifact
			p.cfg.RequireAgentCLI = true
			p.cfg.ExpectedAgentCLICommit = strings.Repeat("b", 40)
			p.cfg.RequiredAgentCLICommands = []string{"commands", "version"}
			daemon.containers[refreshOldID].labels[crewSidecarHashLabel] = hash
			daemon.containers[refreshOldID].labels["crewship.agent-cli-sha256"] = fixture.agentCLIArtifact.SHA256
			daemon.sdk = &agentSDKFake{t: t, artifact: fixture.agentCLIArtifact, commit: p.cfg.ExpectedAgentCLICommit, fault: fault}
			if fault == "stale-probe-commit" {
				daemon.sdk.commit = strings.Repeat("c", 40)
			}
			team := refreshCrew()
			ready := false
			team.ProvisionSink = func(event devcontainer.ProvisionEvent) { ready = ready || event.Step == devcontainer.ProvStepReady }
			if _, err := p.EnsureCrewRuntime(t.Context(), team); err == nil {
				t.Fatal("runtime preflight admitted an unsafe or stale SDK response")
			}
			if ready {
				t.Fatal("failed SDK preflight was advertised as a ready runtime")
			}
		})
	}
}

func TestAgentCLIQualificationRequiresMappedBind(t *testing.T) {
	for _, useDaemonPath := range []bool{true, false} {
		name := "local spelling"
		if useDaemonPath {
			name = "daemon spelling"
		}
		t.Run(name, func(t *testing.T) {
			p, daemon, _ := newSidecarRefreshFixture(t, "", "running")
			cfg := agentCLIProviderFixture(t)
			var err error
			cfg.OutputBasePath, err = filepath.EvalSymlinks(cfg.OutputBasePath)
			if err != nil {
				t.Fatal(err)
			}
			staged := stageRuntimeArtifacts(cfg, quietLogger())
			p.cfg.agentCLIArtifact = staged.agentCLIArtifact
			p.cfg.ExpectedAgentCLICommit = strings.Repeat("b", 40)
			p.cfg.RequiredAgentCLICommands = []string{"commands", "version"}
			p.pathMap = &daemonPathMap{local: cfg.OutputBasePath, daemon: "/fixture-daemon-storage"}
			daemon.containers[refreshOldID].labels["crewship.agent-cli-sha256"] = staged.agentCLIArtifact.SHA256
			source := staged.agentCLIArtifact.Path
			if useDaemonPath {
				source, err = p.pathMap.translate(source)
				if err != nil {
					t.Fatal(err)
				}
			}
			daemon.sdk = &agentSDKFake{t: t, artifact: staged.agentCLIArtifact, commit: p.cfg.ExpectedAgentCLICommit, mountedSource: source}
			if err := p.qualifyAgentCLI(t.Context(), refreshOldID); (err == nil) != useDaemonPath {
				t.Fatalf("mapped bind qualification = %v, want success %t", err, useDaemonPath)
			}
		})
	}
}
