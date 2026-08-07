package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// The CLI names container runtimes in prose in two places: `system info`'s
// "nothing answered" hint and `doctor`'s "none installed" detail. Both are
// printed at exactly the moment the reader has no runtime working and is
// deciding what to install or start — which is when a false name does the most
// damage.
//
// containerd/nerdctl was named in both long after #1687 proved it can never
// answer: containerd serves its own gRPC API over HTTP/2, the moby client this
// product is built on speaks the Docker REST API over HTTP/1.1, and nothing
// bridges that. #1688 removed the candidate from the detector and the prose
// stayed, because nothing tied them together.
//
// So this asserts the VALUE, not the shape, against the detector's own
// vocabulary: every runtime named must be one Crewship can drive, and every
// runtime Crewship can drive must be named.
func TestRuntimeProse_NamesOnlyRuntimesTheDetectorCanDrive(t *testing.T) {
	t.Parallel()

	vocab := drivableRuntimeVocabulary(t)

	surfaces := map[string]string{
		"system info no-runtime hint": noRuntimeHint(),
		"doctor no-runtime detail":    "no container runtime installed (" + strings.Join(probedRuntimeNames, ", ") + ")",
	}

	for surface, text := range surfaces {
		lower := strings.ToLower(text)

		// Nothing Crewship cannot drive may be named. The forbidden set is
		// filtered through the live vocabulary, so the day a label is genuinely
		// added to the detector this stops objecting to it on its own.
		for _, name := range []string{"containerd", "nerdctl", "cri-o", "finch"} {
			if inVocabulary(name, vocab) {
				continue
			}
			if strings.Contains(lower, name) {
				t.Errorf("%s names %q, which the detector can never produce (vocabulary: %v) — "+
					"the reader is told to reach for a runtime Crewship cannot drive:\n  %s",
					surface, name, vocab, text)
			}
		}

		// And everything it CAN drive must be named, or dropping a false entry
		// quietly turns into dropping a true one.
		for _, label := range vocab {
			if !strings.Contains(lower, label) {
				t.Errorf("%s does not name %q, a runtime Crewship drives — an operator running it "+
					"reads this as 'not supported':\n  %s", surface, label, text)
			}
		}
	}
}

// drivableRuntimeVocabulary is every runtime the product can actually drive:
// the Docker detector's own label set plus Apple Containers, which is not a
// Docker-API daemon and reaches these surfaces from internal/provider/apple.
func drivableRuntimeVocabulary(t *testing.T) []string {
	t.Helper()
	labels := docker.RuntimeLabels()
	if len(labels) < 4 {
		// The detector's candidate list moved and this test would otherwise
		// pass by asserting almost nothing — the exact failure it guards.
		t.Fatalf("docker.RuntimeLabels() returned only %v — the detector's candidate list moved", labels)
	}
	return append(append([]string{}, labels...), "apple")
}

func inVocabulary(name string, vocab []string) bool {
	for _, label := range vocab {
		if label == name {
			return true
		}
	}
	return false
}
