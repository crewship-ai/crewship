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
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
)

// crewCreate drives EnsureCrewRuntime against the covRT fake and returns the
// create request for the agent container.
func crewCreate(t *testing.T, mutate func(cfg *covRT)) *container.CreateRequest {
	t.Helper()
	return crewCreateFor(t, covTeam(), mutate)
}

// crewCreateFor is crewCreate with a caller-supplied CrewConfig, so the
// memory-derived sizing tests can drive several crew sizes through the real
// buildCrewContainerConfig rather than calling the helper directly.
func crewCreateFor(t *testing.T, team provider.CrewConfig, mutate func(cfg *covRT)) *container.CreateRequest {
	t.Helper()
	f := &covRT{}
	if mutate != nil {
		mutate(f)
	}
	p := f.provider(t, covRTConfig(t))
	if _, err := p.EnsureCrewRuntime(context.Background(), team); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	return f.realCreate(t)
}

// tmpfsSizeBytes extracts the size= option (in bytes) from a HostConfig.Tmpfs
// spec such as "rw,noexec,nosuid,size=357913942".
func tmpfsSizeBytes(t *testing.T, spec string) int64 {
	t.Helper()
	for _, opt := range strings.Split(spec, ",") {
		if v, ok := strings.CutPrefix(opt, "size="); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				t.Fatalf("tmpfs spec %q: size= is not a byte count: %v", spec, err)
			}
			return n
		}
	}
	t.Fatalf("tmpfs spec %q carries no size= option", spec)
	return 0
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
//
// The gids are pinned as LITERALS, not as crewGroupGID/sidecarGroupGID: the
// previous version of this test compared the request against the very
// constants that produced it, so `crewGroupGID = "0"` — root as a
// supplementary group on every crew exec — passed unchanged (#1636). 1001 is
// the uid/gid buildChownInitCmd chowns the bind mounts to and 1002 is the
// sidecar group the setgid .memory subtrees carry; changing either without
// changing those is a bug, so this test is meant to fail on it.
func TestCrewContainer_GroupAddCarriesCrewAndSidecarGIDs(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	want := []string{"1001", "1002"}
	if !slices.Equal(req.HostConfig.GroupAdd, want) {
		t.Errorf("GroupAdd = %v, want %v (crew gid then sidecar gid)", req.HostConfig.GroupAdd, want)
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
	// MemorySwappiness deliberately absent here. This test drives a provider
	// with no daemon-reported cgroup version, and swappiness is now sent only on
	// cgroup v1 — the one generation where the knob exists.
	//
	// It used to be asserted unconditionally, which was correct as far as unit
	// tests could see and wrong against every modern host: docker 28.0.4 on
	// cgroup v2 accepts the create and warns "Memory swappiness discarded",
	// while podman 6.0.2 accepts the create and then FAILS THE START with
	// `crun: cannot set memory swappiness with cgroupv2`, leaving a container
	// that exists, is configured, and can never run.
	//
	// Nothing is lost: swap is off because MemorySwap == Memory, asserted above,
	// and that is honoured on both cgroup generations. The conditional behaviour
	// has its own coverage in TestMemorySwappinessOnlyOnCgroupV1, including the
	// case that matters most — an UNKNOWN cgroup version must not be treated as
	// v1, because sending the field to a host we failed to identify kills every
	// crew on it.
	if res.MemorySwappiness != nil {
		t.Errorf("MemorySwappiness = %d on a provider with no known cgroup version; podman/crun refuses to start a container carrying it on cgroup v2", *res.MemorySwappiness)
	}
}

// The important one is core=0. The exec CWD is /output/<slug>, a
// host-persistent bind, and containerd's default LimitCORE is infinity — so a
// crashing agent writes a core dump containing every credential in its exec
// environment straight onto host disk, defeating the entire /secrets-as-tmpfs
// design (docker.go secretsTmpfsSpec).
//
// Every limit is pinned to its exact value. The first version of this test
// checked nofile/nproc/fsize with an aggregate "positive, soft<=hard, nofile
// hard below 1<<20" predicate, which survived every value-destroying mutation
// that matters (#1636): `fsize 4<<10` SIGXFSZs the container on any write past
// 4 KiB — npm install, git clone, any agent-authored file — and `nproc 1`
// means no agent run can fork at all. Both satisfied that predicate. A rlimit
// is a number, so the test asserts the number; the reasoning for each one is
// at crewUlimits in docker.go.
func TestCrewContainer_Ulimits(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	byName := map[string]*container.Ulimit{}
	for _, u := range req.HostConfig.Ulimits {
		byName[u.Name] = u
	}

	tests := []struct {
		name     string
		wantSoft int64
		wantHard int64
	}{
		{name: "core", wantSoft: 0, wantHard: 0},
		{name: "nofile", wantSoft: 8192, wantHard: 65536},
		{name: "nproc", wantSoft: 4096, wantHard: 4096},
		{name: "fsize", wantSoft: 4 << 30, wantHard: 4 << 30},
	}
	if len(req.HostConfig.Ulimits) != len(tests) {
		t.Errorf("Ulimits = %v, want exactly %d entries", req.HostConfig.Ulimits, len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, ok := byName[tt.name]
			if !ok {
				t.Fatalf("Ulimits = %v, missing %q", req.HostConfig.Ulimits, tt.name)
			}
			if u.Soft != tt.wantSoft || u.Hard != tt.wantHard {
				t.Errorf("%s = soft %d / hard %d, want %d/%d", tt.name, u.Soft, u.Hard, tt.wantSoft, tt.wantHard)
			}
		})
	}
}

