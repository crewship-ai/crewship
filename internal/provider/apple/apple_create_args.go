package apple

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Container-side destinations for the two host artefacts a crew container
// cannot function without. Deliberately identical to the Docker provider's
// targets (internal/provider/docker/docker.go buildMounts): everything the
// orchestrator does inside the container is provider-agnostic. It launches the
// proxy as a bare `crewship-sidecar` off $PATH (exec_sidecar.go
// sidecarLaunchScript) and /usr/local/bin is on the default PATH, and the
// entrypoint is addressed by absolute path. Diverging here would mean two
// in-container contracts to keep in step.
const (
	sidecarTargetPath    = "/usr/local/bin/crewship-sidecar"
	entrypointTargetPath = "/usr/local/bin/entrypoint.sh"
)

// secretsMountSpec is the `container create --mount` spec for /secrets.
//
// Why /secrets must exist at all: the orchestrator writes cleartext SSH keys,
// passwords, tokens and certificates under /secrets/<agent-slug>/ at every run
// setup (internal/orchestrator/exec_sidecar.go writeCredentialFiles). Without
// the mount there is nowhere for them to land — the crew rootfs is --read-only
// — so every run carrying a file-delivered credential aborts at the
// mkdir preflight. This is the mount that makes file credential delivery
// possible on this provider at all.
//
// tmpfs, never a host bind: a bind persisted those bytes on host disk,
// surviving container removal and leaking into anything that archives
// OutputBasePath. Nothing reads /secrets across container recreation (the
// files are unconditionally rewritten each run), so an in-memory mount loses
// nothing. size=16M bounds a runaway writer; credential files are a handful of
// tokens per agent, so it is orders of magnitude of headroom.
//
// mode=1777, NOT the Docker spec's 0700 — and this is a real, deliberate
// difference the operator should know about. Docker gets
// `mode=0700,uid=1001,gid=1001`, i.e. a mount root owned by the agent UID.
// Apple's CLI has no uid/gid mount directive: `container create --mount`
// accepts exactly type, size, mode, source/src, destination/dst/target and
// readonly/ro, and rejects anything else outright with "unknown directive"
// (Sources/Services/ContainerAPIService/Client/Parser.swift, verified against
// the 1.2.0 tag). A tmpfs is mounted by the guest init as root, and the crew
// container's init process runs as UID 1001 with no way back to root, so a
// root-owned 0700 mount root would be one the agent cannot even traverse —
// credential delivery would fail exactly as it does today, just for a
// different reason. 1777 (sticky, world-writable, the same permissions the
// kernel gives a default tmpfs and Docker gives /tmp) is the closest mount
// that is actually usable.
//
// What that costs, precisely: on Docker the sidecar UID (1002) cannot even
// list /secrets. Here it can list it and see agent slugs. It still cannot read
// any credential, because writeCredentialFiles chmods the per-agent directory
// to 0700 and each file to 0400/0600 as UID 1001 — the secrets themselves keep
// Docker's protection. The sticky bit stops one UID unlinking another's
// entries. Closing the remaining gap needs a uid/gid directive that the CLI
// does not have.
const secretsMountSpec = "type=tmpfs,destination=/secrets,size=16M,mode=1777"

// crewFsizeBytes caps any single file the crew can write. 4 GiB on /output or
// /crew is a runaway log or a stuck writer filling the host disk, not
// legitimate agent output; exceeding it raises SIGXFSZ in the writer rather
// than taking the host's filesystem down.
const crewFsizeBytes = 4 << 30

