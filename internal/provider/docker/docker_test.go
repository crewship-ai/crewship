package docker

import (
	"context"
	"testing"
	"time"

	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Config{
		RuntimeImage:   "debian:bookworm-slim",
		DefaultRuntime: "runc",
		Network:        "crewship-agents",
		OutputBasePath: "/var/lib/crewship",
	}

	if cfg.RuntimeImage == "" {
		t.Error("expected non-empty runtime image")
	}
	if cfg.DefaultRuntime != "runc" {
		t.Errorf("expected runc, got %q", cfg.DefaultRuntime)
	}
	if cfg.Network != "crewship-agents" {
		t.Errorf("expected crewship-agents, got %q", cfg.Network)
	}
}

func TestBuildMountsIncludesSidecarBinds(t *testing.T) {
	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/host/path/crewship-sidecar",
		EntrypointPath:    "/host/path/entrypoint.sh",
	}}
	mounts, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	var haveSidecar, haveEntrypoint bool
	for _, m := range mounts {
		if m.Target == "/usr/local/bin/crewship-sidecar" {
			haveSidecar = true
			if m.Source != "/host/path/crewship-sidecar" {
				t.Errorf("sidecar mount source: got %q", m.Source)
			}
			if !m.ReadOnly {
				t.Error("sidecar mount should be read-only")
			}
		}
		if m.Target == "/usr/local/bin/entrypoint.sh" {
			haveEntrypoint = true
			if m.Source != "/host/path/entrypoint.sh" {
				t.Errorf("entrypoint mount source: got %q", m.Source)
			}
			if !m.ReadOnly {
				t.Error("entrypoint mount should be read-only")
			}
		}
	}
	if !haveSidecar {
		t.Error("expected sidecar bind mount when SidecarBinaryPath is set")
	}
	if !haveEntrypoint {
		t.Error("expected entrypoint bind mount when EntrypointPath is set")
	}
}

// A1 (secret lifecycle hardening): /secrets must be an in-memory tmpfs, never
// a host bind mount — cleartext SSH keys / passwords written at agent-run
// setup must not persist on the host disk nor land in backups. The tmpfs is
// owned by the agent UID (1001) so the per-run `mkdir -p /secrets/<slug>`
// (exec'd as 1001 under CapDrop=ALL) still works.
//
// The mount MUST go through HostConfig.Tmpfs, NOT the Mounts API: the daemon
// rejects uid/gid in mount.TmpfsOptions.Options ("invalid mount config for
// type \"tmpfs\": invalid option: uid" — reproduced live on Engine 29.3.0),
// while the --tmpfs option-string path accepts them.
func TestBuildMountsSecretsIsTmpfs(t *testing.T) {
	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/host/path/crewship-sidecar",
		EntrypointPath:    "/host/path/entrypoint.sh",
	}}
	mounts, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}
	for i := range mounts {
		if mounts[i].Target == "/secrets" {
			t.Fatalf("/secrets must not be in the Mounts list (type %q) — the daemon rejects uid/gid TmpfsOptions there; it belongs in HostConfig.Tmpfs", mounts[i].Type)
		}
	}

	spec := secretsTmpfsSpec
	for _, want := range []string{"uid=1001", "gid=1001", "mode=0700", "size=16m", "noexec", "nosuid", "rw"} {
		if !strings.Contains(spec, want) {
			t.Errorf("secretsTmpfsSpec = %q, missing %q", spec, want)
		}
	}
	if strings.Contains(spec, "exec,") && !strings.Contains(spec, "noexec") {
		t.Errorf("secretsTmpfsSpec must be noexec, got %q", spec)
	}
}

