//go:build conformance

// Apple Containers runtime-conformance harness — the answer to #1650.
//
// This provider has been compiled, unit-tested against a fake CLI, and never
// once run against Apple's actual runtime. Its create path is a rendered
// argument vector, and every claim about what those arguments do was read out
// of Apple's Swift sources rather than observed: that `--ulimit` at create time
// binds every later `container exec` (ContainerExec.swift builds from
// `container.configuration.initProcess`), that `--mount` accepts no uid/gid
// directive (Parser.swift), that `--entrypoint` suppresses the image CMD. Each
// of those is a reasonable reading and none of them is evidence.
//
// So this does what the docker harness does: start a real crew container
// through the provider's own EnsureCrewRuntime, then read back from inside it
// what the guest kernel actually did. Values, not shapes.
//
// Only invariants this provider genuinely claims are asserted. The provider is
// explicit that several Docker controls have no Apple equivalent — no
// PidsLimit, no supplementary groups, no no-new-privileges, /secrets at 1777
// rather than 0700-owned-by-1001 — and those are recorded here as the
// documented differences they are, not as failures.
//
// Running it:
//
//	container system start
//	go test -tags conformance -run TestAppleRuntimeConformance -v ./internal/provider/apple/
//
// The _darwin_ in the filename is load-bearing rather than decorative: Apple
// Containers exist on no other platform, so the constraint is a compile-time
// decision instead of a runtime t.Skip that reads as a pass in CI output —
// which is the whole argument scripts/skip-budget.sh makes.
//
// The remaining environment checks FAIL rather than skip. This file is behind a
// build tag; reaching it means somebody asked for a conformance run, and
// answering "ok" because there was no runtime to test against is the exact
// failure mode a skip budget exists to prevent.
package apple

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const (
	appleConformanceMemoryMB = 1024
	appleConformanceCPUs     = 2
)

type appleProbe struct {
	name        string
	want        string
	got         string
	honoured    bool
	loadBearing bool
	why         string
}

