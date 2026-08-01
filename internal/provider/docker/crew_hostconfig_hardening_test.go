package docker

// Baseline hardening of the crew container's Config/HostConfig (#1626).
//
// The sidecar (sidecar.go) has carried Labels, a RestartPolicy and resource
// caps since the H7/F6 audits; the crew container — longer-lived, holding
// credentials, and the thing every agent actually runs in — carried almost
// none of it. Each test below pins one of those fields on the create request
// that actually reaches the daemon, via the covRT fake in
// docker_container_cov_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// crewCreate drives EnsureCrewRuntime against the covRT fake and returns the
// create request for the agent container.
func crewCreate(t *testing.T, mutate func(cfg *covRT)) *container.CreateRequest {
	t.Helper()
	f := &covRT{}
	if mutate != nil {
		mutate(f)
	}
	p := f.provider(t, covRTConfig(t))
	if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	return f.realCreate(t)
}

// PID 1 in the crew container is `exec sleep infinity` (scripts/entrypoint.sh)
// and the sidecar is started as `… &` inside an `sh -c` exec, so it is ALWAYS
// an orphan reparented onto PID 1. sleep(1) does not reap, so every such orphan
// becomes a permanent zombie counting against PidsLimit: 200 — a monotonic leak
// ending in `fork: Resource temporarily unavailable` for the whole crew.
//
// Init must therefore be on for every crew, not only when a devcontainer
// feature happens to request it (the old `boolPtrIf(team.Init)`, which yielded
// nil — Docker's default of "no init" — for essentially every real crew).
func TestCrewContainer_InitIsAlwaysOn(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	if req.HostConfig.Init == nil {
		t.Fatal("HostConfig.Init = nil (daemon default: no init) — orphans of `sleep infinity` are never reaped")
	}
	if !*req.HostConfig.Init {
		t.Error("HostConfig.Init = false, want true")
	}
}

// HostConfig.GroupAdd is set at container create and moby's execSetPlatformOpt
// folds it into every subsequent exec's AdditionalGids, so this is the only
// lever that gives agent execs supplementary groups. Verified live on dev1
// before this change: `id` inside a crew container returned
// `uid=1001(agent) gid=1001(agent) groups=1001(agent)` — no supplementary
// groups at all, because the `User: "1001:1001"` colon form makes moby's
// Sgids branch unreachable.
func TestCrewContainer_GroupAddCarriesCrewAndSidecarGIDs(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	got := map[string]bool{}
	for _, g := range req.HostConfig.GroupAdd {
		got[g] = true
	}
	for _, want := range []string{crewGroupGID, sidecarGroupGID} {
		if !got[want] {
			t.Errorf("GroupAdd = %v, missing gid %s", req.HostConfig.GroupAdd, want)
		}
	}
}

// Docker defaults MemorySwap to 2x Memory when it is left unset, so a crew
// configured for N MiB of RAM silently gets N MiB of swap on top. Swapping is
// worse than a clean OOM here: it turns a bounded failure into minutes of
// thrash that degrades every co-resident crew on the host.
func TestCrewContainer_SwapIsDisabled(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	res := req.HostConfig.Resources
	if res.Memory <= 0 {
		t.Fatalf("Memory = %d, want a positive limit", res.Memory)
	}
	if res.MemorySwap != res.Memory {
		t.Errorf("MemorySwap = %d, want == Memory (%d) so the container gets zero swap", res.MemorySwap, res.Memory)
	}
	if res.MemorySwappiness == nil {
		t.Fatal("MemorySwappiness = nil, want 0")
	}
	if *res.MemorySwappiness != 0 {
		t.Errorf("MemorySwappiness = %d, want 0", *res.MemorySwappiness)
	}
}

