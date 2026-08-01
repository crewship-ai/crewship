package kinds

// #1627 — the manifest kind rejected only negatives, so
// `spec.devcontainer.cpus: 0.005` passed `crewship apply` validate and was
// only ever caught by the Docker daemon at wake time ("Range of CPUs is
// from 0.01"), by which point every agent run for the crew was wedged.
//
// Validate must fail fast with the same range the API returns.

import (
	"strconv"
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
		{"docker floor exactly", Devcontainer{MemoryMB: crewMinContainerMemoryMB, CPUs: crewMinContainerCPUs}},
		{"ceiling exactly", Devcontainer{MemoryMB: crewMaxContainerMemoryMB, CPUs: crewMaxContainerCPUs}},
		{"ordinary values", Devcontainer{MemoryMB: 4096, CPUs: 2}},
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

// The manifest floor must be the daemon's floor, not a rounder number the
// API would then disagree with — a manifest that validates locally and then
// 400s on apply is worse than no local check.
func TestCrew_ContainerResourceBoundsMatchAPIWording(t *testing.T) {
	doc := makeCrewDoc()
	doc.Spec.Devcontainer = &Devcontainer{Image: doc.Spec.RuntimeImage, CPUs: 0.005}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		strconv.FormatFloat(crewMinContainerCPUs, 'g', -1, 64),
		strconv.FormatFloat(crewMaxContainerCPUs, 'g', -1, 64),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry bound %q", err.Error(), want)
		}
	}
}