func TestAppleRuntimeConformance(t *testing.T) {
	if _, err := exec.LookPath("container"); err != nil {
		t.Fatalf("the `container` CLI is not on PATH: %v — install it, or do not build with -tags conformance", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	image := os.Getenv("CREWSHIP_CONFORMANCE_IMAGE")
	if image == "" {
		// Fully qualified: Apple's CLI does not assume Docker Hub the way the
		// docker CLI does.
		image = "docker.io/library/debian:bookworm-slim"
	}

	base := t.TempDir()

	// Where the crew's mandatory binds come FROM. Default: a sibling of the
	// data dir, which is what this harness always did. That default is exactly
	// why it could not reproduce #1724 as written — both paths sat in the same
	// t.TempDir(), so the install prefix was never the awkward one.
	//
	// CREWSHIP_CONFORMANCE_INSTALL_PREFIX pins it instead, so the reported
	// scenario runs verbatim rather than approximated — the docker harness's
	// knob of the same name (runtime_binds_conformance_test.go), for the same
	// reason:
	//
	//	CREWSHIP_CONFORMANCE_INSTALL_PREFIX=/opt/homebrew/libexec \
	//	  go test -tags conformance -run TestAppleRuntimeConformance -v ./internal/provider/apple/
	//
	// A prefix outside $HOME is the interesting one: that is where Homebrew and
	// install.sh actually put these files, and whether an Apple container VM can
	// bind out of it is the open question #1724 exists to answer.
	//
	// Always a subdirectory of whatever is configured, never the directory
	// itself: pointed at a real /opt/homebrew/libexec, writing crewship-sidecar
	// straight into it would overwrite the operator's actual install.
	installBase := os.Getenv("CREWSHIP_CONFORMANCE_INSTALL_PREFIX")
	if installBase == "" {
		installBase = base
	}
	installPrefix := filepath.Join(installBase, "crewship-1724-install")
	if err := os.MkdirAll(installPrefix, 0o755); err != nil {
		t.Fatalf("create install prefix %s: %v", installPrefix, err)
	}
	defer os.RemoveAll(installPrefix)
	t.Logf("install prefix: %s", installPrefix)

	sidecar := filepath.Join(installPrefix, "crewship-sidecar")
	// verifySidecarIsLinuxELF reads exactly the magic; nothing here execs the
	// binary (that is runByoiSidecarCheck's job on a path this harness does not
	// take), so a stub keeps the harness independent of `make build:sidecar`.
	if err := os.WriteFile(sidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatalf("write sidecar stub: %v", err)
	}
	// The entrypoint has to live beside it, or the install prefix is only half
	// exercised: it is the second mandatory bind and the one the container
	// actually executes.
	entrypoint := filepath.Join(installPrefix, "entrypoint.sh")
	entrypointSrc, err := os.ReadFile(repoFile(t, "scripts", "entrypoint.sh"))
	if err != nil {
		t.Fatalf("read scripts/entrypoint.sh: %v", err)
	}
	if err := os.WriteFile(entrypoint, entrypointSrc, 0o755); err != nil {
		t.Fatalf("write entrypoint to the install prefix: %v", err)
	}

	cfg := Config{
		RuntimeImage:      image,
		OutputBasePath:    filepath.Join(base, "output"),
		ContainerPrefix:   "crewship-conf",
		SidecarBinaryPath: sidecar,
		EntrypointPath:    entrypoint,
	}
	p, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("apple container runtime unavailable: %v — run `container system start` first", err)
	}
	defer p.Close()

	// New() stages both artifacts under the data dir (#1724). Assert it here
	// rather than trusting it: if the crew below comes up, this is what says
	// whether it came up because the bind sources moved or in spite of it.
	if p.cfg.SidecarBinaryPath == sidecar || p.cfg.EntrypointPath == entrypoint {
		t.Fatalf("New() left a mandatory bind at its install path (sidecar=%s entrypoint=%s); the crew below would not be testing #1724 at all",
			p.cfg.SidecarBinaryPath, p.cfg.EntrypointPath)
	}
	t.Logf("bind sources after staging: %s, %s", p.cfg.SidecarBinaryPath, p.cfg.EntrypointPath)

	crew := provider.CrewConfig{
		ID:       "conformanceapple",
		Slug:     "conformance",
		MemoryMB: appleConformanceMemoryMB,
		CPUs:     appleConformanceCPUs,
	}

	name := p.CrewContainerName(crew.ID, crew.Slug)
	// A previous aborted run leaves the container behind and the create would
	// then fail on the name rather than on anything under test.
	_, _ = runCLI(ctx, "rm", "-f", name)

	containerID, err := p.EnsureCrewRuntime(ctx, crew)
	if err != nil {
		t.Fatalf("apple container runtime refused the crew: %v", err)
	}
	if os.Getenv("CREWSHIP_CONFORMANCE_KEEP") == "" {
		defer func() {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer rmCancel()
			_, _ = runCLI(rmCtx, "rm", "-f", containerID)
		}()
	}
	t.Logf("crew container up: %s", containerID)

	facts := readAppleFacts(ctx, t, p, containerID)
	probes := evaluateApple(t, facts)
	reportApple(t, probes)
}

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

// appleProbeScript mirrors the docker harness's probe, minus the controls this
// provider does not claim. The reaping check is functional on purpose: it
// orphans a process that is still alive when its parent exits, so PID 1 is the
// process that must reap it. Reading PID 1's name would pass on an init that
// does not reap.
const appleProbeScript = `
set +e
echo "PID1_COMM=$(cat /proc/1/comm 2>/dev/null)"
echo "WHOAMI=$(id -u):$(id -g)"
# /proc/self/limits rather than ulimit(1): dash prints nothing at all for
# 'ulimit -u', which reads as "the runtime dropped nproc" when the limit is
# in fact applied, and it reports core/fsize in 512-byte blocks. The kernel
# file is in bytes and is what the shell is paraphrasing.
awk -F'  +' '/^Max core file size/{print "RLIMIT_CORE=" $2}
             /^Max open files/{print "RLIMIT_NOFILE=" $2}
             /^Max processes/{print "RLIMIT_NPROC=" $2}
             /^Max file size/{print "RLIMIT_FSIZE=" $2}' /proc/self/limits
echo "ROOT_OPTS=$(awk '$2=="/"{print $4}' /proc/mounts)"
echo "ETC_WRITABLE=$(touch /etc/.crewship-conformance 2>/dev/null && echo yes || echo no)"
# Apple's guest writes /proc/mounts lines with an EMPTY device field:
#   " /secrets tmpfs rw,relatime,size=16384k 0 0"
# so the mount point is $1 and the type is $2, where Docker and Podman put
# them in $2 and $3. Matching on the mount point in either position is what
# makes one probe script work on all three, and getting this wrong reported
# a correctly-mounted tmpfs as absent.
echo "SECRETS_TYPE=$(awk '$1=="/secrets"{print $2; exit} $2=="/secrets"{print $3; exit}' /proc/mounts)"
echo "SECRETS_OPTS=$(awk '$1=="/secrets"{print $3; exit} $2=="/secrets"{print $4; exit}' /proc/mounts)"
echo "SECRETS_STAT=$(stat -c '%a %u %g' /secrets 2>/dev/null)"
# df's Filesystem column is empty for the same reason, which shifts every
# other column left by one. Read the size out of the mount options instead —
# it is the value the kernel actually applied.
echo "TMP_TYPE=$(awk '$1=="/tmp"{print $2; exit} $2=="/tmp"{print $3; exit}' /proc/mounts)"
echo "HOME_TYPE=$(awk '$1=="/home/agent"{print $2; exit} $2=="/home/agent"{print $3; exit}' /proc/mounts)"
echo "MEM_TOTAL=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
echo "NPROC_ONLN=$(getconf _NPROCESSORS_ONLN 2>/dev/null)"
for d in /crew /workspace /output /home/agent /secrets /tmp; do
  k=$(echo "$d" | tr '/-' '__' | tr '[:lower:]' '[:upper:]')
  echo "WRITE$k=$(touch "$d/.crewship-conformance" 2>/dev/null && echo yes || echo no)"
done
echo "SIDECAR_PRESENT=$(test -f /usr/local/bin/crewship-sidecar && echo yes || echo no)"
echo "ENTRYPOINT_PRESENT=$(test -f /usr/local/bin/entrypoint.sh && echo yes || echo no)"
sh -c 'sh -c "sleep 1" & exit 0' 2>/dev/null
sleep 3
z=0
for pdir in /proc/[0-9]*; do
  s=$(awk '/^State:/{print $2}' "$pdir/status" 2>/dev/null)
  [ "$s" = "Z" ] && z=$((z+1))
done
echo "ZOMBIES=$z"
`

func readAppleFacts(ctx context.Context, t *testing.T, p *Provider, containerID string) map[string]string {
	t.Helper()
	// Give the entrypoint a moment to reach `exec sleep infinity`; a container
	// reported as started is not yet a container that will accept an exec.
	var out string
	var lastErr error
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		res, err := p.Exec(ctx, provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         []string{"sh", "-c", appleProbeScript},
			User:        agentContainerUser,
		})
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		body, readErr := io.ReadAll(res.Reader)
		res.Reader.Close()
		if readErr != nil {
			lastErr = readErr
			time.Sleep(2 * time.Second)
			continue
		}
		out = string(body)
		if strings.Contains(out, "ZOMBIES=") {
			break
		}
		lastErr = fmt.Errorf("probe produced no terminating line; output was: %q", out)
		time.Sleep(2 * time.Second)
	}
	if !strings.Contains(out, "ZOMBIES=") {
		t.Fatalf("probe never completed inside the container: %v", lastErr)
	}

	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if eq := strings.IndexByte(line, '='); eq > 0 {
			facts[line[:eq]] = strings.TrimSpace(line[eq+1:])
		}
	}
	return facts
}

