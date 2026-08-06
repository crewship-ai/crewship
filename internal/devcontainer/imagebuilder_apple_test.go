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
