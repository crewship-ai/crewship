//go:build conformance

// Bind-source reachability, against a real daemon (#1706).
//
// The rest of the conformance harness asks "does this runtime honour the
// HostConfig?". This file asks the question that comes before it: can the
// daemon see the host paths the HostConfig names at all? On a VM-backed runtime
// it frequently cannot, and the whole product stops — crewship-sidecar and
// entrypoint.sh are mandatory binds, and a default Homebrew install puts them
// under /opt/homebrew, which a default Colima's VM does not share.
//
// The existing harness passed on Colima and pointed the wrong way: it stages
// its artefacts into t.TempDir() for its own reasons, which happened to be a
// shareable location. A true result about a path the product never uses.
//
// So this test does not hardcode which paths a runtime shares. It MEASURES the
// share set, then asserts two things against it:
//
//   - a crew container starts when the mandatory binds are staged under a data
//     dir the daemon can reach — the #1706 fix, end to end, on a real daemon;
//   - every directory the daemon cannot reach produces an error that names the
//     runtime and how to share it, rather than "bind source path does not
//     exist" for a file that is plainly there.
//
// Run:
//
//	go test -tags conformance -run TestBindSourceReachability -v ./internal/provider/docker/
//	DOCKER_HOST=unix://$HOME/.colima/default/docker.sock go test -tags conformance -run TestBindSourceReachability -v ./internal/provider/docker/

package docker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// reachableFromDaemon reports whether the daemon can bind-mount dir.
//
// The probe is a create with a deliberately unresolvable image: every engine
// measured (Docker 29.4 via OrbStack, Colima 29.5.2, Rancher Desktop 29.5.3)
// validates the mount config BEFORE it resolves the image, so the create always
// fails and only the SHAPE of the failure carries information. Nothing is
// created, so nothing has to be cleaned up.
func reachableFromDaemon(ctx context.Context, p *Provider, path string) (bool, error) {
	_, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: preflightSentinelImage},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: path, Target: "/probe", ReadOnly: true},
		}},
	})
	if unreachableBindSourcePath(err) != "" {
		return false, err
	}
	return true, err
}

// contentReachableFromDaemon is the probe that matters for a mandatory bind,
// which names a FILE. A directory probe is not enough: a guest can carry a
// same-named empty directory that the host directory is not actually shared
// into, and a directory bind then succeeds while every file inside it is
// missing. That state is not hypothetical — it is what a host running
// `colima ssh -- mkdir -p /Users/<you>` (a workaround somebody reached for)
// looks like, and it turns a hard failure into a silently empty mount.
//
// Falls back to the directory probe where a temp file cannot be written
// (/opt/homebrew, /usr/local), which is the honest limit of what can be
// measured without root.
// It returns the path it actually probed and a cleanup — the probe file has to
// still be on disk when explainBindFailure runs, because "the path exists here
// and the daemon says it does not" is that function's whole discriminator.
// fileLevel reports whether the answer is about a file (trustworthy) or fell
// back to the directory (can be fooled by a same-named guest directory).
func contentReachableFromDaemon(ctx context.Context, p *Provider, dir string) (ok bool, probed string, fileLevel bool, err error, cleanup func()) {
	f, ferr := os.CreateTemp(dir, ".crewship-reach-*")
	if ferr != nil {
		ok, err = reachableFromDaemon(ctx, p, dir)
		return ok, dir, false, err, func() {}
	}
	name := f.Name()
	_, _ = f.WriteString("probe")
	f.Close()
	ok, err = reachableFromDaemon(ctx, p, name)
	return ok, name, true, err, func() { _ = os.Remove(name) }
}

