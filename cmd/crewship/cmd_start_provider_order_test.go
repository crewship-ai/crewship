//go:build !clionly

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/provider"
)

// stubContainerProvider satisfies provider.ContainerProvider by embedding it.
// None of its methods are called here — the auto-detection tests only care
// which candidate was selected, so the embedded nil interface is never
// dereferenced.
type stubContainerProvider struct {
	provider.ContainerProvider
	name string
}

// orderCaptureLogger returns a logger that records everything at Debug and
// above into the returned buffer, so a test can assert on what the operator
// is told at startup.
func orderCaptureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func candidateNames(candidates []containerProviderCandidate) []string {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.name)
	}
	return names
}

// TestAutoContainerCandidates_DockerBeforeApple pins the order
// `container.provider: auto` probes runtimes in. Docker must come first: every
// crew isolation control (CapDrop ALL, no-new-privileges, the /secrets tmpfs,
// noexec mounts, the core-dump ulimit) and the whole `restricted` egress fence
// exist on the Docker path only, so on a host with both runtimes installed
// `auto` has to land on the hardened one (#1647).
func TestAutoContainerCandidates_DockerBeforeApple(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Container.Provider = "auto"

	got := candidateNames(autoContainerCandidates(context.Background(), cfg, nil, slog.New(slog.NewTextHandler(os.Stderr, nil))))
	want := []string{"docker", "apple"}

	if len(got) != len(want) {
		t.Fatalf("auto candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("auto candidate order = %v, want %v (Docker must be probed before Apple Containers — it is the only provider that applies the crew isolation controls)", got, want)
		}
	}
}

// TestAutoContainerCandidates_ConstructorsAreLazy guards the mechanism the
// order test relies on: building the candidate list must not touch a container
// runtime, otherwise probing order and construction order could diverge.
func TestAutoContainerCandidates_ConstructorsAreLazy(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	logger, buf := orderCaptureLogger()

	candidates := autoContainerCandidates(context.Background(), cfg, nil, logger)
	if len(candidates) == 0 {
		t.Fatal("expected at least one auto candidate")
	}
	for _, c := range candidates {
		if c.new == nil {
			t.Fatalf("candidate %q has no constructor", c.name)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("listing candidates must not probe a runtime, logged: %s", buf.String())
	}
}

// TestSelectAutoContainerProvider_PrefersEarlierCandidate pins first-match-wins:
// when several runtimes are usable, the earliest candidate is selected, never a
// later one.
func TestSelectAutoContainerProvider_PrefersEarlierCandidate(t *testing.T) {
	t.Parallel()

	first := &stubContainerProvider{name: "first"}
	second := &stubContainerProvider{name: "second"}
	candidates := []containerProviderCandidate{
		{name: "first", new: func() (provider.ContainerProvider, error) { return first, nil }},
		{name: "second", new: func() (provider.ContainerProvider, error) { return second, nil }},
	}

	logger, _ := orderCaptureLogger()
	got, name := selectAutoContainerProvider(candidates, logger)
	if name != "first" {
		t.Errorf("selected %q, want first", name)
	}
	if got != provider.ContainerProvider(first) {
		t.Errorf("selected provider = %#v, want the first candidate", got)
	}
}

// TestSelectAutoContainerProvider_FallsBackWhenEarlierUnavailable pins that an
// unusable earlier candidate does not strand the server without a runtime.
func TestSelectAutoContainerProvider_FallsBackWhenEarlierUnavailable(t *testing.T) {
	t.Parallel()

	second := &stubContainerProvider{name: "second"}
	candidates := []containerProviderCandidate{
		{name: "first", new: func() (provider.ContainerProvider, error) { return nil, errors.New("socket not found") }},
		{name: "second", new: func() (provider.ContainerProvider, error) { return second, nil }},
	}

	logger, buf := orderCaptureLogger()
	got, name := selectAutoContainerProvider(candidates, logger)
	if name != "second" {
		t.Fatalf("selected %q, want second", name)
	}
	if got != provider.ContainerProvider(second) {
		t.Errorf("selected provider = %#v, want the second candidate", got)
	}
	if !strings.Contains(buf.String(), "socket not found") {
		t.Errorf("the skipped candidate's error must be logged, got: %s", buf.String())
	}
}

// TestSelectAutoContainerProvider_NoneAvailable pins the all-fail path: no
// provider, no name, and a warning that names every runtime that was tried.
func TestSelectAutoContainerProvider_NoneAvailable(t *testing.T) {
	t.Parallel()

	candidates := []containerProviderCandidate{
		{name: "docker", new: func() (provider.ContainerProvider, error) { return nil, errors.New("no docker") }},
		{name: "apple", new: func() (provider.ContainerProvider, error) { return nil, errors.New("no apple") }},
	}

	logger, buf := orderCaptureLogger()
	got, name := selectAutoContainerProvider(candidates, logger)
	if got != nil || name != "" {
		t.Fatalf("got (%#v, %q), want (nil, \"\")", got, name)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("all-candidates-failed must warn, got: %s", out)
	}
	for _, want := range []string{"docker", "apple"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning must name the tried runtime %q, got: %s", want, out)
		}
	}
}