// crewUlimits returns the per-process rlimits for everything in the crew
// container, in Apple's `<type>=<soft>[:<hard>]` form.
//
// These reach the agent, which is the only reason they are worth setting. The
// agent never runs as the container's init process — every run is a
// `container exec` — and Apple's exec builds its process configuration as
// `var config = container.configuration.initProcess`, overriding only
// executable, arguments, terminal, environment, working directory and user.
// rlimits are inherited verbatim from the init process, so a limit set once at
// create time binds every subsequent exec. (Verified in
// Sources/ContainerCommands/Container/ContainerExec.swift at the 1.2.0 tag.
// The exec command does accept its own --ulimit and then never reads it, so
// there is no per-exec override to worry about either.)
//
//	core   0/0     THE ONE THAT MATTERS, and it is a live credential exposure
//	               rather than a missing hardening nicety. The exec working
//	               directory is /output/<agent-slug>
//	               (orchestrator_run.go sets it provider-agnostically), and
//	               /output is a real host bind here too — see the -v below.
//	               An unlimited core dump from a crashing agent lands there
//	               containing every credential in the exec environment, and
//	               survives the container it came from. That defeats the whole
//	               point of /secrets being in memory. Hard 0 so nothing inside
//	               the container can raise it back.
//	nofile 8192/65536
//	               Generous for a Node/Python toolchain plus watchers (a busy
//	               dev server sits in the low thousands) while still bounding
//	               a descriptor leak. Also avoids the pathology where a very
//	               large soft limit makes anything that loops to
//	               RLIMIT_NOFILE on fork burn seconds per exec.
//	nproc  4096/4096
//	               Carried, but NOT for the Docker path's reason, and the
//	               difference is worth stating. There, RLIMIT_NPROC is enforced
//	               per real uid and is not namespaced, so every crew running as
//	               uid 1001 shares one host budget and the value is sized so
//	               co-resident crews cannot starve each other — with PidsLimit
//	               (200) as the actual binding per-container cap. Neither half
//	               transfers. Each crew here is its own VM with its own kernel,
//	               so uid 1001's budget is per-crew and a fork bomb cannot
//	               reach a sibling or the host. And there is no PidsLimit
//	               equivalent: `container create` has no --pids-limit, so this
//	               is the ONLY fork backstop the crew has. It is a much looser
//	               one than 200, which is the honest trade — the value is kept
//	               identical to the Docker path because it is a tested ceiling
//	               for real toolchains, and inventing a tighter number with no
//	               runtime to try it on is how working crews get broken.
//	fsize  4 GiB   See crewFsizeBytes. /output and /crew are host binds on both
//	               providers, so this rationale transfers unchanged.
//
// Deliberately duplicated from the Docker provider's crewUlimits() rather than
// hoisted into internal/provider, for the same reason verifySidecarIsLinuxELF
// below is: sharing means editing a package other work is in flight against,
// and the two policies are not actually the same policy — nproc means
// something different on each, and only one of the two has a PidsLimit behind
// it. The numbers agreeing today is a coincidence worth keeping visible rather
// than a contract worth encoding. The values are pinned by test; the twin is
// named here so a future change to either finds the other.
func crewUlimits() []string {
	return []string{
		"core=0:0",
		"nofile=8192:65536",
		"nproc=4096:4096",
		fmt.Sprintf("fsize=%d:%d", crewFsizeBytes, crewFsizeBytes),
	}
}

// createArgsInput is everything buildCreateArgs needs to render a
// `container create` invocation. Split out from EnsureCrewRuntime so the
// argument construction — the only part of the create path that can be checked
// without an Apple Container runtime on the machine — is a pure function with
// no filesystem or process side effects beyond reading the sidecar binary's
// magic number.
type createArgsInput struct {
	containerName string
	image         string
	network       string
	cpus          int
	memoryMB      int
	crewID        string
	workspacePath string
	outputPath    string
	crewPath      string

	// sidecarPath / entrypointPath are HOST paths, bind-mounted read-only.
	sidecarPath    string
	entrypointPath string

	// containerEnv is the crew's devcontainer containerEnv. Passed explicitly
	// rather than reported as dropped: `container create` has supported --env
	// all along (CREWSHIP_CREW_ID already used it), and for an unprovisioned
	// crew nothing else supplies these — a provisioned one gets them a second
	// time from the image's own ENV, which is harmless (#1779).
	containerEnv map[string]string
}

