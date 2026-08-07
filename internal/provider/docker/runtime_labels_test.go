package docker

import (
	"strings"
	"testing"
)

// RuntimeLabels is what the CLI's runtime prose is pinned to (#1689), so it has
// to be the detector's real vocabulary rather than a hand-kept list: too small
// and honest surfaces start claiming a supported runtime is unsupported; too
// large and they advertise one that can never answer.
func TestRuntimeLabels(t *testing.T) {
	t.Parallel()

	got := RuntimeLabels()
	want := map[string]bool{"docker": true, "podman": true, "colima": true, "orbstack": true, "rancher": true}

	seen := map[string]bool{}
	for _, label := range got {
		if !want[label] {
			t.Errorf("RuntimeLabels() offers %q, which candidateSocketsFor does not attach to any socket", label)
		}
		if seen[label] {
			t.Errorf("RuntimeLabels() repeats %q", label)
		}
		seen[label] = true
	}
	for label := range want {
		if !seen[label] {
			t.Errorf("RuntimeLabels() is missing %q — a surface pinned to it will call that runtime unsupported", label)
		}
	}

	// Sorted, so callers rendering it produce stable output.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("RuntimeLabels() is not sorted: %v", got)
			break
		}
	}

	// The whole point: containerd speaks gRPC over HTTP/2 and can never answer
	// the moby client's HTTP/1.1 ping, so it is not in the candidate list and
	// must not leak into the vocabulary the prose is generated from (#1687).
	for _, label := range got {
		if strings.Contains(label, "containerd") || strings.Contains(label, "nerdctl") {
			t.Errorf("RuntimeLabels() includes %q — see containerdSocketPaths", label)
		}
	}
}

// The rootless-podman candidate is uid-dependent and Windows reports -1; the
// vocabulary must not depend on which uid happens to run the test.
func TestRuntimeLabelsIsUIDIndependent(t *testing.T) {
	t.Parallel()

	got := strings.Join(RuntimeLabels(), ",")
	if !strings.Contains(got, "podman") {
		t.Errorf("podman missing from %q — the rootless candidate is skipped for uid < 0 and the "+
			"machine/root candidates should still carry the label", got)
	}
}
