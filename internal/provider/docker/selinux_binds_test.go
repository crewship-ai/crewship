package docker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/mount"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

func TestPodmanSELinuxCrewBinds(t *testing.T) {
	for _, tc := range []struct {
		runtimeName   string
		enabled, want bool
	}{
		{"docker", true, false}, {"podman", false, false}, {"podman", true, true},
	} {
		cfg := covRTConfig(t)
		cfg.EntrypointPath = filepath.Join(cfg.OutputBasePath, runtimestage.DirName, runtimestage.EntrypointFileName)
		if err := os.MkdirAll(filepath.Dir(cfg.EntrypointPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.EntrypointPath, []byte("#!/bin/sh"), 0755); err != nil {
			t.Fatal(err)
		}
		cfg = stageRuntimeArtifacts(cfg, slog.Default())
		p := (&covRT{}).provider(t, cfg)
		p.detected.Runtime = tc.runtimeName
		p.selinuxEnabled = tc.enabled
		_, host := contractSpec(t, p)
		source, err := filepath.EvalSymlinks(cfg.EntrypointPath)
		if err != nil {
			t.Fatal(err)
		}
		want := source + ":/usr/local/bin/entrypoint.sh:ro,z"
		if slices.Contains(host.Binds, want) != tc.want || len(host.Binds) > 1 {
			t.Fatalf("%s SELinux=%t: binds %v", tc.runtimeName, tc.enabled, host.Binds)
		}
		// Unstaged, operator-supplied sidecar paths must keep structured binds.
		if !slices.ContainsFunc(host.Mounts, func(m mount.Mount) bool {
			return m.Type == mount.TypeBind && m.Source == cfg.SidecarBinaryPath && m.ReadOnly
		}) {
			t.Fatal("lost operator sidecar bind")
		}
		for _, m := range host.Mounts {
			if agentWritableNoexecTargets[m.Target] && m.VolumeOptions.DriverConfig.Options["o"] != noexecBindMountOpts {
				t.Fatalf("lost noexec/nosuid on %s", m.Target)
			}
		}
	}
}

func TestPodmanSELinuxDataPreparation(t *testing.T) {
	f := &covRT{}
	cfg := covRTConfig(t)
	cfg.OutputBasePath = filepath.Join(cfg.OutputBasePath, "custom workspace root")
	p := f.provider(t, cfg)
	p.detected.Runtime, p.selinuxEnabled = "podman", true
	dirs, err := p.prepareCrewDirs(provider.CrewConfig{ID: "crew1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.fixBindMountOwnership(context.Background(), "image", dirs, []string{"home"}); err != nil {
		t.Fatal(err)
	}
	host := f.creates[0].HostConfig
	if len(host.Binds) != 3 || len(host.Mounts) != 1 || host.Mounts[0].Type != mount.TypeVolume {
		t.Fatalf("unexpected preparation mounts: %v %v", host.Binds, host.Mounts)
	}
	for _, dir := range []string{dirs.workspace, dirs.output, dirs.crew} {
		src, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(host.Binds, src+":/mnt"+dir+":rw,z") {
			t.Fatalf("missing shared label for %s", dir)
		}
	}
}

func TestPodmanSELinuxRefusesRedirectedBind(t *testing.T) {
	base := t.TempDir()
	alias := filepath.Join(base, "crew")
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, alias, filepath.Dir(base)} {
		if _, err := sharedCrewBind(base, path, "/mnt/crew", "rw"); err == nil {
			t.Fatalf("accepted unsafe source %q", path)
		}
	}
}

func TestPodmanSELinuxStagedToolsAndFeatureMount(t *testing.T) {
	cfg := covRTConfig(t)
	dir := filepath.Join(cfg.OutputBasePath, runtimestage.DirName)
	if err := os.MkdirAll(filepath.Join(dir, "agent-cli"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg.SidecarBinaryPath = filepath.Join(dir, runtimestage.SidecarFileName)
	cfg.stagedSidecar = true
	cfg.AgentCLIBinaryPath = filepath.Join(dir, "agent-cli", "crewship-agent-cli-test")
	cfg.agentCLIArtifact = &runtimestage.AgentCLIArtifact{Path: cfg.AgentCLIBinaryPath}
	for _, src := range []string{cfg.SidecarBinaryPath, cfg.AgentCLIBinaryPath} {
		if err := os.WriteFile(src, []byte("\x7fELF"), 0555); err != nil {
			t.Fatal(err)
		}
	}
	p := (&covRT{}).provider(t, cfg)
	p.detected.Runtime, p.selinuxEnabled = "podman", true
	team := provider.CrewConfig{ID: "crew1", ExtraMounts: []provider.CrewMount{{Source: "/dev/fuse", Target: "/dev/fuse"}}}
	dirs, err := p.prepareCrewDirs(team)
	if err != nil {
		t.Fatal(err)
	}
	_, host, err := p.assembleCrewSpec(team, "image", "", 256, 1, dirs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.Binds) != 2 {
		t.Fatalf("unexpected shared binds: %v", host.Binds)
	}
	for src, target := range map[string]string{cfg.SidecarBinaryPath: "/usr/local/bin/crewship-sidecar", cfg.AgentCLIBinaryPath: agentCLIExecutable} {
		canonical, err := filepath.EvalSymlinks(src)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(host.Binds, canonical+":"+target+":ro,z") {
			t.Fatalf("missing read-only staged bind for %s", src)
		}
	}
	if !slices.ContainsFunc(host.Mounts, func(m mount.Mount) bool {
		return m.Type == mount.TypeBind && m.Source == "/dev/fuse" && m.Target == "/dev/fuse"
	}) {
		t.Fatal("expected unchanged feature bind")
	}
}