// buildCreateArgs renders the full `container create` argument vector for a
// crew container.
//
// The image is always the LAST element: Apple's CLI parses everything after
// the image reference as the container process's argv
// (`@Argument(parsing: .captureForPassthrough)`), so an option emitted after
// it would silently become a command-line argument to the entrypoint instead
// of an option to the CLI.
func buildCreateArgs(in createArgsInput) ([]string, error) {
	// Mandatory, and mandatory loudly. Both paths are populated by
	// internal/config autodetect (resolveSidecarPaths), which already fails
	// startup with an actionable message when it cannot find them. Reaching
	// this function with either empty means a caller built a Config by hand;
	// creating the container anyway would reproduce exactly the bug this
	// closes — a crew that the database, the dashboard and `crewship crew get`
	// all report as network-restricted, with no proxy binary inside it to
	// enforce that (#1648).
	if in.sidecarPath == "" {
		return nil, fmt.Errorf("apple provider: SidecarBinaryPath is required (run 'make build:sidecar' or set CREWSHIP_SIDECAR_PATH)")
	}
	if in.entrypointPath == "" {
		return nil, fmt.Errorf("apple provider: EntrypointPath is required (run 'make build:sidecar' or set CREWSHIP_ENTRYPOINT_PATH)")
	}
	if err := verifySidecarIsLinuxELF(in.sidecarPath); err != nil {
		return nil, err
	}

	args := []string{
		"create",
		"--name", in.containerName,
		"--cpus", fmt.Sprintf("%d", in.cpus),
		"--memory", fmt.Sprintf("%dM", in.memoryMB),
		"--read-only",
		// PID 1 reaping. The container's init process ends in
		// `exec sleep infinity` (scripts/entrypoint.sh), which never calls
		// wait(), and the sidecar is launched as `… &` inside an `sh -c` exec
		// so it is ALWAYS reparented onto PID 1. Every such orphan would
		// become a permanent zombie — a monotonic leak that ends in
		// `fork: Resource temporarily unavailable` for the whole crew. Apple's
		// --init is the direct equivalent of Docker's HostConfig.Init: "Run an
		// init process inside the container that forwards signals and reaps
		// processes".
		"--init",
		"--env", "CREWSHIP_CREW_ID=" + in.crewID,
		"-v", in.workspacePath + ":/workspace",
		"-v", in.outputPath + ":/output",
		"-v", in.crewPath + ":/crew",
		// The egress fence. A crew whose network_mode is `restricted` — the
		// database default since migration v148 — is enforced by the
		// crewship-sidecar proxy running inside the container. The binary
		// reaches the container only by being mounted from the host, so
		// without this line the mode is reported everywhere and enforced
		// nowhere (#1648).
		//
		// Single-file bind, read-only, via -v rather than --mount: Apple's
		// --mount rejects a non-directory source outright ("path '…' is not a
		// directory"), while the -v parser accepts any existing path and
		// containerization resolves single files by hardlinking into a managed
		// temp directory and bind-mounting the file in
		// (apple/containerization#487, shipped well before container 1.0).
		"-v", in.sidecarPath + ":" + sidecarTargetPath + ":ro",
		"-v", in.entrypointPath + ":" + entrypointTargetPath + ":ro",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
		// /secrets — see secretsMountSpec. --mount rather than --tmpfs
		// because the bare --tmpfs flag takes a path and nothing else
		// (Parser.tmpfsMounts hardcodes an empty option list), so it can
		// express neither the size cap nor the mode.
		"--mount", secretsMountSpec,
	}

	// Process rlimits. Inherited by every exec — see crewUlimits.
	for _, limit := range crewUlimits() {
		args = append(args, "--ulimit", limit)
	}

	if in.network != "" {
		args = append(args, "--network", in.network)
	}

	// Apple Containers use --user for the init process user.
	args = append(args, "--user", agentContainerUser)

	// Force the bind-mounted entrypoint so a user-provided base image
	// (debian, ubuntu, a devcontainer base) runs it rather than its own
	// CMD. Apple's --entrypoint replaces the image entrypoint AND suppresses
	// the image CMD when no trailing arguments are given
	// (Parser.process: `hasEntrypointOverride`), which is exactly Docker's
	// semantics — so no explicit keep-alive argv is needed here. The script
	// itself ends in `exec sleep infinity`, which is what keeps the container
	// up for the exec pattern.
	// Sorted so the same crew config always renders the same argument vector;
	// map order would make two identical starts incomparable.
	envKeys := make([]string, 0, len(in.containerEnv))
	for k := range in.containerEnv {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "--env", k+"="+in.containerEnv[k])
	}

	args = append(args, "--entrypoint", entrypointTargetPath)

	// Image LAST — see the doc comment.
	args = append(args, in.image)
	return args, nil
}

