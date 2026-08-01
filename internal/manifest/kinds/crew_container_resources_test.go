package kinds

// #1627 — the manifest kind rejected only negatives, so
// `spec.devcontainer.cpus: 0.005` passed `crewship apply` validate and was
// only ever caught by the Docker daemon at wake time ("Range of CPUs is
// from 0.01"), by which point every agent run for the crew was wedged.
//
// Validate must fail fast with the same range the API returns.
//
// #1638: these bounds stay Docker's own, and that is a decision rather than
// an oversight. A crew sized between the daemon's minimum and what an agent
// actually needs (~2048 MiB) is undersized, but the operator may have meant
// it, so the server ACCEPTS it and returns a warning. That advisory floor is
// an instance setting (`runtime.agent_min_memory_mb`), which offline manifest
// validation cannot read — `crewship apply --dry-run` runs without a server.
// Hard-coding a copy of the default here would make `apply` reject manifests
// the server would have accepted, and would silently ignore an operator who
// moved the floor. So the manifest kind enforces only the bound that is
// universally true, and the advisory arrives from the API on apply.
//
// Bounds below are asserted against LITERALS. The previous version compared
// crewMinContainerMemoryMB against itself, which pins nothing — raising the
// constant to nonsense left the file green.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func TestCrew_Validate_ContainerResourcesOutOfRange(t *testing.T) {
	cases := []struct {
		name  string
		dc    Devcontainer
		field string
	}{
		{"cpus below the docker floor", Devcontainer{CPUs: 0.005}, "cpus"},
		{"cpus negative", Devcontainer{CPUs: -1}, "cpus"},
		{"cpus above the ceiling", Devcontainer{CPUs: 100000}, "cpus"},
		{"memory below the docker floor", Devcontainer{MemoryMB: 5}, "memory_mb"},
		{"memory negative", Devcontainer{MemoryMB: -512}, "memory_mb"},
		{"memory above the ceiling", Devcontainer{MemoryMB: 999999}, "memory_mb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeCrewDoc()
			dc := tc.dc
			dc.Image = doc.Spec.RuntimeImage
			doc.Spec.Devcontainer = &dc

			err := doc.Validate(internalapi.WorkspaceContext{})
			if err == nil {
				t.Fatalf("want a validation error for %s", tc.field)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.field) {
				t.Errorf("error %q does not name the offending field %q", msg, tc.field)
			}
			if !strings.Contains(msg, "between") {
				t.Errorf("error %q does not name the valid range", msg)
			}
		})
	}
}

func TestCrew_Validate_ContainerResourcesInRange(t *testing.T) {
	cases := []struct {
		name string
		dc   Devcontainer
	}{
		// 0 stays the "omitted / use the server default" sentinel — the
		// emit path already gates on `> 0`.
		{"both omitted", Devcontainer{}},
		{"docker's hard floor exactly", Devcontainer{MemoryMB: 6, CPUs: 0.01}},
		{"ceiling exactly", Devcontainer{MemoryMB: 262144, CPUs: 512}},
		{"ordinary values", Devcontainer{MemoryMB: 4096, CPUs: 2}},
		// Undersized but legal: the server warns, apply does not block.
		{"below the server's advisory floor", Devcontainer{MemoryMB: 1024, CPUs: 0.25}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := makeCrewDoc()
			dc := tc.dc
			dc.Image = doc.Spec.RuntimeImage
			doc.Spec.Devcontainer = &dc

			if err := doc.Validate(internalapi.WorkspaceContext{}); err != nil {
				t.Fatalf("Validate(%s) = %v, want nil", tc.name, err)
			}
		})
	}
}

// The manifest bounds must be the same numbers the API HARD-rejects on — a
// manifest that validates locally and then 400s on apply is worse than no
// local check, and one that rejects locally what the server would have
// accepted is worse still. internal/manifest/kinds must not import the server
// package, so the numbers are mirrored; these literals keep the two mirrors in
// step, and internal/api's TestCrewContainerBounds_HardIsDockerAdvisoryIsOneAgent
// pins the other side to the same values.
func TestCrew_ContainerResourceBoundsMatchAPI(t *testing.T) {
	if crewMinContainerMemoryMB != 6 {
		t.Errorf("crewMinContainerMemoryMB = %d, want 6 (api.dockerMinContainerMemoryMB)", crewMinContainerMemoryMB)
	}
	if crewMaxContainerMemoryMB != 262144 {
		t.Errorf("crewMaxContainerMemoryMB = %d, want 262144 (api.maxCrewContainerMemoryMB)", crewMaxContainerMemoryMB)
	}
	if crewMinContainerCPUs != 0.01 {
		t.Errorf("crewMinContainerCPUs = %v, want 0.01 (api.dockerMinContainerCPUs)", crewMinContainerCPUs)
	}
	if crewMaxContainerCPUs != 512 {
		t.Errorf("crewMaxContainerCPUs = %v, want 512 (api.maxCrewContainerCPUs)", crewMaxContainerCPUs)
	}

	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{Image: doc.Spec.RuntimeImage, CPUs: 0.005}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"0.01", "512"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry bound %q", err.Error(), want)
		}
	}
}
