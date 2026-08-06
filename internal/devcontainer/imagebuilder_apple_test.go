package devcontainer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingAppleCLI writes a stand-in `container` that records its argv and
// emits two build-log lines, then returns the record path. Asserting on the
// builder's fields would pass just as happily if Build never invoked anything —
// the argv that actually reaches the subprocess is the only real evidence.
func recordingAppleCLI(t *testing.T, exitCode int) (bin, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "container")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + recordPath + "\n" +
		"echo '#1 [internal] load build definition'\n" +
		"echo '#5 DONE 0.1s'\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, recordPath
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	return string(rune('0' + i))
}

func appleArgv(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake container CLI never ran (%v)", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestAppleBuilder_UnavailableWithoutBinary(t *testing.T) {
	b := &AppleContainerBuilder{logger: slog.Default()}
	if b.Available() {
		t.Fatal("a builder with no resolved binary must not report itself available")
	}
	if err := b.Build(context.Background(), t.TempDir(), "crewship-cache:x", nil); err == nil {
		t.Fatal("Build must fail when the CLI is unavailable")
	}
}

func TestAppleBuilder_AvailableWithBinary(t *testing.T) {
	bin, _ := recordingAppleCLI(t, 0)
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}
	if !b.Available() {
		t.Fatal("a builder with a resolved binary must report itself available")
	}
}

// The Dockerfile the provisioner generates opens with `# syntax=` and uses
// `RUN --mount=type=cache`, so the build has to run through the real frontend.
// It also has to be plain-progress: `auto` emits TTY control sequences, and
// those lines are streamed verbatim into provision events and the journal.
func TestAppleBuilder_InvokesContainerBuildWithTagFileAndPlainProgress(t *testing.T) {
	bin, rec := recordingAppleCLI(t, 0)
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}
	dir := t.TempDir()

	if err := b.Build(context.Background(), dir, "crewship-cache:abc123", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	argv := appleArgv(t, rec)
	for _, want := range []string{
		"build",
		"--tag crewship-cache:abc123",
		"--file " + filepath.Join(dir, "Dockerfile"),
		"--progress plain",
		dir,
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q missing %q", argv, want)
		}
	}
}

func TestAppleBuilder_StreamsBuildLogLines(t *testing.T) {
	bin, _ := recordingAppleCLI(t, 0)
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}

	var lines []string
	if err := b.Build(context.Background(), t.TempDir(), "crewship-cache:x", func(l string) {
		lines = append(lines, l)
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected the two emitted log lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "load build definition") {
		t.Errorf("first line not streamed through: %q", lines[0])
	}
}

func TestAppleBuilder_BuildFailureNamesTheTag(t *testing.T) {
	bin, _ := recordingAppleCLI(t, 1)
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}

	err := b.Build(context.Background(), t.TempDir(), "crewship-cache:deadbeef", nil)
	if err == nil {
		t.Fatal("a non-zero exit must surface as an error")
	}
	if !strings.Contains(err.Error(), "crewship-cache:deadbeef") {
		t.Errorf("error should name the tag being built, got %q", err)
	}
}

// Apple's `container build` can finish the image and then never exit: observed
// on 2026-08-06 with container 1.2.0 — the manifest was exported and tagged at
// 12:02, and the process then sat at 0% CPU for 13 minutes until it was killed.
// A provisioning job that waits on it waits forever, which is the one outcome a
// customer must never see. The builder therefore gives up on silence rather
// than on a total deadline: a legitimate build emits progress continuously, so
// a long quiet gap means the work is over even when the process disagrees.
func TestAppleBuilder_GivesUpWhenTheCLIGoesSilent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	// Emits one line, then sleeps far past the idle window without exiting.
	script := "#!/bin/sh\necho '#1 building'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default(), idleTimeout: 300 * time.Millisecond}

	start := time.Now()
	err := b.Build(context.Background(), dir, "crewship-cache:quiet", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a build that stops emitting must not hang the caller")
	}
	if elapsed > 10*time.Second {
		t.Errorf("gave up after %s — the idle watchdog did not fire", elapsed)
	}
}

// The silent build above may well have produced its image before going quiet —
// that is exactly what happened in the real run. Killing it must therefore not
// be reported as a failure when the tag is actually there.
func TestAppleBuilder_ImageExistsAsksTheCLIForTheTag(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	rec := filepath.Join(dir, "argv")
	script := "#!/bin/sh\necho \"$@\" > " + rec + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}

	ok, err := b.ImageExists(context.Background(), "crewship-cache:abc")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if !ok {
		t.Error("a zero exit from `image inspect` means the tag is present")
	}
	argv := appleArgv(t, rec)
	if !strings.Contains(argv, "image inspect crewship-cache:abc") {
		t.Errorf("argv %q should inspect the tag", argv)
	}
}

func TestAppleBuilder_ImageExistsReportsAbsence(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default()}

	ok, err := b.ImageExists(context.Background(), "crewship-cache:missing")
	if err != nil {
		t.Fatalf("ImageExists must not error on a plain absence: %v", err)
	}
	if ok {
		t.Error("a non-zero exit means the tag is not there")
	}
}