// /dev/shm defaults to 64 MB, verified live on dev1. Chromium (and therefore
// Playwright, which this repo itself ships) crashes on that. But /dev/shm and
// /tmp are both unswappable shmem charged to the crew's memory cgroup, and
// swap is off (MemorySwap == Memory), so their CAPS have to be sized against
// the crew's memory limit or a crew that used to complete gets OOM-killed
// instead (#1636). crewTmpfsSizes owns the arithmetic; this pins the results
// per crew size.
func TestCrewContainer_TmpfsSizesScaleWithMemoryLimit(t *testing.T) {
	t.Parallel()

	const mib = int64(1024 * 1024)
	tests := []struct {
		name     string
		memoryMB int
		wantShm  int64
		wantTmp  int64
	}{
		// The product default (crews_create.go). Budget 1 GiB: 2/3 to
		// /dev/shm, the rest to /tmp — total exactly a quarter of the limit.
		{name: "default 4096 MB crew", memoryMB: 4096, wantShm: 715827882, wantTmp: 357913942},
		// Budget 2 GiB, so both ceilings bind: 1 GiB shm and 500 MiB /tmp,
		// i.e. the pre-#1636 values, reached only once the crew is big
		// enough to absorb them.
		{name: "8192 MB crew hits both ceilings", memoryMB: 8192, wantShm: 1024 * mib, wantTmp: 500 * mib},
		{name: "16384 MB crew stays at the ceilings", memoryMB: 16384, wantShm: 1024 * mib, wantTmp: 500 * mib},
		// Budget 256 MiB. A small crew gets a small /dev/shm rather than a
		// cap three times its own memory limit.
		{name: "1024 MB crew scales down", memoryMB: 1024, wantShm: 178956970, wantTmp: 89478486},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			team := covTeam()
			team.MemoryMB = tt.memoryMB
			req := crewCreateFor(t, team, nil)

			if req.HostConfig.ShmSize != tt.wantShm {
				t.Errorf("ShmSize = %d, want %d", req.HostConfig.ShmSize, tt.wantShm)
			}
			if got := tmpfsSizeBytes(t, req.HostConfig.Tmpfs["/tmp"]); got != tt.wantTmp {
				t.Errorf("/tmp size = %d, want %d", got, tt.wantTmp)
			}
			// The invariant the numbers exist to hold: with swap off, the two
			// caps together can never claim more than a quarter of the limit,
			// so three quarters stay available to the workload itself.
			limit := int64(tt.memoryMB) * mib
			if total := req.HostConfig.ShmSize + tmpfsSizeBytes(t, req.HostConfig.Tmpfs["/tmp"]); total > limit/4 {
				t.Errorf("shm+tmp = %d, exceeds a quarter of the %d-byte memory limit (%d)", total, limit, limit/4)
			}
			// …and the reason the ceilings are that high in the first place:
			// Docker's own 64 MB /dev/shm is what Chromium cannot work in.
			if req.HostConfig.ShmSize <= 64*mib {
				t.Errorf("ShmSize = %d, at or below Docker's 64 MB default — Chromium/Playwright aborts on that", req.HostConfig.ShmSize)
			}
		})
	}
}

// The /tmp tmpfs keeps its hardening flags whatever its size: noexec blocks
// execve()-ing a staged payload directly and nosuid strips setuid bits.
func TestCrewContainer_TmpTmpfsStaysNoexecNosuid(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	spec := req.HostConfig.Tmpfs["/tmp"]
	for _, want := range []string{"rw", "noexec", "nosuid"} {
		if !slices.Contains(strings.Split(spec, ","), want) {
			t.Errorf("/tmp tmpfs spec = %q, missing %q", spec, want)
		}
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
	if rp.MaximumRetryCount != 3 {
		t.Errorf("RestartPolicy.MaximumRetryCount = %d, want 3", rp.MaximumRetryCount)
	}
	if req.Config.StopTimeout == nil {
		t.Fatal("Config.StopTimeout = nil, want an explicit grace period")
	}
	// Pinned as a literal, not against crewStopTimeoutSeconds: comparing the
	// request to the constant that produced it made the assertion a tautology
	// that `crewStopTimeoutSeconds = 0` (SIGKILL with no grace period, killing
	// a run mid-flush) passed unchanged (#1636). 30 s is the window
	// StopCrewRuntime passes by hand; the two must not drift apart.
	if *req.Config.StopTimeout != 30 {
		t.Errorf("Config.StopTimeout = %d, want 30", *req.Config.StopTimeout)
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
//
// Dropping the field is NOT how you remove it. A nil Healthcheck — and an
// empty Test — means "inherit", so the daemon falls back to the image's own
// HEALTHCHECK directive (docker-image-spec, HealthcheckConfig.Test: "{} :
// inherit healthcheck", `{"NONE"} : disable healthcheck`). A BYOI crew on an
// app or devcontainer base that declares e.g. `HEALTHCHECK CMD curl -f
// localhost:3000/health` then probes forever against a container whose PID 1
// is `sleep infinity`: `docker ps` shows "Up 2 hours (unhealthy)" and the
// per-crew exec load the removal set out to shed comes straight back, on the
// IMAGE's interval (#1636). Only ["NONE"] actually turns it off.
func TestCrewContainer_HealthcheckExplicitlyDisabled(t *testing.T) {
	t.Parallel()

	req := crewCreate(t, nil)
	hc := req.Config.Healthcheck
	if hc == nil {
		t.Fatal("Config.Healthcheck = nil — nil INHERITS the image's HEALTHCHECK; want an explicit Test [NONE]")
	}
	if !slices.Equal(hc.Test, []string{"NONE"}) {
		t.Errorf("Healthcheck.Test = %v, want exactly [NONE] — anything else (including an empty slice) inherits the image directive", hc.Test)
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