// TestSelectAutoContainerProvider_WarnsWhenAppleSelected pins the operator
// disclosure: if `auto` does land on Apple Containers (Docker absent), startup
// must say which controls are not being enforced, rather than reporting a
// successful detection that reads as equivalent to Docker (#1647).
func TestSelectAutoContainerProvider_WarnsWhenAppleSelected(t *testing.T) {
	t.Parallel()

	candidates := []containerProviderCandidate{
		{name: "docker", new: func() (provider.ContainerProvider, error) { return nil, errors.New("no docker") }},
		{name: "apple", new: func() (provider.ContainerProvider, error) {
			return &stubContainerProvider{name: "apple"}, nil
		}},
	}

	logger, buf := orderCaptureLogger()
	if _, name := selectAutoContainerProvider(candidates, logger); name != "apple" {
		t.Fatalf("selected %q, want apple", name)
	}
	assertAppleIsolationWarning(t, buf.String())
}

// TestWarnAppleProviderIsolationGap_NamesWhatIsNotEnforced keeps the disclosure
// specific. "Apple provider selected" on its own tells an operator nothing; the
// line has to name the controls that do not apply and the egress fence that is
// not enforced.
func TestWarnAppleProviderIsolationGap_NamesWhatIsNotEnforced(t *testing.T) {
	t.Parallel()

	logger, buf := orderCaptureLogger()
	warnAppleProviderIsolationGap(logger)
	assertAppleIsolationWarning(t, buf.String())
}

func assertAppleIsolationWarning(t *testing.T, out string) {
	t.Helper()

	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("apple selection must warn, got: %s", out)
	}
	lower := strings.ToLower(out)

	// Still not applied on this provider, and the Apple CLI has no flag for
	// some of them at all. These are why `auto` still prefers Docker, so the
	// disclosure has to keep naming them.
	for _, want := range []string{"cap_drop", "no-new-privileges", "noexec"} {
		if !strings.Contains(lower, want) {
			t.Errorf("warning must name %q as not applied, got: %s", want, out)
		}
	}

	// Applied since #1649. A disclosure that lists these as missing sends the
	// operator to Docker for controls they already have, and — worse in the
	// egress case — describes a crew as unfenced when it is fenced. The
	// warning must account for them on the applied side.
	for _, want := range []string{"tmpfs", "egress fence", "init process"} {
		if !strings.Contains(lower, want) {
			t.Errorf("warning must name %q among the controls that ARE applied, got: %s", want, out)
		}
	}
	if strings.Contains(lower, "not enforced") || strings.Contains(lower, "unfiltered") {
		t.Errorf("the egress fence is enforced on this provider now; the warning must not say otherwise, got: %s", out)
	}
}

// TestProvidersDocMatchesAutoDetectionOrder keeps the documented order and the
// implemented order from drifting apart — the exact failure #1647 reports,
// where the docs promised Docker first while the code tried Apple first.
func TestProvidersDocMatchesAutoDetectionOrder(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../docs/configuration/providers.mdx")
	if err != nil {
		t.Fatalf("read providers.mdx: %v", err)
	}
	doc := string(raw)

	names := candidateNames(autoContainerCandidates(context.Background(), &config.Config{}, nil, slog.New(slog.NewTextHandler(os.Stderr, nil))))
	docWords := map[string]string{"docker": "Docker", "apple": "Apple Containers"}

	prev := -1
	for _, name := range names {
		word, ok := docWords[name]
		if !ok {
			t.Fatalf("auto candidate %q has no documented name in providers.mdx — document it", name)
		}
		at := strings.Index(doc, "tries "+word)
		if at < 0 {
			at = strings.Index(doc, "falls back to "+word)
		}
		if at < 0 {
			t.Fatalf("providers.mdx never states where %q sits in the auto-detection order", word)
		}
		if at < prev {
			t.Fatalf("providers.mdx documents the auto-detection order the wrong way round; the code probes %v", names)
		}
		prev = at
	}
}