// A build that goes quiet without ever announcing an export really did fail,
// so the short settle delay must not be mistaken for success.
func TestAppleBuilder_KeepsWaitingWhileTheImageIsAbsent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"image\" ]; then exit 1; fi\n" +
		"echo '#1 building'\n" +
		"sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{
		bin:         bin,
		logger:      slog.Default(),
		idleTimeout: 600 * time.Millisecond,
		settleDelay: 100 * time.Millisecond,
	}

	err := b.Build(context.Background(), dir, "crewship-cache:absent", nil)
	if err == nil {
		t.Fatal("no image and no output must remain a failure")
	}
}

func TestBuilderShare_LeavesHeadroomForTheHost(t *testing.T) {
	cpus, memMB := builderShare(10, 16*1024)
	if cpus >= 10 || cpus < 2 {
		t.Errorf("cpus = %d, want a share below the host's 10 and at least 2", cpus)
	}
	if memMB >= 16*1024 || memMB < 2048 {
		t.Errorf("memoryMB = %d, want a share below the host's 16384 and at least 2048", memMB)
	}
}

// A small machine must not end up worse off than Apple's own defaults.
func TestBuilderShare_NeverGoesBelowTheDefaults(t *testing.T) {
	cpus, memMB := builderShare(2, 4*1024)
	if cpus < 2 {
		t.Errorf("cpus = %d, must not drop under the CLI default of 2", cpus)
	}
	if memMB < 2048 {
		t.Errorf("memoryMB = %d, must not drop under the CLI default of 2048", memMB)
	}
}

// Apple's builder defaults to 2 CPUs and 2048 MB regardless of the host. On a
// 10-core / 16 GB Mac that is what a devcontainer feature's `apt-get install`
// and `npm ci` are squeezed through, and those installs are the bulk of a cold
// provisioning run.
//
// The sizing belongs to the BUILDER, not to the build: `container build`
// accepts --cpus/--memory and then ignores them when the builder container is
// already up. Verified on 1.2.0 — `container build --cpus 6` against a running
// 2-CPU builder still reported nproc=3 and `builder status` still read CPUS 2;
// recreating the builder at that size reported nproc=7. So this asserts on the
// builder lifecycle, which is where the setting actually lands.
func TestAppleBuilder_ResizesAnUndersizedBuilder(t *testing.T) {
	bin, rec := fakeContainerCLI(t, "2", "2048")
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default(), cpus: 6, memoryMB: 8192}

	b.ensureBuilderSized(context.Background())

	argv := appleArgv(t, rec)
	if !strings.Contains(argv, "builder delete --force") {
		t.Errorf("an undersized builder must be replaced, argv:\n%s", argv)
	}
	if !strings.Contains(argv, "builder start --cpus 6 --memory 8192m") {
		t.Errorf("the replacement must carry the sizing, argv:\n%s", argv)
	}
}

// Recreating the builder throws away its layer cache, so a builder that is
// already big enough must be left alone.
func TestAppleBuilder_LeavesASufficientBuilderAlone(t *testing.T) {
	bin, rec := fakeContainerCLI(t, "8", "16384")
	b := &AppleContainerBuilder{bin: bin, logger: slog.Default(), cpus: 6, memoryMB: 8192}

	b.ensureBuilderSized(context.Background())

	if strings.Contains(appleArgv(t, rec), "builder delete") {
		t.Error("a big-enough builder must keep its cache")
	}
}

// fakeContainerCLI stands in for `container`, reporting a builder of the given
// size and recording every argv it is called with.
func fakeContainerCLI(t *testing.T, cpus, memMB string) (bin, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "container")
	script := `#!/bin/sh
echo "$@" >> ` + recordPath + `
if [ "$1" = "builder" ] && [ "$2" = "status" ]; then
  echo "ID        IMAGE  STATE    IP  CPUS  MEMORY"
  echo "buildkit  img    running  ip  ` + cpus + `     ` + memMB + ` MB"
fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, recordPath
}

// Probing the image store during the wedge does not work: a stuck
// `container build` blocks `container image inspect` too — the apiserver
// serialises — so the probe answered "no image" for a build whose image was
// already there, and the run waited out the full idle timeout anyway (measured
// 2026-08-06: export finished 12:56:23, watchdog gave up 13:04:54).
//
// The build's own output is the reliable signal. BuildKit announces the export,
// and once that step reports DONE the image is written; anything after is the
// CLI failing to exit.
func TestAppleBuilder_StopsShortlyAfterTheExportCompletes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\n" +
		"echo '#18 exporting to oci image format'\n" +
		"echo '#18 sending tarball 26.3s done'\n" +
		"echo '#18 DONE 61.3s'\n" +
		"sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{
		bin:         bin,
		logger:      slog.Default(),
		idleTimeout: time.Hour,              // the long fallback must NOT be what fires
		settleDelay: 200 * time.Millisecond, // grace after the export
	}

	start := time.Now()
	err := b.Build(context.Background(), dir, "crewship-cache:exported", nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a build that finished its export must succeed, got %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("waited %s — the export marker was not acted on", elapsed)
	}
}

// Without an export the build never produced anything, so silence is a failure
// and only the long fallback may end it.
func TestAppleBuilder_SilenceBeforeAnyExportIsStillAFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "container")
	script := "#!/bin/sh\necho '#3 [1/5] RUN apt-get install'\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &AppleContainerBuilder{
		bin:         bin,
		logger:      slog.Default(),
		idleTimeout: 600 * time.Millisecond,
		settleDelay: 100 * time.Millisecond,
	}

	if err := b.Build(context.Background(), dir, "crewship-cache:noexport", nil); err == nil {
		t.Fatal("silence with no export must remain a failure")
	}
}