// verifySidecarIsLinuxELF guards against a host sidecar binary that cannot
// exec inside the container. Apple Containers are Linux VMs, so the mounted
// sidecar must be a Linux ELF regardless of the host OS — and this provider
// only ever runs on macOS, where a source checkout's `go build ./cmd/
// crewship-sidecar` produces a Mach-O binary that would mount cleanly and then
// fail to exec, leaving the crew with a silently unenforced egress policy. The
// release archives bundle the LINUX sidecar precisely for this reason
// (.goreleaser.yml, #953); this catches the developer build that does not.
//
// Mirrors the Docker provider's identically-named guard. Unreadable paths are
// deliberately NOT this guard's problem: existence and permission failures
// keep their existing error path (the CLI's own mount error), and turning a
// transient read error into "wrong format" would mislead.
func verifySidecarIsLinuxELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return nil //nolint:nilerr // existence/permission problems surface via the CLI's mount error
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil //nolint:nilerr // too short / unreadable — let the container-create path report it
	}
	if magic == [4]byte{0x7f, 'E', 'L', 'F'} {
		return nil
	}
	detected := "an unrecognized binary format"
	switch magic {
	case [4]byte{0xcf, 0xfa, 0xed, 0xfe}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
		[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xfe, 0xed, 0xfa, 0xce},
		[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca}:
		detected = "a macOS Mach-O binary"
	}
	return fmt.Errorf(
		"apple provider: sidecar at %s is %s, but crew containers are Linux VMs — the sidecar must be a Linux ELF build (#953); reinstall crewship (the release archive bundles the correct Linux sidecar) or point CREWSHIP_SIDECAR_PATH at a GOOS=linux build",
		path, detected,
	)
}

// appleMountDirectives is the exact set of `--mount` directives Apple's CLI
// accepts; anything else is rejected with "unknown directive <k> when parsing
// mount". Kept here as the machine-readable form of that contract so a test
// can hold every mount spec this package emits against it — the runtime that
// would otherwise catch a typo is not present on developer machines or in CI
// (#1650). Notably absent, and the reason /secrets cannot be owned by the
// agent UID here: uid and gid.
var appleMountDirectives = map[string]bool{
	"type": true, "size": true, "mode": true,
	"source": true, "src": true,
	"destination": true, "dst": true, "target": true,
	"readonly": true, "ro": true,
}

// validAppleMountSpec reports whether every directive in a --mount spec is one
// Apple's parser accepts. Used by the tests, and cheap enough to keep beside
// the map it validates against.
func validAppleMountSpec(spec string) error {
	for _, part := range strings.Split(spec, ",") {
		key, _, _ := strings.Cut(part, "=")
		if !appleMountDirectives[key] {
			return fmt.Errorf("unknown directive %q when parsing mount %q", key, spec)
		}
	}
	return nil
}

// appleUlimitTypes is the exact set of --ulimit type names Apple's CLI maps to
// an RLIMIT_*; an unlisted one is rejected with "unsupported ulimit type".
// The same machine-readable-contract trick as appleMountDirectives, for the
// same reason: no runtime here to catch a typo (#1650).
var appleUlimitTypes = map[string]bool{
	"core": true, "cpu": true, "data": true, "fsize": true, "locks": true,
	"memlock": true, "msgqueue": true, "nice": true, "nofile": true,
	"nproc": true, "rss": true, "rtprio": true, "rttime": true,
	"sigpending": true, "stack": true,
}

// validAppleUlimitSpec reports whether a --ulimit spec would survive Apple's
// Parser.rlimit: `<type>=<soft>[:<hard>]`, a known type, values that are
// non-negative integers or "unlimited"/"-1", and soft never above hard.
func validAppleUlimitSpec(spec string) error {
	name, values, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("invalid ulimit format %q: expected <type>=<soft>[:<hard>]", spec)
	}
	if !appleUlimitTypes[name] {
		return fmt.Errorf("unsupported ulimit type %q", name)
	}
	parts := strings.SplitN(values, ":", 2)
	parsed := make([]uint64, 0, len(parts))
	for _, p := range parts {
		if p == "unlimited" || p == "-1" {
			parsed = append(parsed, math.MaxUint64)
			continue
		}
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid ulimit value %q for %q: must be a non-negative integer or 'unlimited'", p, name)
		}
		parsed = append(parsed, v)
	}
	if len(parsed) == 2 && parsed[0] > parsed[1] {
		return fmt.Errorf("ulimit %q soft limit (%d) cannot exceed hard limit (%d)", name, parsed[0], parsed[1])
	}
	return nil
}
