package preflight

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// The installed-runtime scan exists to tell a user "start what you already
// have". Every name it can produce is therefore a promise: start this and
// Crewship will drive it.
//
// `containerd (nerdctl)` was in the Linux list with the start hint
// `sudo systemctl start containerd`, and it was the worst kind of wrong — the
// daemon is *already running* on such a host, so the user follows the advice,
// sees no change, and has no reason to suspect the instruction could never
// have worked. containerd serves its own gRPC API over HTTP/2; the moby client
// this product is built on speaks the Docker REST API over HTTP/1.1; nothing
// bridges that on any version (#1687).
//
// This asserts the VALUE, not the shape: every candidate name must map to a
// label the detector can actually attach to a socket. That ties the list to
// docker.RuntimeLabels() rather than to a copy of itself, so a runtime added
// to or removed from the detector cannot drift this list apart from it again.
func TestInstalledCandidatesNameOnlyRuntimesCrewshipCanDrive(t *testing.T) {
	t.Parallel()

	vocab := drivableVocabulary(t)

	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, c := range installedCandidatesFor(goos) {
			if !namesADrivableRuntime(c.name, vocab) {
				t.Errorf("goos=%s offers %q (start hint %q), but no runtime label the detector can produce (%v) "+
					"appears in that name — the scan promises a runtime Crewship cannot drive, "+
					"and its start hint sends the user after a daemon that will not help",
					goos, c.name, c.startHint, vocab)
			}
		}
	}
}

// The same list must still cover the runtimes that DO work, or "drop the
// entry" quietly becomes "drop the coverage".
func TestInstalledCandidatesCoverTheRuntimesThatWork(t *testing.T) {
	t.Parallel()

	found := map[string]bool{}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, c := range installedCandidatesFor(goos) {
			for _, label := range drivableVocabulary(t) {
				if strings.Contains(strings.ToLower(c.name), label) {
					found[label] = true
				}
			}
		}
	}
	// Every Docker-API runtime plus Apple Containers must be scanned for
	// somewhere. rancher/orbstack/colima/apple are macOS-only in practice, so
	// this is a union across the three OS lists, not a per-OS assertion.
	for _, want := range drivableVocabulary(t) {
		if !found[want] {
			t.Errorf("no installed-runtime candidate on any OS names %q — a user who has it installed "+
				"but stopped is told to install a runtime they already have", want)
		}
	}
}

// A candidate with no start hint is a dead end in the guidance message, which
// renders "<name>:" followed by nothing.
func TestInstalledCandidatesAllCarryAStartHint(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, c := range installedCandidatesFor(goos) {
			if strings.TrimSpace(c.startHint) == "" {
				t.Errorf("goos=%s candidate %q has no start hint", goos, c.name)
			}
			if c.path == "" && c.binary == "" {
				t.Errorf("goos=%s candidate %q is detectable by neither path nor binary", goos, c.name)
			}
		}
	}
}

// drivableVocabulary is every runtime Crewship can actually drive: the Docker
// detector's own label set, plus Apple Containers, which reaches the same
// surfaces from internal/provider/apple.
func drivableVocabulary(t *testing.T) []string {
	t.Helper()
	labels := docker.RuntimeLabels()
	if len(labels) < 4 {
		// The detector's list moved and this test would otherwise pass by
		// asserting almost nothing — the failure mode it exists to prevent.
		t.Fatalf("docker.RuntimeLabels() returned only %v — the detector's candidate list moved", labels)
	}
	return append(append([]string{}, labels...), "apple")
}

// namesADrivableRuntime reports whether a human-facing candidate name ("Docker
// Desktop", "Apple Containers") is a rendering of a label the detector can
// produce ("docker", "apple"). Substring, because the display names carry the
// product suffix and the labels do not.
func namesADrivableRuntime(name string, vocab []string) bool {
	lower := strings.ToLower(name)
	for _, label := range vocab {
		if strings.Contains(lower, label) {
			return true
		}
	}
	return false
}