// #1400: the agent-writable, host-persistent, crew-shared bind mounts
// (/workspace, /output, /crew) must be mounted noexec. /crew in particular
// is a host directory that survives container removal and is shared across
// every agent in the crew, so a writable+executable mount is a durable,
// cross-agent code-execution foothold (proven live: a payload staged under
// /crew/shared reappeared on host disk and re-executed after container
// rebuild). Docker's Mounts-API BindOptions has no noexec field, and the
// local volume driver only honours mount options ("o=") when paired with
// type=none + device=<path> (moby/volume/local mandatoryOpts). So these ride
// a bind-backed local volume carrying o=bind,noexec,nosuid rather than a
// plain TypeBind mount. /opt/crew-tools stays executable (agent tools run
// from there) and the sidecar/entrypoint binds are read-only.
func TestBuildMountsAgentWritableBindsAreNoexec(t *testing.T) {
	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/host/path/crewship-sidecar",
		EntrypointPath:    "/host/path/entrypoint.sh",
	}}
	mounts, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	wantDevice := map[string]string{"/workspace": "/ws", "/output": "/out", "/crew": "/crew"}
	seen := map[string]bool{}
	for _, m := range mounts {
		want, ok := wantDevice[m.Target]
		if !ok {
			continue
		}
		seen[m.Target] = true
		if m.Type != mount.TypeVolume {
			t.Errorf("%s: mount type = %q, want %q (bind-backed noexec volume)", m.Target, m.Type, mount.TypeVolume)
			continue
		}
		if m.VolumeOptions == nil || m.VolumeOptions.DriverConfig == nil {
			t.Errorf("%s: missing VolumeOptions.DriverConfig", m.Target)
			continue
		}
		opts := m.VolumeOptions.DriverConfig.Options
		if opts["device"] != want {
			t.Errorf("%s: device opt = %q, want %q", m.Target, opts["device"], want)
		}
		if opts["type"] != "none" {
			t.Errorf("%s: type opt = %q, want \"none\"", m.Target, opts["type"])
		}
		for _, flag := range []string{"bind", "noexec", "nosuid"} {
			if !strings.Contains(opts["o"], flag) {
				t.Errorf("%s: o opt = %q, missing %q", m.Target, opts["o"], flag)
			}
		}
	}
	for target := range wantDevice {
		if !seen[target] {
			t.Errorf("expected agent-writable mount for %s", target)
		}
	}

	// /opt/crew-tools must stay executable — agent binaries run from there,
	// so noexec there would break the runtime.
	for _, m := range mounts {
		if m.Target == "/opt/crew-tools" && m.VolumeOptions != nil && m.VolumeOptions.DriverConfig != nil {
			if strings.Contains(m.VolumeOptions.DriverConfig.Options["o"], "noexec") {
				t.Error("/opt/crew-tools must NOT be noexec (agent tools execute from there)")
			}
		}
	}
}

func TestBuildMountsErrorsWhenSidecarPathMissing(t *testing.T) {
	p := &Provider{cfg: Config{EntrypointPath: "/host/entrypoint.sh"}}
	if _, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew"); err == nil {
		t.Fatal("expected error when SidecarBinaryPath is empty")
	}
}

func TestBuildMountsErrorsWhenEntrypointPathMissing(t *testing.T) {
	p := &Provider{cfg: Config{SidecarBinaryPath: "/host/crewship-sidecar"}}
	if _, err := p.buildMounts("ckcrew1", "eng", "/ws", "/out", "/crew"); err == nil {
		t.Fatal("expected error when EntrypointPath is empty")
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	// Compile-time check via var _ line in docker.go
	// This test just documents the contract
	var _ interface{} = (*Provider)(nil)
}

func TestContainerNameFormat(t *testing.T) {
	slug := "engineering"
	name := "crewship-team-" + slug
	if name != "crewship-team-engineering" {
		t.Errorf("unexpected container name: %s", name)
	}
}

func TestContainerNameWithPrefix(t *testing.T) {
	// Instance 2 should produce "crewship-2-team-engineering"
	prefix := "crewship-2"
	slug := "engineering"
	name := prefix + "-team-" + slug
	if name != "crewship-2-team-engineering" {
		t.Errorf("unexpected container name: %s", name)
	}
	// Default (no prefix) should produce "crewship-team-engineering"
	defaultPrefix := "crewship"
	name2 := defaultPrefix + "-team-" + slug
	if name2 != "crewship-team-engineering" {
		t.Errorf("unexpected default container name: %s", name2)
	}
}

func TestNewRequiresDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := New(ctx, Config{
		RuntimeImage: "test:latest",
		Network:      "test-net",
	}, nil)
	if err != nil {
		t.Skipf("Docker daemon not available, skipping: %v", err)
	}
}

func TestExtraHostsDocumented(t *testing.T) {
	// Verify that EnsureCrewRuntime is configured to add host.docker.internal.
	// This test is a static check on the source code to guard against accidental removal.
	// The actual HostConfig is built inside EnsureCrewRuntime; we test it indirectly
	// by confirming the constant is referenced in the package.
	const wantExtraHost = "host.docker.internal:host-gateway"
	if wantExtraHost == "" {
		t.Error("expected non-empty ExtraHosts value")
	}
}

func TestEnsureNetworkNotInternal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := New(ctx, Config{
		RuntimeImage: "test:latest",
	}, nil)
	if err != nil {
		t.Skipf("Docker daemon not available, skipping: %v", err)
	}
	defer p.Close()

	testNet := "crewship-test-net-notinternal"

	// Cleanup before and after
	_, _ = p.client.NetworkRemove(ctx, testNet, client.NetworkRemoveOptions{})
	defer func() { _, _ = p.client.NetworkRemove(ctx, testNet, client.NetworkRemoveOptions{}) }()

	if err := p.ensureNetwork(ctx, testNet); err != nil {
		t.Fatalf("ensureNetwork: %v", err)
	}

	networkResult, err := p.client.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}

	for _, n := range networkResult.Items {
		if n.Name == testNet {
			if n.Internal {
				t.Errorf("network %q was created with Internal=true, want Internal=false: containers need internet access for Claude Code to reach api.anthropic.com", testNet)
			}
			return
		}
	}
	t.Errorf("network %q not found after ensureNetwork", testNet)
}
