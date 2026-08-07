package apple

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The provider reported ContainerEnv as dropped — "devcontainer containerEnv
// variables are not set on the container; only CREWSHIP_CREW_ID is" — while
// `container create` has supported --env all along and the provider already
// used it for CREWSHIP_CREW_ID.
//
// The report was also measurably false for a provisioned crew: the build path
// bakes containerEnv into the image as ENV, so PATH, PIPX_BIN_DIR and
// PYTHON_PATH were all present inside a live container. Understating what a
// provider does is still a false report — it tells every reader, the Builder UI
// and the agent's own status stream that its tools are unreachable when they
// are (#1690, #1779).
//
// Passing them explicitly fixes the unprovisioned case too, where nothing bakes
// them.
func TestBuildCreateArgs_PassesContainerEnv(t *testing.T) {
	args, err := buildCreateArgs(createArgsInput{
		containerName:  "crewship-team-quality",
		image:          "crewship-cache:abc",
		network:        "crewship-agents",
		cpus:           2,
		memoryMB:       4096,
		crewID:         "crew-1",
		workspacePath:  "/tmp/ws",
		outputPath:     "/tmp/out",
		crewPath:       "/tmp/crew",
		sidecarPath:    "/tmp/sidecar",
		entrypointPath: "/tmp/entrypoint.sh",
		containerEnv:   map[string]string{"PYTHON_PATH": "/usr/local/python/current", "PIPX_BIN_DIR": "/usr/local/py-utils/bin"},
	})
	if err != nil {
		t.Fatalf("buildCreateArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--env PYTHON_PATH=/usr/local/python/current",
		"--env PIPX_BIN_DIR=/usr/local/py-utils/bin",
		"--env CREWSHIP_CREW_ID=crew-1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args missing %q:\n%s", want, joined)
		}
	}
}

// Deterministic order, or the same crew config renders a different argument
// vector on every start and nothing downstream can be compared.
func TestBuildCreateArgs_ContainerEnvIsOrdered(t *testing.T) {
	in := createArgsInput{
		containerName: "c", image: "i", cpus: 1, memoryMB: 1024, crewID: "x",
		workspacePath: "/w", outputPath: "/o", crewPath: "/c",
		sidecarPath: "/s", entrypointPath: "/e",
		containerEnv: map[string]string{"B": "2", "A": "1", "C": "3"},
	}
	first, err := buildCreateArgs(in)
	if err != nil {
		t.Fatalf("buildCreateArgs: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := buildCreateArgs(in)
		if err != nil {
			t.Fatalf("buildCreateArgs: %v", err)
		}
		if strings.Join(again, " ") != strings.Join(first, " ") {
			t.Fatalf("argument vector is not stable across renders:\n%v\n%v", first, again)
		}
	}
}

// The capability report must stop claiming ContainerEnv is dropped.
func TestUnsupportedCrewConfig_NoLongerDisownsContainerEnv(t *testing.T) {
	p := &Provider{cfg: Config{RuntimeImage: "img"}}
	s := p.UnsupportedCrewConfig(provider.CrewConfig{
		ContainerEnv: map[string]string{"PATH": "/x"},
	})
	for _, e := range append(append([]provider.DroppedField{}, s.Refused...), s.Degraded...) {
		if e.Field == "ContainerEnv" {
			t.Errorf("provider still reports ContainerEnv as not honoured: %s", e.Detail)
		}
	}
}
