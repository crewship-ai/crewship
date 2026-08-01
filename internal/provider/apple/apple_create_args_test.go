package apple

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The Apple Container runtime is not installed on developer machines and not
// in CI (#1650), so nothing in this package can drive a real `container
// create`. What these tests CAN prove is that the argument vector we hand the
// CLI says what we mean it to say, and that every construct in it is one the
// CLI's own parser accepts — the parser semantics asserted here were read off
// Sources/Services/ContainerAPIService/Client/Parser.swift at the 1.2.0 tag,
// not inferred from the Docker equivalents. What they cannot prove is that the
// guest honours the mounts once made; that needs a runtime.

var (
	testSidecarPath    string
	testEntrypointPath string
)

// TestMain stages the two host artefacts every crew container mounts. They
// have to be real files on disk: buildCreateArgs reads the sidecar's magic
// number to reject a non-Linux build, so a bare path string would exercise a
// different branch than production does.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crewship-apple-artefacts-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "stage test artefacts:", err)
		os.Exit(1)
	}
	testSidecarPath = filepath.Join(dir, "crewship-sidecar")
	testEntrypointPath = filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(testSidecarPath, []byte("\x7fELF\x02\x01\x01 fake linux sidecar"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "write fake sidecar:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(testEntrypointPath, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "write fake entrypoint:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withTestSidecarArtefacts fills the two mandatory host paths on a Config that
// did not set them, so the pre-existing tests in this package (which predate
// the mounts and care about other things entirely) keep exercising the create
// path rather than tripping the new required-path guard.
func withTestSidecarArtefacts(cfg Config) Config {
	if cfg.SidecarBinaryPath == "" {
		cfg.SidecarBinaryPath = testSidecarPath
	}
	if cfg.EntrypointPath == "" {
		cfg.EntrypointPath = testEntrypointPath
	}
	return cfg
}

func testCreateArgsInput() createArgsInput {
	return createArgsInput{
		containerName:  "crewship-team-eng-crew1",
		image:          "img:1",
		cpus:           2,
		memoryMB:       1024,
		crewID:         "crew1",
		workspacePath:  "/base/workspaces/crew1",
		outputPath:     "/base/crew1",
		crewPath:       "/base/crews/crew1",
		sidecarPath:    testSidecarPath,
		entrypointPath: testEntrypointPath,
	}
}

// flagValues returns every value passed for a repeated `--flag value` option.
func flagValues(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func mustBuild(t *testing.T, in createArgsInput) []string {
	t.Helper()
	args, err := buildCreateArgs(in)
	if err != nil {
		t.Fatalf("buildCreateArgs: %v", err)
	}
	return args
}

// mountDirectives splits a --mount spec into its key/value directives.
func mountDirectives(spec string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		k, v, _ := strings.Cut(part, "=")
		out[k] = v
	}
	return out
}

// The /secrets tmpfs is the mount that makes file-delivered credentials
// possible at all: the rootfs is --read-only, so without it every run carrying
// a file credential has nowhere to write and aborts.
func TestBuildCreateArgsMountsSecretsTmpfs(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())

	mounts := flagValues(args, "--mount")
	if len(mounts) != 1 {
		t.Fatalf("--mount count = %d (%v), want exactly the /secrets tmpfs", len(mounts), mounts)
	}
	d := mountDirectives(mounts[0])

	if d["type"] != "tmpfs" {
		t.Errorf("/secrets type = %q, want tmpfs — a host bind would persist cleartext credentials on disk past container removal", d["type"])
	}
	if d["destination"] != "/secrets" {
		t.Errorf("/secrets destination = %q, want /secrets", d["destination"])
	}
	if d["size"] == "" {
		t.Error("/secrets has no size cap; tmpfs pages count against container memory, an uncapped one is a host-memory DoS")
	}
	if _, ok := d["mode"]; !ok {
		t.Fatal("/secrets has no mode; the default would be whatever the guest kernel picks")
	}

	// Apple's CLI has no uid/gid mount directive, so the mount root is
	// root-owned no matter what. The crew's init process runs as UID 1001 with
	// no route back to root, so a Docker-style 0700 would be a directory the
	// agent cannot even traverse — credential delivery would still fail, just
	// later and less legibly. The mode therefore has to be one the agent can
	// write through, and sticky so one UID cannot unlink another's entries.
	mode, err := strconv.ParseInt(d["mode"], 8, 32)
	if err != nil {
		t.Fatalf("mode %q is not octal: %v", d["mode"], err)
	}
	if mode&0o002 == 0 {
		t.Errorf("mode %s is not writable by the agent UID; a root-owned mount the agent cannot write is no better than no mount", d["mode"])
	}
	if mode&0o1000 == 0 {
		t.Errorf("mode %s lacks the sticky bit; a world-writable /secrets without it lets any UID unlink another's credential files", d["mode"])
	}
}

// Every directive we emit has to be one Apple's parser knows. It rejects an
// unknown key outright ("unknown directive …"), which would fail container
// creation for every crew — and the runtime that would catch that is not on
// this machine.
func TestBuildCreateArgsEmitsOnlyDirectivesAppleAccepts(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())

	for _, spec := range flagValues(args, "--mount") {
		if err := validAppleMountSpec(spec); err != nil {
			t.Errorf("mount spec %q would be rejected by the CLI: %v", spec, err)
		}
	}

	// Named explicitly rather than left to the whitelist: uid/gid are the two
	// Docker uses for /secrets and the two Apple does not have. If a future
	// CLI gains them, this is the test that should start looking wrong.
	for _, spec := range flagValues(args, "--mount") {
		d := mountDirectives(spec)
		if _, ok := d["uid"]; ok {
			t.Errorf("mount %q sets uid, which Apple's parser rejects", spec)
		}
		if _, ok := d["gid"]; ok {
			t.Errorf("mount %q sets gid, which Apple's parser rejects", spec)
		}
	}
}