// The important one is core=0. The exec CWD is /output/<slug>, a
// host-persistent bind, and containerd's default LimitCORE is infinity — so a
// crashing agent writes a core dump containing every credential in its exec
// environment straight onto host disk, defeating the entire /secrets-as-tmpfs
// design (docker.go secretsTmpfsSpec).
func TestCrewContainer_Ulimits(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	byName := map[string]*container.Ulimit{}
	for _, u := range req.HostConfig.Ulimits {
		byName[u.Name] = u
	}

	tests := []struct {
		name       string
		wantSoft   int64
		wantHard   int64
		wantExact  bool // core must be exactly 0/0; the rest only need to be bounded
		mustBeSane bool
	}{
		{name: "core", wantSoft: 0, wantHard: 0, wantExact: true},
		{name: "nofile", mustBeSane: true},
		{name: "nproc", mustBeSane: true},
		{name: "fsize", mustBeSane: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, ok := byName[tt.name]
			if !ok {
				t.Fatalf("Ulimits = %v, missing %q", req.HostConfig.Ulimits, tt.name)
			}
			if tt.wantExact {
				if u.Soft != tt.wantSoft || u.Hard != tt.wantHard {
					t.Errorf("%s = soft %d / hard %d, want %d/%d", tt.name, u.Soft, u.Hard, tt.wantSoft, tt.wantHard)
				}
				return
			}
			if tt.mustBeSane {
				if u.Soft <= 0 || u.Hard <= 0 {
					t.Errorf("%s = soft %d / hard %d, want a positive bound", tt.name, u.Soft, u.Hard)
				}
				if u.Soft > u.Hard {
					t.Errorf("%s soft %d > hard %d", tt.name, u.Soft, u.Hard)
				}
				// containerd's inherited defaults are 1048576 (nofile) and
				// unlimited (nproc/fsize); anything at or above that is not a
				// bound.
				if u.Hard >= 1<<20 && tt.name == "nofile" {
					t.Errorf("nofile hard = %d, no tighter than the inherited containerd default", u.Hard)
				}
			}
		})
	}
}

// /dev/shm defaults to 64 MB, verified live on dev1. Chromium (and therefore
// Playwright, which this repo itself ships) crashes on that.
func TestCrewContainer_ShmSizeIsOneGiB(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	const want = int64(1024 * 1024 * 1024)
	if req.HostConfig.ShmSize != want {
		t.Errorf("ShmSize = %d, want %d (1 GiB)", req.HostConfig.ShmSize, want)
	}
}

// Every crew-container discovery path today (FindCrewContainer,
// PruneCrewRuntimes, the orphan reaper) is name-based and driven off the DB.
// A container whose DB row is gone — crew deleted while the daemon was
// unreachable, DB restored from an older backup, container_prefix changed — is
// invisible to all of them and keeps its mounts, network and credentials.
// Labels give those paths something to find. The sidecar has carried them
// since the H7 audit (sidecar.go).
func TestCrewContainer_CarriesDiscoveryLabels(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	want := map[string]string{
		"managed-by":       "crewship",
		"crewship.kind":    "crew",
		"crewship.crew":    "alpha",
		"crewship.crew-id": "crew1",
	}
	for k, v := range want {
		if got := req.Config.Labels[k]; got != v {
			t.Errorf("label %q = %q, want %q", k, got, v)
		}
	}
}

// Parity with the sidecar, which has had a restart policy since the H7 audit.
func TestCrewContainer_RestartPolicyAndStopTimeout(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	rp := req.HostConfig.RestartPolicy
	if rp.Name != container.RestartPolicyOnFailure {
		t.Errorf("RestartPolicy.Name = %q, want %q", rp.Name, container.RestartPolicyOnFailure)
	}
	if rp.MaximumRetryCount <= 0 {
		t.Errorf("RestartPolicy.MaximumRetryCount = %d, want a bounded positive count", rp.MaximumRetryCount)
	}
	if req.Config.StopTimeout == nil {
		t.Fatal("Config.StopTimeout = nil, want an explicit grace period")
	}
	if *req.Config.StopTimeout != crewStopTimeoutSeconds {
		t.Errorf("Config.StopTimeout = %d, want %d", *req.Config.StopTimeout, crewStopTimeoutSeconds)
	}
}