func evaluateApple(t *testing.T, f map[string]string) []appleProbe {
	t.Helper()
	var probes []appleProbe
	add := func(pr appleProbe) { probes = append(probes, pr) }

	add(appleProbe{
		name: "exec runs as the configured uid:gid", want: agentContainerUser, got: f["WHOAMI"],
		honoured: f["WHOAMI"] == agentContainerUser, loadBearing: true,
		why: "ContainerUser reports this constant to keeper, which runs credential-injected commands with it",
	})

	// The load-bearing claim in this whole file: that a --ulimit set once at
	// create time is inherited by every `container exec`. It was read out of
	// ContainerExec.swift and never observed. If it is wrong, `core: 0` is not
	// in effect for the agent — and the agent's working directory is /output,
	// a real host bind, so a crash would write its credential environment to
	// host disk exactly as the Docker provider's #1666 fix prevented.
	for _, spec := range crewUlimits() {
		name, soft := parseAppleUlimit(t, spec)
		key := "RLIMIT_" + strings.ToUpper(name)
		want := soft
		add(appleProbe{
			name: "rlimit " + name + " reaches an exec", want: strconv.FormatInt(want, 10), got: f[key],
			honoured:    f[key] == strconv.FormatInt(want, 10),
			loadBearing: name == "core",
			why:         "create-time rlimits binding every exec is read from Apple's sources, not measured — this is the measurement",
		})
	}

	add(appleProbe{
		name: "root filesystem is read-only (--read-only)", want: "no", got: f["ETC_WRITABLE"],
		honoured: f["ETC_WRITABLE"] == "no", loadBearing: true,
		why: "read-only /etc is what makes a user-supplied base image safe to hand an agent",
	})
	add(appleProbe{
		name: "PID 1 reaps orphans (--init)", want: "0 zombies", got: f["ZOMBIES"] + " zombies",
		honoured: f["ZOMBIES"] == "0", loadBearing: true,
		why: "the entrypoint ends in `exec sleep infinity`, which never calls wait(); every orphan would leak permanently",
	})

	// /secrets is the mount that makes file-delivered credentials possible on
	// this provider at all — without it every run carrying one aborts at the
	// mkdir preflight against a read-only rootfs.
	add(appleProbe{
		name: "/secrets is a tmpfs", want: "tmpfs", got: f["SECRETS_TYPE"],
		honoured: f["SECRETS_TYPE"] == "tmpfs", loadBearing: true,
		why: "a host-backed /secrets would persist cleartext credentials on disk past container removal",
	})
	add(appleProbe{
		name: "/secrets is writable by the agent", want: "yes", got: f["WRITE_SECRETS"],
		honoured: f["WRITE_SECRETS"] == "yes", loadBearing: true,
		why: "mode 1777 is the documented Apple-specific compromise precisely so this works; if it does not, credential delivery is dead",
	})
	add(appleProbe{
		name: "/secrets carries the mode the spec asks for", want: "1777", got: f["SECRETS_STAT"],
		honoured: strings.HasPrefix(f["SECRETS_STAT"], "1777"),
		why:      "the sticky bit is what stops one uid unlinking another's credential directory",
	})
	add(appleProbe{
		name: "/secrets is size-capped at 16 MiB", want: "size=16384k", got: appleMountOpt(f["SECRETS_OPTS"], "size"),
		honoured: appleMountOpt(f["SECRETS_OPTS"], "size") == "16384k",
		why:      "an uncapped tmpfs is charged against the VM's memory and a runaway writer takes the crew down",
	})

	for _, m := range []struct{ key, path, kind string }{
		{"WRITE_CREW", "/crew", "host bind"},
		{"WRITE_WORKSPACE", "/workspace", "host bind"},
		{"WRITE_OUTPUT", "/output", "host bind"},
		{"WRITE_HOME_AGENT", "/home/agent", "tmpfs"},
		{"WRITE_TMP", "/tmp", "tmpfs"},
	} {
		add(appleProbe{
			name: "agent can write " + m.path + " (" + m.kind + ")", want: "yes", got: f[m.key],
			honoured: f[m.key] == "yes", loadBearing: true,
			why: "the agent's own working directories; unwritable means every run fails at preflight",
		})
	}

	add(appleProbe{
		name: "the sidecar binary is present", want: "yes", got: f["SIDECAR_PRESENT"],
		honoured: f["SIDECAR_PRESENT"] == "yes", loadBearing: true,
		why: "the egress fence for `restricted` crews is this binary; absent, the mode is reported and not enforced (#1648)",
	})
	add(appleProbe{
		name: "the entrypoint is present", want: "yes", got: f["ENTRYPOINT_PRESENT"],
		honoured: f["ENTRYPOINT_PRESENT"] == "yes", loadBearing: true,
		why: "single-file bind mounts are the mechanism both artefacts arrive by; if one fails, both do",
	})

	// Resources. Each Apple container is its own VM, so these are the guest's
	// own view rather than a cgroup — /proc/meminfo IS the limit here.
	// Resources on this runtime are a FLOOR, not a cap, and that is measured
	// rather than assumed. `container run` on 1.2.0, three data points on a
	// 10-CPU / 16 GiB host:
	//
	//	--cpus 1 --memory 512M  → guest 2 CPUs,  MemTotal  611 MiB
	//	--cpus 2 --memory 1024M → guest 3 CPUs,  MemTotal 1112 MiB
	//	--cpus 4 --memory 2048M → guest 5 CPUs,  MemTotal 2113 MiB
	//
	// So the guest gets exactly one vCPU MORE than asked, every time, and
	// memory lands ~65–100 MiB above the request. A crew configured for 2 CPUs
	// runs on 3, which matters for capacity planning on this provider: at 20
	// crews the host is over-committed by 20 vCPUs relative to what the crew
	// rows say. Recorded rather than "fixed" — subtracting one from the request
	// to compensate would be guessing at an undocumented hypervisor detail, and
	// would silently give an operator asking for 1 CPU a container with 1.
	//
	// What the assertions therefore check is that the number REACHED the
	// runtime and scales with it, which is what would break if a future release
	// started ignoring the flag and handing every container the host's ten.
	wantMemBytes := int64(appleConformanceMemoryMB) << 20
	gotMemBytes := appleParseInt64(f["MEM_TOTAL"]) * 1024
	add(appleProbe{
		name: "memory request reaches the guest (floor, +overhead)", want: "≥ " + appleHumanBytes(wantMemBytes), got: appleHumanBytes(gotMemBytes),
		honoured:    gotMemBytes >= wantMemBytes && gotMemBytes <= wantMemBytes+(256<<20),
		loadBearing: true,
		why:         "an unbounded crew VM competes with the host for the whole machine",
	})
	gotCPUs := appleParseInt64(f["NPROC_ONLN"])
	add(appleProbe{
		name: "CPU request reaches the guest (floor, +1 vCPU)", want: strconv.Itoa(appleConformanceCPUs) + " or " + strconv.Itoa(appleConformanceCPUs+1), got: f["NPROC_ONLN"],
		honoured:    gotCPUs >= appleConformanceCPUs && gotCPUs <= appleConformanceCPUs+1,
		loadBearing: true,
		why:         "a container handed the whole host's CPU count would mean the crew's cpus setting reaches nothing",
	})

	return probes
}