// core=0:0 is the one rlimit that closes a live credential exposure rather
// than adding a hardening nicety: the exec working directory is
// /output/<agent-slug>, which is a host bind here as well, so an unlimited
// core dump from a crashing agent writes its entire credential environment to
// host disk and outlives the container.
func TestBuildCreateArgsDisablesCoreDumps(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())

	limits := flagValues(args, "--ulimit")
	var core string
	for _, l := range limits {
		if strings.HasPrefix(l, "core=") {
			core = l
		}
	}
	if core == "" {
		t.Fatalf("no core rlimit; a crashing agent dumps its credential environment onto the host bind. limits: %v", limits)
	}

	soft, hard, ok := strings.Cut(strings.TrimPrefix(core, "core="), ":")
	if !ok {
		t.Fatalf("core rlimit %q has no hard limit; without it the container can raise the soft one back", core)
	}
	if soft != "0" {
		t.Errorf("core soft limit = %q, want 0", soft)
	}
	if hard != "0" {
		t.Errorf("core hard limit = %q, want 0 — a non-zero hard limit is one setrlimit call away from unlimited", hard)
	}
}

// The remaining three are carried from the Docker path. Pinned by value
// because they are duplicated rather than shared: if either side moves, this
// is the test that should be read alongside the other provider's crewUlimits.
func TestBuildCreateArgsCarriesTheRemainingRlimits(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())
	limits := flagValues(args, "--ulimit")

	for _, want := range []string{
		"core=0:0",
		"nofile=8192:65536",
		"nproc=4096:4096",
		"fsize=4294967296:4294967296",
	} {
		if !containsString(limits, want) {
			t.Errorf("rlimit %q missing from %v", want, limits)
		}
	}
	if len(limits) != 4 {
		t.Errorf("rlimit count = %d (%v), want exactly the four carried from the Docker path", len(limits), limits)
	}
}

