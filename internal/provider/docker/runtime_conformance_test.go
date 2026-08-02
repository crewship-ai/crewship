//go:build conformance

// Package docker's runtime-conformance harness.
//
// # What this is for
//
// Crewship advertises five container runtimes (README, PRIVACY.md,
// `crewship doctor`, and the eight sockets Detect() probes) and has only ever
// been run against one of them. The detected runtime string reaches a log line,
// the /system/runtime payload and doctor — and nothing else. No code adapts to
// it. Every crew container is created with Docker API semantics and the hope
// that whatever is on the other end of the socket means the same thing (#1672).
//
// The dangerous failure here is not rejection. A runtime that *rejects*
// PidsLimit says so at create time and someone notices. A runtime that accepts
// the field and quietly does not apply it looks identical to success while the
// zombie leak #1666 fixed comes straight back, a crashing agent writes its
// credential environment to a host-persistent core dump, and the read-only /etc
// that makes the image safe is not read-only. Every one of those fails open and
// silent.
//
// So this harness does not assert that we *sent* a field. It starts a real crew
// container and reads back what the kernel actually did, from inside:
// /proc/self/status, /proc/mounts, the cgroup files, id(1), ulimit(1),
// getent(1). Values, never shapes — HANDOFF-2026-08-02.md §2.
//
// # Why it drives the product's own builder
//
// The HostConfig under test comes from buildCrewContainerConfig, the same call
// EnsureCrewRuntime makes. It is deliberately not a hand-copied struct: a copy
// would keep passing after someone changes the real one, which is exactly the
// class of hole §2 of the handoff was written about.
//
// # Running it
//
//	go test -tags conformance -run TestRuntimeConformance -v ./internal/provider/docker/
//
// Against a specific runtime, point DOCKER_HOST at it — Detect() honours that
// first:
//
//	DOCKER_HOST=unix:///run/user/1000/podman/podman.sock go test -tags conformance ...
//
// Knobs: CREWSHIP_CONFORMANCE_IMAGE (default debian:bookworm-slim; needs bash,
// which scripts/entrypoint.sh has a shebang for) and CREWSHIP_CONFORMANCE_KEEP=1
// to leave the container up for a post-mortem.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// conformanceMemoryMB and conformanceCPUs are the crew shape under test. The
// memory figure is not arbitrary: ShmSize and the /tmp tmpfs size are both
// derived from it by crewTmpfsSizes, so a probe that reads /dev/shm back is
// only meaningful against a number the arithmetic actually ran on.
const (
	conformanceMemoryMB = 1024
	conformanceCPUs     = 1.5
)

// probe is one invariant: what the HostConfig asked the runtime for, and what
// the container turned out to have. `honoured` is the whole point — a runtime
// that ignores a field lands here as false with both values printed, rather
// than as a create error nobody sees.
type probe struct {
	name     string
	want     string
	got      string
	honoured bool
	// loadBearing marks an invariant whose silent absence is a security or
	// stability regression rather than a cosmetic difference. Only these fail
	// the test; the rest are reported so the matrix stays honest about what a
	// runtime does differently without blocking on it.
	loadBearing bool
	// why is printed on failure — the consequence of the invariant not
	// holding, so a red run explains itself without a git archaeology trip.
	why string
}