// parseAppleUlimit splits Apple's <type>=<soft>[:<hard>] form. Everything is
// compared against /proc/self/limits, which reports bytes, so no unit
// conversion is involved.
func parseAppleUlimit(t *testing.T, spec string) (name string, soft int64) {
	t.Helper()
	eq := strings.IndexByte(spec, '=')
	if eq < 0 {
		t.Fatalf("malformed ulimit spec %q", spec)
	}
	name = spec[:eq]
	softStr, _, _ := strings.Cut(spec[eq+1:], ":")
	return name, appleParseInt64(softStr)
}

func reportApple(t *testing.T, probes []appleProbe) {
	t.Helper()
	var failed []appleProbe
	t.Log("── apple container runtime conformance ──")
	for _, pr := range probes {
		mark := "ok  "
		if !pr.honoured {
			mark = "MISS"
		}
		t.Logf("  [%s] %-52s want=%-16s got=%s", mark, pr.name, pr.want, pr.got)
		if !pr.honoured && pr.loadBearing {
			failed = append(failed, pr)
		}
	}
	if len(failed) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the Apple Container runtime does not honour %d load-bearing invariant(s):\n", len(failed))
	for _, pr := range failed {
		fmt.Fprintf(&b, "  · %s\n      want: %s\n      got:  %s\n      why:  %s\n", pr.name, pr.want, pr.got, pr.why)
	}
	t.Fatal(b.String())
}

// appleMountOpt pulls one directive out of a /proc/mounts option list.
func appleMountOpt(opts, name string) string {
	for _, part := range strings.Split(opts, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), name+"="); ok {
			return v
		}
	}
	return ""
}

func appleParseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func appleHumanBytes(n int64) string {
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