// Apple rejects an unknown ulimit type, a malformed value, and a soft limit
// above its hard limit — any of which fails container creation for every crew,
// with no runtime here to catch it.
func TestBuildCreateArgsEmitsParseableRlimits(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())

	seen := map[string]bool{}
	for _, spec := range flagValues(args, "--ulimit") {
		if err := validAppleUlimitSpec(spec); err != nil {
			t.Errorf("rlimit %q would be rejected by the CLI: %v", spec, err)
		}
		name, _, _ := strings.Cut(spec, "=")
		if seen[name] {
			t.Errorf("rlimit type %q emitted twice; the CLI rejects a duplicate outright", name)
		}
		seen[name] = true
	}

	// The validator has to be able to say no, or the assertion above is
	// vacuous. These are the three shapes Parser.rlimit rejects.
	for _, bad := range []string{"core", "bogus=0:0", "core=x", "nofile=99:1"} {
		if err := validAppleUlimitSpec(bad); err == nil {
			t.Errorf("validAppleUlimitSpec(%q) = nil, want a rejection", bad)
		}
	}
	for _, good := range []string{"core=0", "nofile=8192:65536", "nproc=unlimited", "fsize=1:-1"} {
		if err := validAppleUlimitSpec(good); err != nil {
			t.Errorf("validAppleUlimitSpec(%q) = %v, want accepted", good, err)
		}
	}
}

// The sidecar binary is the egress fence. network_mode `restricted` is the
// database default and is enforced by this proxy running inside the container;
// if the binary never arrives the mode is reported by the API, the dashboard
// and the CLI while nothing enforces it (#1648).
func TestBuildCreateArgsMountsSidecarBinaryReadOnly(t *testing.T) {
	in := testCreateArgsInput()
	args := mustBuild(t, in)

	want := in.sidecarPath + ":" + sidecarTargetPath + ":ro"
	if !containsString(flagValues(args, "-v"), want) {
		t.Fatalf("sidecar bind %q not in -v mounts %v", want, flagValues(args, "-v"))
	}
	if sidecarTargetPath != "/usr/local/bin/crewship-sidecar" {
		t.Errorf("sidecar target = %q; the orchestrator launches a bare `crewship-sidecar` off $PATH", sidecarTargetPath)
	}
}

// The entrypoint has to be both mounted and forced: a user-provided base image
// (debian, ubuntu, a devcontainer base) has its own CMD and would never run
// ours otherwise.
func TestBuildCreateArgsMountsAndForcesEntrypoint(t *testing.T) {
	in := testCreateArgsInput()
	args := mustBuild(t, in)

	want := in.entrypointPath + ":" + entrypointTargetPath + ":ro"
	if !containsString(flagValues(args, "-v"), want) {
		t.Fatalf("entrypoint bind %q not in -v mounts %v", want, flagValues(args, "-v"))
	}
	if got := flagValues(args, "--entrypoint"); len(got) != 1 || got[0] != entrypointTargetPath {
		t.Fatalf("--entrypoint = %v, want [%s]", got, entrypointTargetPath)
	}

	// Apple suppresses the image CMD when --entrypoint is given and no
	// trailing arguments are present, so the old `sleep infinity` argv is not
	// only unnecessary, it would be passed to entrypoint.sh as arguments.
	for _, a := range args {
		if a == "sleep" || a == "infinity" {
			t.Errorf("create args still carry the standalone keep-alive argv %q; entrypoint.sh ends in `exec sleep infinity` itself", a)
		}
	}
}

// PID 1 never calls wait(), and the sidecar is launched backgrounded inside an
// `sh -c` exec so it is always reparented onto it. Without an init the zombies
// accumulate against the pid limit until the crew cannot fork.
func TestBuildCreateArgsRequestsInitProcess(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())
	if !hasFlag(args, "--init") {
		t.Fatalf("--init missing; orphaned sidecar processes would never be reaped. args: %v", args)
	}
}

