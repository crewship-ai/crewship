package devcontainer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failingDockerCLI writes a stand-in `docker` onto PATH that emits `body` on
// STDERR (where BuildKit writes its progress and its errors) and then exits
// non-zero, exactly as a real failed build does.
//
// STDERR specifically: the builder merges the child's stderr into stdout, and a
// fake that printed to stdout would pass even if that merge were removed.
func failingDockerCLI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	payload := filepath.Join(dir, "buildkit-output")
	if err := os.WriteFile(payload, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat " + payload + " >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// builderWithCLI returns a builder wired to a stand-in docker binary and a
// discarded logger — the default-level logger is precisely the sink that swallows
// the build output today, so a test must not be able to read it from there.
func builderWithCLI(t *testing.T, bin string) *DockerBuildKitBuilder {
	t.Helper()
	b := NewDockerBuildKitBuilder(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.bin = bin
	return b
}

// buildkitFailure is the shape of a real BuildKit failure: layer progress,
// then the failing step's own output, then the solve error. The actionable
// part is at the END, which is why the retained window is a tail.
const buildkitFailure = `#1 [internal] load build definition from Dockerfile
#1 DONE 0.0s
#2 [internal] load metadata for docker.io/library/ubuntu:22.04
#2 DONE 0.4s
#5 [2/3] RUN apt-get install -y definitely-not-a-package
#5 0.412 Reading package lists...
#5 0.688 E: Unable to locate package definitely-not-a-package
#5 ERROR: process "/bin/sh -c apt-get install -y definitely-not-a-package" did not complete successfully: exit code: 100
ERROR: failed to solve: process "/bin/sh -c apt-get install -y definitely-not-a-package" did not complete successfully: exit code: 100`

// TestBuildFailureCarriesTheDaemonsOwnComplaint is #1730. `docker build`
// failing reported its exit status and discarded the only text that says WHY:
//
//	docker build failed for crewship-feat:3d097b41b405: exit status 1
//
// The output had been read and streamed — to onLog, which is nil for every
// caller that did not pass WithProgress, and to logger.Debug, which is below
// the default level. Neither reaches the RETURNED error, and the returned error
// is the only thing that survives to the operator, to CI, and to a test log.
//
// onLog is nil here ON PURPOSE: that is the configuration the failure is most
// often seen in, and the one where the reason vanished completely.
func TestBuildFailureCarriesTheDaemonsOwnComplaint(t *testing.T) {
	b := builderWithCLI(t, failingDockerCLI(t, buildkitFailure))

	err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:3d097b41b405", nil)
	if err == nil {
		t.Fatal("Build returned nil for a docker CLI that exited 1")
	}
	got := err.Error()

	// Still says what it always said.
	for _, want := range []string{"docker build failed", "crewship-feat:3d097b41b405", "exit status 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("error lost %q:\n%s", want, got)
		}
	}
	// And now says why. Both the failing step's own line and the solve
	// summary: the first names the command, the second names the cause.
	for _, want := range []string{
		"E: Unable to locate package definitely-not-a-package",
		"ERROR: failed to solve",
		"exit code: 100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error does not carry the daemon's own complaint %q — this is the whole of #1730:\n%s", want, got)
		}
	}
}

// TestBuildFailureTailIsBounded: a BuildKit failure can be preceded by
// megabytes of layer progress. The error keeps a bounded window of it, and the
// window is the TAIL — dropping the end to keep the beginning would keep the
// noise and discard the reason.
func TestBuildFailureTailIsBounded(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "#3 sha256:%064d extracting layer %d\n", i, i)
	}
	sb.WriteString(buildkitFailure)

	b := builderWithCLI(t, failingDockerCLI(t, sb.String()))
	err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:noisy", nil)
	if err == nil {
		t.Fatal("Build returned nil for a docker CLI that exited 1")
	}
	got := err.Error()

	if len(got) > 2*buildErrTailByteCap {
		t.Errorf("error is %d bytes; a multi-MB build log must not land whole in an error string", len(got))
	}
	if strings.Contains(got, "extracting layer 0 ") || strings.Contains(got, "extracting layer 1\n") {
		t.Errorf("error kept the HEAD of the log; the reason is at the tail:\n%s", got)
	}
	if !strings.Contains(got, "ERROR: failed to solve") {
		t.Errorf("bounding dropped the failing step's output — the one part that had to survive:\n%s", got)
	}
}

// TestBuildFailureTailIsScrubbed: the retained window is arbitrary build output
// and a Dockerfile/feature RUN can echo a credential (a build-arg, an env var,
// a curl -H). The error travels to CI logs, journal rows and WS payloads, so it
// goes through the same scrubber the durable failure event already uses (#829)
// rather than becoming a new, unfiltered path for the same secrets.
func TestBuildFailureTailIsScrubbed(t *testing.T) {
	const secret = "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	body := "#4 [2/3] RUN echo \"key=$ANTHROPIC_API_KEY\"\n" +
		"#4 0.101 key=" + secret + "\n" +
		"#4 0.102 Authorization: Bearer ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
		"ERROR: failed to solve: process did not complete successfully: exit code: 1"

	b := builderWithCLI(t, failingDockerCLI(t, body))
	err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:secrets", nil)
	if err == nil {
		t.Fatal("Build returned nil for a docker CLI that exited 1")
	}
	got := err.Error()

	if strings.Contains(got, secret) {
		t.Errorf("the build-output tail leaked a credential into the returned error:\n%s", got)
	}
	if strings.Contains(got, "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Errorf("the build-output tail leaked a bearer token into the returned error:\n%s", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("expected the scrubber's marker in place of the credential:\n%s", got)
	}
	// Scrubbing must not cost the diagnostic.
	if !strings.Contains(got, "ERROR: failed to solve") {
		t.Errorf("scrubbing dropped the reason:\n%s", got)
	}
}

// TestBuildFailureWithNoOutputSaysSo: a docker that dies without printing
// anything is itself a finding (the CLI never reached a daemon). Say that,
// rather than appending an empty block that reads like the output was cut.
func TestBuildFailureWithNoOutputSaysSo(t *testing.T) {
	b := builderWithCLI(t, failingDockerCLI(t, ""))

	err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:silent", nil)
	if err == nil {
		t.Fatal("Build returned nil for a docker CLI that exited 1")
	}
	if got := err.Error(); !strings.Contains(got, "no output") {
		t.Errorf("a silent failure must say it was silent, got:\n%s", got)
	}
}

// TestBuildSuccessStillReturnsNil: capturing a tail must not change the
// success path, and must not turn a clean build into an error.
func TestBuildSuccessStillReturnsNil(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '#1 DONE 0.0s' >&2\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := builderWithCLI(t, bin)

	var lines []string
	if err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:ok", func(l string) {
		lines = append(lines, l)
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// onLog still gets every line — the tail is additional, not a replacement.
	if len(lines) != 1 || lines[0] != "#1 DONE 0.0s" {
		t.Errorf("onLog no longer receives the stream: %q", lines)
	}
}