func TestBindSourceReachability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	image := conformanceImageRef()

	// A bare provider, only to ask the daemon questions. Its cfg is replaced
	// below for the crew-start half.
	probeP, cleanup := newConformanceProvider(ctx, t)
	defer cleanup()
	t.Logf("runtime under test: %s %s (%s)", probeP.detected.Runtime, probeP.detected.Version, probeP.detected.Socket)

	home, _ := os.UserHomeDir()
	repoRoot := filepath.Dir(repoFile(t, "go.mod"))
	candidates := []string{home, os.TempDir(), "/private/tmp", repoRoot, "/opt/homebrew", "/usr/local"}

	// fileVerified is the subset of reachable whose CONTENTS the daemon proved
	// it can see. Only those are candidates for the data dir: a directory that
	// binds but whose files do not exist in the guest produces a container that
	// starts with an empty /usr/local/bin/crewship-sidecar, which is worse than
	// a create failure.
	var reachable, fileVerified, unreachable []string
	unreachableProbe := map[string]error{}
	for _, dir := range candidates {
		if _, err := os.Stat(dir); err != nil {
			continue // not a directory on THIS host; nothing to say about it
		}
		ok, probed, fileLevel, err, cleanup := contentReachableFromDaemon(ctx, probeP, dir)
		switch {
		case ok:
			reachable = append(reachable, dir)
			if fileLevel {
				fileVerified = append(fileVerified, dir)
			}
			t.Logf("SHARED     %-40s (daemon can bind it; file-level=%t)", dir, fileLevel)
			cleanup()
		default:
			unreachable = append(unreachable, dir)
			unreachableProbe[dir] = err
			t.Logf("NOT SHARED %-40s %v", dir, err)
			// Deliberately NOT cleaned up yet — see below.
			defer cleanup() //nolint:gocritic // the probe file must outlive the explanation assertions
			_ = probed
		}
	}
	if len(fileVerified) == 0 {
		t.Fatalf("%s cannot bind the CONTENTS of any of %v — no crew can start on it whatever Crewship does", probeP.detected.Runtime, candidates)
	}

	// Half one: every path the daemon cannot see must produce an explanation
	// that names the runtime and a remedy. On a runtime that shares the whole
	// filesystem (OrbStack) there are none, and that is reported rather than
	// asserted away.
	if len(unreachable) == 0 {
		t.Logf("%s shares every candidate path; the unreachable-bind explanation has nothing to explain here", probeP.detected.Runtime)
	}
	for _, dir := range unreachable {
		err := unreachableProbe[dir]
		explained := probeP.explainBindFailure(err).Error()
		for _, want := range []string{probeP.detected.Runtime, "exists on this machine"} {
			if !strings.Contains(explained, want) {
				t.Errorf("the error for unreachable %s does not mention %q — an operator reading it would go looking for a missing file:\n%s", dir, want, explained)
			}
		}
		if explained == err.Error() {
			t.Errorf("the daemon's raw error for unreachable %s was passed through unexplained:\n%s", dir, explained)
		}
		t.Logf("explained %s:\n%s", dir, explained)
	}

	// Half two: the fix. Put the sidecar and entrypoint where an install would
	// — preferring a directory this daemon CANNOT see, which is the #1706
	// scenario — and the data dir somewhere it can. Staging must move the
	// binds into the data dir and the crew must start.
	// CREWSHIP_CONFORMANCE_INSTALL_PREFIX pins the fake install location, so the
	// exact reported scenario can be reproduced verbatim rather than approximated:
	//
	//	CREWSHIP_CONFORMANCE_INSTALL_PREFIX=/opt/homebrew \
	//	  DOCKER_HOST=unix://$HOME/.colima/default/docker.sock \
	//	  go test -tags conformance -run TestBindSourceReachability -v ./internal/provider/docker/
	installBase := os.Getenv("CREWSHIP_CONFORMANCE_INSTALL_PREFIX")
	if installBase == "" {
		installBase = firstNonEmptyList(unreachable, reachable)[0]
	}
	installPrefix := filepath.Join(installBase, "crewship-1706-install")
	dataDir := filepath.Join(fileVerified[0], "crewship-1706-data")
	t.Logf("install prefix (Homebrew-shaped): %s", installPrefix)
	t.Logf("data dir:                         %s", dataDir)
	for _, d := range []string{installPrefix, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		defer os.RemoveAll(d)
	}
	// The data dir is the one that gets handed to uid 1001 by the product's own
	// chown below, so it is the one the host user cannot delete afterwards
	// (#2005). Here that showed up as a silent leak rather than a red run —
	// os.RemoveAll's error is discarded above — which is worse, not better.
	// Registered after those defers so it reclaims before they remove.
	defer reclaimBindOwnership(t, probeP.detected.Host, image, dataDir)

	sidecar := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(sidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(installPrefix, "entrypoint.sh")
	src, err := os.ReadFile(repoFile(t, "scripts", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, src, 0o755); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	staged := stageRuntimeArtifacts(Config{
		OutputBasePath:    dataDir,
		ContainerPrefix:   "crewship-conf1706",
		SidecarBinaryPath: sidecar,
		EntrypointPath:    entrypoint,
	}, logger)
	if staged.SidecarBinaryPath == sidecar {
		t.Fatalf("staging left the sidecar at its install path %s", sidecar)
	}
	t.Logf("sidecar bind source after staging: %s", staged.SidecarBinaryPath)

	p := &Provider{
		client:                probeP.client,
		cfg:                   staged,
		logger:                logger,
		detected:              probeP.detected,
		checkVolumeMountpoint: probeP.checkVolumeMountpoint,
		cgroupVersion:         probeP.cgroupVersion,
		digestResolver:        probeP.digestResolver,
	}
	// This harness is about #1706 — whether the daemon can reach the BIND
	// SOURCES after staging moved them under the data dir. The image is a
	// prerequisite, not the subject, so the provenance is logged and dropped.
	// The helper's tag check still earns its place: this Provider is
	// hand-assembled around a staged Config, and an unnamed image would surface
	// as a create failure that reads exactly like the bind bug under test.
	_ = ensureConformanceImage(ctx, t, p, image)

	crew := provider.CrewConfig{ID: "conf1706crew", Slug: "binds", MemoryMB: 512, CPUs: 1}
	dirs, err := p.prepareCrewDirs(crew)
	if err != nil {
		t.Fatalf("prepare crew dirs: %v", err)
	}
	ensureCrewVolumesOwned(ctx, t, p, image, crew, dirs)

	name := p.CrewContainerName(crew.ID, crew.Slug)
	_, _ = p.client.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})

	cfg, hostCfg, err := p.buildCrewContainerConfig(ctx, crew, name, image, "", 512, 1, dirs)
	if err != nil {
		t.Fatalf("build crew container config: %v", err)
	}
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: cfg, HostConfig: hostCfg, NetworkingConfig: &dockernetwork.NetworkingConfig{}, Name: name,
	})
	if err != nil {
		t.Fatalf("a crew whose sidecar and entrypoint were staged under a data dir %s CAN reach still failed to create — #1706 is not fixed on this runtime: %v",
			p.detected.Runtime, p.explainBindFailure(err))
	}
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer rmCancel()
		_, _ = p.client.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		for _, v := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
			_, _ = p.client.VolumeRemove(rmCtx, v, client.VolumeRemoveOptions{Force: true})
		}
	}()
	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("crew container created but would not start on %s: %v", p.detected.Runtime, err)
	}
	waitForPID1(ctx, t, p, created.ID)

	// Not "it started" — read the staged artefacts back from inside, at the
	// paths the orchestrator and the entrypoint actually use. A container that
	// came up with an empty bind (which a VM CAN produce, when the path exists
	// in the guest but shares nothing) would otherwise read as success.
	out, err := execInContainer(ctx, p, created.ID,
		`echo "SIDECAR_BYTES=$(wc -c < /usr/local/bin/crewship-sidecar 2>/dev/null || echo 0)"; `+
			`echo "ENTRYPOINT_BYTES=$(wc -c < /usr/local/bin/entrypoint.sh 2>/dev/null || echo 0)"; `+
			`echo "PID1=$(cat /proc/1/comm)"`)
	if err != nil {
		t.Fatalf("probe exec failed: %v", err)
	}
	t.Logf("in-container facts:\n%s", out)
	if !strings.Contains(out, "SIDECAR_BYTES=8") {
		t.Errorf("the sidecar bind is present but empty inside the container — the daemon resolved the path to something other than the staged file:\n%s", out)
	}
	if strings.Contains(out, "ENTRYPOINT_BYTES=0") {
		t.Errorf("entrypoint.sh is empty inside the container:\n%s", out)
	}
	t.Logf("PASS: a crew whose sidecar+entrypoint came from an install prefix %s cannot see started and ran on %s, because staging moved the binds under the data dir",
		p.detected.Runtime, p.detected.Runtime)
}

// firstNonEmpty returns the first non-empty list. The unreachable set is
// preferred as the fake install prefix because it reproduces #1706 exactly; on
// a runtime that shares everything there is no such directory and the reachable
// set stands in, which still exercises staging end to end.
func firstNonEmptyList(lists ...[]string) []string {
	for _, l := range lists {
		if len(l) > 0 {
			return l
		}
	}
	return []string{""}
}