// Apple parses everything after the image reference as the container
// process's argv, so an option emitted after the image silently stops being an
// option.
func TestBuildCreateArgsPutsImageLast(t *testing.T) {
	in := testCreateArgsInput()
	args := mustBuild(t, in)

	if got := args[len(args)-1]; got != in.image {
		t.Fatalf("last arg = %q, want the image %q — anything after it is parsed as process argv", got, in.image)
	}
	// Once, not merely last: a second occurrence earlier in the vector would
	// make the CLI treat the trailing one as the container process's argv0.
	n := 0
	for _, a := range args {
		if a == in.image {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("image %q appears %d times; the extra one becomes process argv", in.image, n)
	}
	if args[0] != "create" {
		t.Fatalf("first arg = %q, want create", args[0])
	}
}

// Both host paths are mandatory. Creating the container without them would
// reproduce exactly the gap this closes: a crew reported as restricted with no
// proxy inside it, and credentials with nowhere to land.
func TestBuildCreateArgsRequiresHostArtefacts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*createArgsInput)
		wantSub string
	}{
		{"no sidecar", func(in *createArgsInput) { in.sidecarPath = "" }, "CREWSHIP_SIDECAR_PATH"},
		{"no entrypoint", func(in *createArgsInput) { in.entrypointPath = "" }, "CREWSHIP_ENTRYPOINT_PATH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := testCreateArgsInput()
			tc.mutate(&in)
			args, err := buildCreateArgs(in)
			if err == nil {
				t.Fatalf("buildCreateArgs succeeded with a missing artefact, args: %v", args)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v, want it to name %s so the operator can fix it", err, tc.wantSub)
			}
		})
	}
}

// A Mach-O sidecar mounts cleanly into a Linux VM and then fails to exec,
// which looks exactly like "the egress fence is on" from outside. Catch it on
// the host where the error can say what is wrong.
func TestBuildCreateArgsRejectsNonLinuxSidecar(t *testing.T) {
	dir := t.TempDir()

	machO := filepath.Join(dir, "crewship-sidecar")
	if err := os.WriteFile(machO, []byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00}, 0o755); err != nil {
		t.Fatal(err)
	}
	in := testCreateArgsInput()
	in.sidecarPath = machO
	_, err := buildCreateArgs(in)
	if err == nil {
		t.Fatal("a Mach-O sidecar was accepted; it would mount and then fail to exec inside the Linux VM")
	}
	if !strings.Contains(err.Error(), "Mach-O") {
		t.Errorf("err = %v, want it to name the detected format", err)
	}

	// Unknown-but-not-ELF is still refused, just less specifically.
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in.sidecarPath = junk
	if _, err := buildCreateArgs(in); err == nil {
		t.Error("a non-ELF sidecar was accepted")
	}

	// A path we cannot read is NOT this guard's problem — turning a permission
	// error into "wrong format" would send the operator after the wrong bug.
	in.sidecarPath = filepath.Join(dir, "does-not-exist")
	if _, err := buildCreateArgs(in); err != nil {
		t.Errorf("missing sidecar path should fall through to the CLI's own mount error, got %v", err)
	}
}

// Every -v we emit has to be shaped the way Apple's volume parser expects:
// source:destination[:options], with only options it understands.
func TestBuildCreateArgsVolumeSpecsAreParseable(t *testing.T) {
	args := mustBuild(t, testCreateArgsInput())

	vols := flagValues(args, "-v")
	if len(vols) != 5 {
		t.Fatalf("-v count = %d (%v), want workspace, output, crew, sidecar, entrypoint", len(vols), vols)
	}
	for _, v := range vols {
		parts := strings.Split(v, ":")
		if len(parts) < 2 || len(parts) > 3 {
			t.Errorf("volume %q has %d colon-separated fields; Apple accepts 2 or 3", v, len(parts))
			continue
		}
		if !strings.HasPrefix(parts[0], "/") {
			t.Errorf("volume %q source is not absolute", v)
		}
		if len(parts) == 3 {
			for _, opt := range strings.Split(parts[2], ",") {
				if opt != "ro" {
					t.Errorf("volume %q carries option %q; only ro is known to be honoured", v, opt)
				}
			}
		}
	}
}