func TestRuntimeConformance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	image := os.Getenv("CREWSHIP_CONFORMANCE_IMAGE")
	if image == "" {
		image = "debian:bookworm-slim"
	}

	p, cleanupProvider := newConformanceProvider(ctx, t)
	defer cleanupProvider()

	t.Logf("runtime under test: %s %s (%s)", p.detected.Runtime, p.detected.Version, p.detected.Socket)

	if err := p.ensureImage(ctx, image); err != nil {
		t.Fatalf("pull %s: %v", image, err)
	}

	crew := provider.CrewConfig{
		ID:       "conformance-crew-id",
		Slug:     "conformance",
		MemoryMB: conformanceMemoryMB,
		CPUs:     conformanceCPUs,
	}
	dirs, err := p.prepareCrewDirs(crew)
	if err != nil {
		t.Fatalf("prepare crew dirs: %v", err)
	}

	// Mirrors EnsureCrewRuntime, which chowns the binds through a helper
	// container before create (docker_container.go). Without it the bind-write
	// probes below would report a false negative on every runtime — the host
	// dirs are owned by whoever runs the test, not by uid 1001.
	p.fixBindMountOwnership(ctx, image, dirs)

	name := p.CrewContainerName(crew.ID, crew.Slug)
	// A previous aborted run leaves the container behind; the create would then
	// fail on the name rather than on anything this test is about.
	_, _ = p.client.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})

	cfg, hostCfg, err := p.buildCrewContainerConfig(ctx, crew, name, image, "", conformanceMemoryMB, conformanceCPUs, dirs)
	if err != nil {
		t.Fatalf("build crew container config: %v", err)
	}

	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: &dockernetwork.NetworkingConfig{},
		Name:             name,
	})
	if err != nil {
		// A create rejection is a conformance result too, and a much better one
		// than a silent drop — say which runtime refused what.
		t.Fatalf("%s rejected the crew HostConfig at create time: %v", p.detected.Runtime, err)
	}
	for _, w := range created.Warnings {
		// Podman and containerd report unapplied fields here, where Docker
		// mostly does not. Never swallow these: a warning is the runtime
		// telling us it accepted a field it will not honour.
		t.Logf("CREATE WARNING from %s: %s", p.detected.Runtime, w)
	}
	if os.Getenv("CREWSHIP_CONFORMANCE_KEEP") == "" {
		defer func() {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer rmCancel()
			_, _ = p.client.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			for _, v := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
				_, _ = p.client.VolumeRemove(rmCtx, v, client.VolumeRemoveOptions{Force: true})
			}
		}()
	}

	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("%s accepted the crew HostConfig but could not start it: %v", p.detected.Runtime, err)
	}
	waitForPID1(ctx, t, p, created.ID)

	facts := readContainerFacts(ctx, t, p, created.ID)
	probes := evaluate(t, p, hostCfg, facts)
	report(t, p, probes)
}

// newConformanceProvider builds a real Provider against whatever runtime is
// reachable. The sidecar bind mount is mandatory in buildMounts, so the harness
// supplies a four-byte ELF stub: verifySidecarIsLinuxELF reads exactly the
// magic, the mount is read-only, and nothing in this harness execs it (that is
// runByoiSidecarCheck's job, on the EnsureCrewRuntime path this test
// deliberately does not take). A real sidecar build would make the harness
// depend on `make build:sidecar` for no added signal about the runtime.
func newConformanceProvider(ctx context.Context, t *testing.T) (*Provider, func()) {
	t.Helper()

	base := t.TempDir()
	sidecar := filepath.Join(base, "crewship-sidecar")
	if err := os.WriteFile(sidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatalf("write sidecar stub: %v", err)
	}

	entrypoint := repoFile(t, "scripts", "entrypoint.sh")

	cfg := Config{
		OutputBasePath:    filepath.Join(base, "output"),
		ContainerPrefix:   "crewship-conf",
		SidecarBinaryPath: sidecar,
		EntrypointPath:    entrypoint,
	}
	p, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Skipf("no container runtime reachable: %v", err)
	}
	return p, func() { _ = p.client.Close() }
}

// repoFile resolves a path relative to the repository root from this test's own
// source location, so the harness works from any working directory.
func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, self, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	root := filepath.Join(filepath.Dir(self), "..", "..", "..")
	path := filepath.Join(append([]string{root}, parts...)...)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected repo file %s: %v", path, err)
	}
	return path
}