// The healthcheck tested `test -f /workspace/.ready`, but /workspace is a
// host-persistent bind — once ANY container for that crew has booted,
// entrypoint.sh's marker survives container removal, so a fresh container
// reports healthy at t=0 forever. Nothing consumed the result either:
// ContainerStatus switches on State.Running/Restarting/Dead/OOMKilled and
// never looks at State.Health (the only State.Health reader in the repo is
// waitSidecarHealthy, which is scoped to sidecars). It cost one container exec
// per crew per 30 s and bought nothing.
func TestCrewContainer_NoHealthcheck(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	if req.Config.Healthcheck != nil {
		t.Errorf("Config.Healthcheck = %+v, want nil — the /workspace/.ready probe is a tautology and nobody reads State.Health",
			req.Config.Healthcheck)
	}
}

// Non-ASCII paths and filenames are otherwise mangled under the POSIX locale,
// and an unset TZ leaves the container on whatever the image happens to ship.
func TestCrewContainer_LocaleAndTimezoneDefaults(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	got := map[string]string{}
	for _, e := range req.Config.Env {
		if eq := strings.IndexByte(e, '='); eq > 0 {
			got[e[:eq]] = e[eq+1:]
		}
	}
	if got["LANG"] != "C.UTF-8" {
		t.Errorf("LANG = %q, want C.UTF-8", got["LANG"])
	}
	if got["TZ"] == "" {
		t.Errorf("TZ is unset; env = %v", req.Config.Env)
	}
}

// A base image that bakes LANG into its own ENV has made a deliberate choice.
// A container-level entry would silently override it, so the default must not
// be emitted at all when the image already declares the key.
func TestCrewContainer_ImageEnvSuppressesLocaleDefault(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, func(f *covRT) {
		f.imgInspectFn = func(int) (string, int) {
			return `{"Id":"sha256:cov","RepoDigests":[],"Config":{"Env":["PATH=/usr/bin:/bin","LANG=ja_JP.UTF-8"]}}`, 200
		}
	})
	for _, e := range req.Config.Env {
		if strings.HasPrefix(e, "LANG=") {
			t.Errorf("env carries %q; the image's own LANG must not be overridden", e)
		}
	}
	// TZ is not in the image ENV, so its default still applies.
	found := false
	for _, e := range req.Config.Env {
		if e == "TZ=UTC" {
			found = true
		}
	}
	if !found {
		t.Errorf("env = %v, want TZ=UTC (the image declares no TZ)", req.Config.Env)
	}
}

// The defaults are defaults: a devcontainer that declares its own LANG/TZ in
// containerEnv must keep them, and must not end up with two conflicting
// entries for the same key.
func TestCrewContainer_ContainerEnvOverridesLocaleDefaults(t *testing.T) {
	t.Parallel()

	f := &covRT{}
	p := f.provider(t, covRTConfig(t))
	team := covTeam()
	team.ContainerEnv = map[string]string{"LANG": "en_US.UTF-8", "TZ": "Europe/Prague"}
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	counts := map[string]int{}
	values := map[string]string{}
	for _, e := range f.realCreate(t).Config.Env {
		if eq := strings.IndexByte(e, '='); eq > 0 {
			counts[e[:eq]]++
			values[e[:eq]] = e[eq+1:]
		}
	}
	for k, want := range map[string]string{"LANG": "en_US.UTF-8", "TZ": "Europe/Prague"} {
		if counts[k] != 1 {
			t.Errorf("env has %d entries for %s, want exactly 1", counts[k], k)
		}
		if values[k] != want {
			t.Errorf("%s = %q, want %q (containerEnv must win over the default)", k, values[k], want)
		}
	}
}