// The three new mounts must not have cost the container anything it already
// had. This is the regression half of the change.
func TestBuildCreateArgsPreservesExistingContainerShape(t *testing.T) {
	in := testCreateArgsInput()
	in.network = "mynet"
	args := mustBuild(t, in)

	checks := []struct {
		flag string
		want string
	}{
		{"--name", in.containerName},
		{"--cpus", "2"},
		{"--memory", "1024M"},
		{"--env", "CREWSHIP_CREW_ID=crew1"},
		{"--network", "mynet"},
		{"--user", agentContainerUser},
	}
	for _, c := range checks {
		got := flagValues(args, c.flag)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s = %v, want [%s]", c.flag, got, c.want)
		}
	}
	if !hasFlag(args, "--read-only") {
		t.Error("--read-only dropped; the crew rootfs would become writable")
	}
	for _, want := range []string{"/tmp", "/home/agent"} {
		if !containsString(flagValues(args, "--tmpfs"), want) {
			t.Errorf("--tmpfs %s dropped", want)
		}
	}
	for _, want := range []string{
		in.workspacePath + ":/workspace",
		in.outputPath + ":/output",
		in.crewPath + ":/crew",
	} {
		if !containsString(flagValues(args, "-v"), want) {
			t.Errorf("bind %q dropped", want)
		}
	}

	// No --network at all when none is configured, rather than an empty value
	// the CLI would reject.
	in.network = ""
	if got := flagValues(mustBuild(t, in), "--network"); len(got) != 0 {
		t.Errorf("--network = %v with no network configured, want it absent", got)
	}
}

// End-to-end through the fake CLI: this drives the real EnsureCrewRuntime,
// including host directory creation and the existing-container probe, and
// asserts on the argv that actually reached the `container` binary. It is the
// most of this path that can be exercised without an Apple runtime.
func TestEnsureCrewRuntimeDeliversMountsToTheCLI(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	p := newTestProvider(Config{RuntimeImage: "img:1", OutputBasePath: t.TempDir()})

	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"}); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	var create string
	for _, c := range fake.calls(t) {
		if strings.HasPrefix(c, "create ") {
			create = c
		}
	}
	if create == "" {
		t.Fatalf("no create call reached the CLI, calls: %v", fake.calls(t))
	}

	for _, want := range []string{
		"--mount " + secretsMountSpec,
		testSidecarPath + ":" + sidecarTargetPath + ":ro",
		testEntrypointPath + ":" + entrypointTargetPath + ":ro",
		"--init",
		"--ulimit core=0:0",
		"--entrypoint " + entrypointTargetPath,
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create invocation missing %q\ngot: %s", want, create)
		}
	}
}

// If the sidecar binary is missing we must fail BEFORE creating anything. A
// container that comes up without the proxy is a crew the whole product
// reports as network-restricted and nothing enforces.
func TestEnsureCrewRuntimeRefusesToCreateWithoutSidecar(t *testing.T) {
	fake := installFakeContainer(t, crewBody)
	// Deliberately not newTestProvider: this case is about the paths being
	// absent, which that helper fills in.
	p := newTestProvider(Config{
		RuntimeImage:   "img:1",
		OutputBasePath: t.TempDir(),
		EntrypointPath: testEntrypointPath,
	})
	p.cfg.SidecarBinaryPath = ""

	_, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"})
	if err == nil {
		t.Fatal("EnsureCrewRuntime created a crew container with no sidecar binary")
	}
	if fake.hasCall(t, "create") {
		t.Errorf("a container was created despite the missing sidecar, calls: %v", fake.calls(t))
	}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