// waitForPID1 blocks until the entrypoint has reached `exec sleep infinity` and
// the container is accepting execs. A container that dies here is itself the
// finding, so the failure names the runtime and dumps the logs.
func waitForPID1(ctx context.Context, t *testing.T, p *Provider, id string) {
	t.Helper()
	// Both the state and the exec error are kept, because they distinguish the
	// two ways this fails and they need opposite fixes: a container that exited
	// is the entrypoint or the HostConfig, while a running container that will
	// not exec is the exec path itself (user resolution, the #1158 guard, or the
	// runtime's exec semantics). Reporting only "never became exec-able" sends
	// the reader to the wrong half.
	var lastState, lastExecErr string
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		inspect, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		switch {
		case err != nil:
			lastState = "inspect failed: " + err.Error()
		case inspect.Container.State == nil:
			lastState = "no state reported"
		default:
			s := inspect.Container.State
			lastState = fmt.Sprintf("status=%s running=%t restarting=%t exit=%d oom=%t err=%q",
				s.Status, s.Running, s.Restarting, s.ExitCode, s.OOMKilled, s.Error)
			if s.Running {
				// Running is not the same as ready: the entrypoint still has to
				// get past its home-directory skeleton before an exec means
				// anything.
				out, execErr := execInContainer(ctx, p, id, "test -d /proc/1 && echo ready")
				if execErr == nil && strings.Contains(out, "ready") {
					return
				}
				if execErr != nil {
					lastExecErr = execErr.Error()
				} else {
					lastExecErr = fmt.Sprintf("exec succeeded but returned %q", out)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	logs, logErr := p.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	switch {
	case logErr != nil:
		t.Logf("container logs unavailable: %v", logErr)
	default:
		body, _ := io.ReadAll(logs)
		logs.Close()
		if len(bytes.TrimSpace(body)) == 0 {
			t.Log("container produced no log output")
		} else {
			t.Logf("container logs:\n%s", body)
		}
	}
	t.Fatalf("crew container never became exec-able on %s\n  last state: %s\n  last exec error: %s",
		p.detected.Runtime, lastState, emptyAs(lastExecErr, "(never attempted — container was not running)"))
}

// probeScript reads back everything the HostConfig asked for, from inside the
// container, as KEY=VALUE lines. Deliberately one exec: each round trip costs
// real time on a VM-backed runtime, and a single shell keeps the observations
// close together in time.
//
// The zombie count is the functional half of the Init check. `Init: true` is
// only worth anything if PID 1 actually reaps — the harness orphans a process,
// waits for the reparent, and counts processes left in state Z. Asserting that
// PID 1 is named docker-init would pass on a runtime that runs an init which
// does not reap.
const probeScript = `
set +e
echo "PID1_COMM=$(cat /proc/1/comm 2>/dev/null)"
echo "WHOAMI=$(id -u):$(id -g)"
echo "GROUPS=$(id -G | tr ' ' ',')"
echo "NNP=$(awk '/^NoNewPrivs:/{print $2}' /proc/self/status)"
echo "ULIMIT_CORE=$(ulimit -c)"
echo "ULIMIT_NOFILE=$(ulimit -n)"
echo "ULIMIT_NPROC=$(ulimit -u)"
echo "ULIMIT_FSIZE=$(ulimit -f)"
echo "SHM_BYTES=$(df -B1 /dev/shm 2>/dev/null | awk 'NR==2{print $2}')"
echo "TMP_OPTS=$(awk '$2=="/tmp"{print $4}' /proc/mounts)"
echo "TMP_BYTES=$(df -B1 /tmp 2>/dev/null | awk 'NR==2{print $2}')"
echo "SECRETS_OPTS=$(awk '$2=="/secrets"{print $4}' /proc/mounts)"
echo "SECRETS_STAT=$(stat -c '%a %u %g' /secrets 2>/dev/null)"
echo "ROOT_OPTS=$(awk '$2=="/"{print $4}' /proc/mounts)"
echo "ETC_WRITABLE=$(touch /etc/.crewship-conformance 2>/dev/null && echo yes || echo no)"
echo "MEM_MAX=$(cat /sys/fs/cgroup/memory.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null)"
echo "SWAP_MAX=$(cat /sys/fs/cgroup/memory.swap.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.memsw.limit_in_bytes 2>/dev/null)"
echo "PIDS_MAX=$(cat /sys/fs/cgroup/pids.max 2>/dev/null || cat /sys/fs/cgroup/pids/pids.max 2>/dev/null)"
echo "CPU_MAX=$(cat /sys/fs/cgroup/cpu.max 2>/dev/null || echo "$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null) $(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us 2>/dev/null)")"
echo "HOST_GATEWAY=$(getent hosts host.docker.internal 2>/dev/null | head -1 | awk '{print $1}')"
echo "HOST_CONTAINERS_INTERNAL=$(getent hosts host.containers.internal 2>/dev/null | head -1 | awk '{print $1}')"
for d in /crew /workspace /output /home/agent /opt/crew-tools; do
  k=$(echo "$d" | tr '/-' '__' | tr '[:lower:]' '[:upper:]')
  echo "WRITE$k=$(touch "$d/.crewship-conformance" 2>/dev/null && echo yes || echo no)"
done
# Orphan a process that is still ALIVE when its parent exits, so the kernel
# reparents it onto PID 1 and PID 1 is the process that must reap it. A child
# that exits first would be a zombie under its own parent and prove nothing
# about init.
sh -c 'sh -c "sleep 1" & exit 0' 2>/dev/null
sleep 3
z=0
for pdir in /proc/[0-9]*; do
  s=$(awk '/^State:/{print $2}' "$pdir/status" 2>/dev/null)
  [ "$s" = "Z" ] && z=$((z+1))
done
echo "ZOMBIES=$z"
`

// containerFacts is the parsed KEY=VALUE output of probeScript.
type containerFacts map[string]string

func readContainerFacts(ctx context.Context, t *testing.T, p *Provider, id string) containerFacts {
	t.Helper()
	out, err := execInContainer(ctx, p, id, probeScript)
	if err != nil {
		t.Fatalf("probe exec failed on %s: %v", p.detected.Runtime, err)
	}
	facts := containerFacts{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if eq := strings.IndexByte(line, '='); eq > 0 {
			facts[line[:eq]] = strings.TrimSpace(line[eq+1:])
		}
	}
	if len(facts) == 0 {
		t.Fatalf("probe produced no parseable output on %s; raw: %q", p.detected.Runtime, out)
	}
	return facts
}

// execInContainer runs a shell snippet through the provider's own Exec — with
// an empty User, so the run also exercises ContainerUser resolution and the
// #1158 fail-closed guard against whatever this runtime reports for the
// container's configured user.
func execInContainer(ctx context.Context, p *Provider, id, script string) (string, error) {
	res, err := p.Exec(ctx, provider.ExecConfig{
		ContainerID: id,
		Cmd:         []string{"sh", "-c", script},
	})
	if err != nil {
		return "", err
	}
	defer res.Reader.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, res.Reader); err != nil {
		return "", fmt.Errorf("demux exec stream: %w (stderr so far: %s)", err, stderr.String())
	}
	return stdout.String(), nil
}

// evaluate turns the observed facts into the probe matrix, comparing each
// against what hostCfg actually asked this runtime for.
func evaluate(t *testing.T, p *Provider, hostCfg *container.HostConfig, f containerFacts) []probe {
	t.Helper()
	var probes []probe
	add := func(pr probe) { probes = append(probes, pr) }

	// --- identity ------------------------------------------------------
	add(probe{
		name: "exec runs as the configured uid:gid", want: "1001:1001", got: f["WHOAMI"],
		honoured: f["WHOAMI"] == "1001:1001", loadBearing: true,
		why: "the whole containment model assumes agent commands are unprivileged",
	})
	// GroupAdd carries bare numeric GIDs with no /etc/group entry. moby accepts
	// that (agent-isolation-findings §1.2, verified live); a runtime that
	// resolves group arguments against the container's group file instead would
	// either reject the create or drop them here.
	wantGroups := hostCfg.GroupAdd
	gotGroups := strings.Split(f["GROUPS"], ",")
	add(probe{
		name: "supplementary groups from GroupAdd are present", want: strings.Join(wantGroups, ","), got: f["GROUPS"],
		honoured: containsAll(gotGroups, wantGroups), loadBearing: true,
		why: "the sidecar-owned .memory subtrees are group-readable only; without gid 1002 the agent cannot read its own memory",
	})

	// --- process hardening ---------------------------------------------
	add(probe{
		name: "no-new-privileges is set", want: "1", got: f["NNP"],
		honoured: f["NNP"] == "1", loadBearing: true,
		why: "a setuid binary in a user-supplied base image would otherwise be an escalation path",
	})
	// Every rlimit is checked against the soft limit the product actually asked
	// for, read back through ulimit(1) inside an exec. Two of these are only
	// meaningful as exact values: `core` because any non-zero core dump lands on
	// a host-persistent bind with the agent's whole credential environment in
	// it, and `fsize` because the ulimits test that asserted merely
	// "positive && soft<=hard" let a 4 KiB fsize through — the third of the
	// three shape-not-value holes that made mutation testing the standard here.
	//
	// ulimit reports `core` and `fsize` in 512-byte blocks and `nofile`/`nproc`
	// as plain counts, so the expectation is converted rather than compared raw.
	for _, u := range hostCfg.Resources.Ulimits {
		key, blocks := "ULIMIT_"+strings.ToUpper(u.Name), false
		switch u.Name {
		case "core", "fsize":
			blocks = true
		}
		want := u.Soft
		if blocks {
			want /= 512
		}
		add(probe{
			name: "rlimit " + u.Name + " is applied", want: strconv.FormatInt(want, 10), got: f[key],
			honoured:    f[key] == strconv.FormatInt(want, 10),
			loadBearing: u.Name == "core",
			why:         "rootless runtimes cannot raise an rlimit above the invoking user's hard limit, so these can silently land lower than asked",
		})
	}

	// --- filesystem ----------------------------------------------------
	add(probe{
		name: "root filesystem is read-only", want: "no", got: f["ETC_WRITABLE"],
		honoured: f["ETC_WRITABLE"] == "no", loadBearing: true,
		why: "read-only /etc is what makes a user-supplied base image safe to hand an agent",
	})
	add(probe{
		name: "/secrets is a private tmpfs owned by the agent", want: "700 1001 1001", got: f["SECRETS_STAT"],
		honoured: f["SECRETS_STAT"] == "700 1001 1001", loadBearing: true,
		why: "credential files land here; a wrong mode or owner exposes them to every sibling process",
	})
	add(probe{
		name: "/secrets carries noexec,nosuid", want: "noexec,nosuid", got: f["SECRETS_OPTS"],
		honoured: hasMountOpts(f["SECRETS_OPTS"], "noexec", "nosuid"), loadBearing: true,
		why: "a credential file staged here must not be directly executable",
	})
	add(probe{
		name: "/tmp carries noexec,nosuid", want: "noexec,nosuid", got: f["TMP_OPTS"],
		honoured: hasMountOpts(f["TMP_OPTS"], "noexec", "nosuid"),
		why:      "defense in depth against staging and execve-ing a payload",
	})
	add(probe{
		name: "/tmp tmpfs is sized from the memory budget", want: humanBytes(tmpfsSizeFromSpec(hostCfg.Tmpfs["/tmp"])), got: humanBytes(parseInt64(f["TMP_BYTES"])),
		honoured: parseInt64(f["TMP_BYTES"]) == tmpfsSizeFromSpec(hostCfg.Tmpfs["/tmp"]),
		why:      "/tmp is unswappable shmem charged to the crew's memory cgroup; an unbounded one OOM-kills the crew",
	})
	add(probe{
		name: "/dev/shm honours ShmSize", want: humanBytes(hostCfg.ShmSize), got: humanBytes(parseInt64(f["SHM_BYTES"])),
		honoured: parseInt64(f["SHM_BYTES"]) == hostCfg.ShmSize,
		why:      "the default 64 MiB breaks toolchains that size their shared memory from the container's memory limit",
	})
	for _, m := range []struct{ key, path string }{
		{"WRITE_CREW", "/crew"},
		{"WRITE_WORKSPACE", "/workspace"},
		{"WRITE_OUTPUT", "/output"},
		{"WRITE_HOME_AGENT", "/home/agent"},
		{"WRITE_OPT_CREW_TOOLS", "/opt/crew-tools"},
	} {
		add(probe{
			name: "agent can write " + m.path, want: "yes", got: f[m.key],
			honoured: f[m.key] == "yes", loadBearing: true,
			why: "a rootless runtime maps the agent uid into a user namespace, which can make a host bind unwritable for it",
		})
	}

	// --- resource limits ------------------------------------------------
	add(probe{
		name: "memory limit is applied", want: humanBytes(hostCfg.Resources.Memory), got: humanBytes(parseInt64(f["MEM_MAX"])),
		honoured: parseInt64(f["MEM_MAX"]) == hostCfg.Resources.Memory, loadBearing: true,
		why: "without it one crew's runaway process takes the host and every co-resident crew with it",
	})
	add(probe{
		name: "swap is off (MemorySwap == Memory)", want: "0", got: f["SWAP_MAX"],
		honoured: swapDisabled(f["SWAP_MAX"], hostCfg.Resources.Memory),
		why:      "swapping turns a bounded OOM kill into minutes of thrash across every crew on the host",
	})
	add(probe{
		name: "PidsLimit is applied", want: strconv.FormatInt(derefInt64(hostCfg.Resources.PidsLimit), 10), got: f["PIDS_MAX"],
		honoured: f["PIDS_MAX"] == strconv.FormatInt(derefInt64(hostCfg.Resources.PidsLimit), 10), loadBearing: true,
		why: "this is the cap the zombie leak in #1666 was exhausting; unapplied, the leak is back with no signal",
	})
	add(probe{
		name: "CPU quota is applied", want: fmt.Sprintf("%.2f cpus", float64(hostCfg.Resources.NanoCPUs)/1e9), got: f["CPU_MAX"],
		honoured: cpuQuotaMatches(f["CPU_MAX"], hostCfg.Resources.NanoCPUs),
		why:      "an unlimited crew starves its neighbours long before it OOMs",
	})

	// --- init and reaping ------------------------------------------------
	add(probe{
		name: "PID 1 is an init, not the entrypoint's sleep", want: "an init process", got: f["PID1_COMM"],
		honoured: f["PID1_COMM"] != "" && f["PID1_COMM"] != "sleep" && f["PID1_COMM"] != "bash" && f["PID1_COMM"] != "sh",
		why:      "the entrypoint ends in `exec sleep infinity`, which never calls wait()",
	})
	add(probe{
		name: "PID 1 reaps orphans", want: "0 zombies", got: f["ZOMBIES"] + " zombies",
		honoured: f["ZOMBIES"] == "0", loadBearing: true,
		why: "every unreaped orphan is a permanent process-table entry counting against PidsLimit — a monotonic leak ending in `fork: Resource temporarily unavailable`",
	})

	// --- host reachability -------------------------------------------------
	// Not load-bearing as spelled: what has to hold is that SOME name resolves
	// to the host, because that is how the sidecar reaches crewshipd. Docker
	// spells it host.docker.internal; Podman spells it host.containers.internal
	// and its support for the `host-gateway` magic value is version-dependent.
	// Recording both is how the fix gets scoped instead of guessed.
	add(probe{
		name: "host.docker.internal resolves", want: "an address", got: emptyAs(f["HOST_GATEWAY"], "(unresolved)"),
		honoured: f["HOST_GATEWAY"] != "",
		why:      "the sidecar reaches crewshipd through this name; without it every assignment IPC call fails",
	})
	add(probe{
		name: "some host alias resolves", want: "an address", got: emptyAs(firstNonEmpty(f["HOST_GATEWAY"], f["HOST_CONTAINERS_INTERNAL"]), "(neither resolved)"),
		honoured: firstNonEmpty(f["HOST_GATEWAY"], f["HOST_CONTAINERS_INTERNAL"]) != "", loadBearing: true,
		why: "with no route to the host the crew cannot run at all",
	})

	return probes
}

// report prints the matrix and fails on load-bearing misses. The matrix is
// printed in full either way: a runtime's differences are the deliverable here,
// not a side note on a failure.
func report(t *testing.T, p *Provider, probes []probe) {
	t.Helper()
	var failed []probe
	t.Logf("── runtime conformance: %s %s ──", p.detected.Runtime, p.detected.Version)
	for _, pr := range probes {
		mark := "ok  "
		if !pr.honoured {
			mark = "MISS"
		}
		t.Logf("  [%s] %-48s want=%-24s got=%s", mark, pr.name, pr.want, pr.got)
		if !pr.honoured && pr.loadBearing {
			failed = append(failed, pr)
		}
	}
	if len(failed) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s does not honour %d load-bearing invariant(s) of the crew container contract:\n", p.detected.Runtime, len(failed))
	for _, pr := range failed {
		fmt.Fprintf(&b, "  · %s\n      want: %s\n      got:  %s\n      why:  %s\n", pr.name, pr.want, pr.got, pr.why)
	}
	t.Fatal(b.String())
}

// --- small helpers ------------------------------------------------------

func containsAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if strings.TrimSpace(h) == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasMountOpts(opts string, want ...string) bool {
	set := map[string]bool{}
	for _, o := range strings.Split(opts, ",") {
		set[strings.TrimSpace(o)] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// tmpfsSizeFromSpec pulls the size= bytes out of a HostConfig.Tmpfs spec so the
// expectation comes from the product's own string rather than a constant
// repeated here.
func tmpfsSizeFromSpec(spec string) int64 {
	for _, part := range strings.Split(spec, ",") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(part), "size="); ok {
			return parseInt64(rest)
		}
	}
	return -1
}

// swapDisabled reads both cgroup generations. v2's memory.swap.max is the swap
// allowance alone (0 = none, "max" = unlimited); v1's memsw limit is memory +
// swap combined, so "no swap" there means equal to the memory limit.
func swapDisabled(v string, memoryLimit int64) bool {
	v = strings.TrimSpace(v)
	switch v {
	case "0":
		return true
	case "max", "":
		return false
	}
	return parseInt64(v) == memoryLimit
}

// cpuQuotaMatches accepts both cgroup generations' spelling of a CPU cap:
// v2's "<quota> <period>" in cpu.max, v1's quota/period pair.
func cpuQuotaMatches(v string, nanoCPUs int64) bool {
	fields := strings.Fields(strings.TrimSpace(v))
	if len(fields) != 2 || fields[0] == "max" {
		return false
	}
	quota, period := parseInt64(fields[0]), parseInt64(fields[1])
	if quota <= 0 || period <= 0 {
		return false
	}
	// NanoCPUs is cpus × 1e9; quota/period is the same ratio.
	return quota*1_000_000_000 == nanoCPUs*period
}

func humanBytes(n int64) string {
	if n < 0 {
		return "(unreadable)"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func emptyAs(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
