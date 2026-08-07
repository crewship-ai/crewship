package apple

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Provisioning builds a crew an image with its devcontainer features baked in,
// and this provider ran the configured runtime image instead — so on macOS the
// whole feature chain was built and then thrown away: no mise toolchains, no
// github-cli, no python, none of it reachable at exec time. The crew's own
// image has to win, exactly as it does on the Docker provider (#1779).
func TestCrewImage_PrefersTheProvisionedImage(t *testing.T) {
	p := &Provider{cfg: Config{RuntimeImage: "registry/agent-runtime:latest"}}

	got := p.crewImage(provider.CrewConfig{
		Image:       "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
		CachedImage: "crewship-cache:66e240493ae4",
	})
	if got != "crewship-cache:66e240493ae4" {
		t.Errorf("crewImage = %q, want the provisioned image", got)
	}
}

// An unprovisioned crew that names a base image should still get that, not the
// provider default — otherwise "which image am I running" has a third answer.
func TestCrewImage_FallsBackToTheRequestedImage(t *testing.T) {
	p := &Provider{cfg: Config{RuntimeImage: "registry/agent-runtime:latest"}}

	got := p.crewImage(provider.CrewConfig{Image: "debian:12"})
	if got != "debian:12" {
		t.Errorf("crewImage = %q, want the requested image", got)
	}
}

func TestCrewImage_FallsBackToTheProviderDefault(t *testing.T) {
	p := &Provider{cfg: Config{RuntimeImage: "registry/agent-runtime:latest"}}

	if got := p.crewImage(provider.CrewConfig{}); got != "registry/agent-runtime:latest" {
		t.Errorf("crewImage = %q, want the provider default", got)
	}
}

// The capability report must stop claiming the image is ignored once it is not:
// a report that understates what the provider does is still a false report, and
// this one told every reader that provisioned tools would be missing.
func TestUnsupportedCrewConfig_NoLongerDisownsTheProvisionedImage(t *testing.T) {
	p := &Provider{cfg: Config{RuntimeImage: "registry/agent-runtime:latest"}}

	s := p.UnsupportedCrewConfig(provider.CrewConfig{
		CachedImage: "crewship-cache:66e240493ae4",
	})
	for _, e := range append(append([]provider.DroppedField{}, s.Refused...), s.Degraded...) {
		if e.Field == "CachedImage" || e.Field == "Image" {
			t.Errorf("provider still reports %q as not honoured: %s", e.Field, e.Detail)
		}
	}
}

// crewImage picking the right image is only half of it: EnsureCrewRuntime
// resolved the image for the presence check and then handed
// p.cfg.RuntimeImage to `container create` anyway. Fixing one call site left
// the other wrong, and the crew still started from the provider default —
// which on a dev box is a registry that is not running, so create failed with
// "Connection refused" (#1779). This pins the argument vector, which is the
// only thing the runtime actually obeys.
func TestBuildCreateArgs_CarriesTheGivenImage(t *testing.T) {
	args, err := buildCreateArgs(createArgsInput{
		containerName:  "crewship-team-quality",
		image:          "crewship-cache:66e240493ae4",
		network:        "crewship-agents",
		cpus:           2,
		memoryMB:       4096,
		crewID:         "crew-1",
		workspacePath:  "/tmp/ws",
		outputPath:     "/tmp/out",
		crewPath:       "/tmp/crew",
		sidecarPath:    "/tmp/sidecar",
		entrypointPath: "/tmp/entrypoint.sh",
	})
	if err != nil {
		t.Fatalf("buildCreateArgs: %v", err)
	}
	last := args[len(args)-1]
	if last != "crewship-cache:66e240493ae4" {
		t.Errorf("create must run the crew's image, got %q (args: %v)", last, args)
	}
}
