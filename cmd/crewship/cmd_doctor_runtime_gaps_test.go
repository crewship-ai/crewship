//go:build !clionly

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// `crewship doctor` is the "is everything OK before I file an issue?" surface,
// and a runtime that silently drops a crew hardening control is exactly the
// thing somebody is about to file an issue about — under a symptom (agents
// forgetting things) that names neither the runtime nor the control (#1673).
//
// Advisory, and deliberately so. A gap is "this runtime has known limitations",
// not "your install is broken": crews still start, still run, and every other
// control is applied. Failing doctor over it would put a red FAIL on a working
// host, and a checker that fails on working hosts is one people stop reading —
// which is precisely how the startup WARN this replaces got missed.
func TestCheckRuntimeGaps_AdvisoryNotFailure(t *testing.T) {
	t.Parallel()

	got := checkRuntimeGaps(docker.DetectResult{Runtime: "podman", Version: "4.9.3"}, nil)

	if got.status != "WARN" {
		t.Errorf("status = %q, want WARN — a known limitation is not a broken install", got.status)
	}
	if got.status == "FAIL" {
		t.Errorf("a runtime gap failed doctor; crews run fine on podman 4.9.3, they just lose one control")
	}
	// The control so it can be looked up, the version so the operator knows
	// which of their hosts, and the consequence because "GroupAdd is not
	// honoured" connects to nothing they can observe.
	both := got.detail + " " + got.hint
	for _, want := range []string{"GroupAdd", "4.9.3", "crew-shared memory"} {
		if !strings.Contains(both, want) {
			t.Errorf("doctor row omits %q, so it cannot be acted on: detail=%q hint=%q", want, got.detail, got.hint)
		}
	}
}

// A clean runtime gets a clean row rather than silence — "we looked and there
// is nothing" is a different statement from "we did not look", and doctor's
// row-per-check contract means a check that sometimes vanishes is a check
// nobody can rely on being run.
func TestCheckRuntimeGaps_PassesOnARuntimeThatHonoursEverything(t *testing.T) {
	t.Parallel()

	for _, d := range []docker.DetectResult{
		{Runtime: "docker", Version: "28.0.4"},
		{Runtime: "podman", Version: "6.0.2"},
		{Runtime: "orbstack", Version: "29.4.0"},
	} {
		got := checkRuntimeGaps(d, nil)
		if got.status != "PASS" {
			t.Errorf("%s %s: status = %q, want PASS", d.Runtime, d.Version, got.status)
		}
		if !strings.Contains(got.detail, d.Runtime) {
			t.Errorf("%s %s: detail does not name the runtime it cleared: %q", d.Runtime, d.Version, got.detail)
		}
	}
}

// No Docker-API daemon answered: INFO, not FAIL. The `container runtime` check
// immediately above already reports that, and doctor must not count the same
// fact twice — an operator reading two FAILs looks for two problems.
func TestCheckRuntimeGaps_InfoWhenNoDockerRuntimeAnswered(t *testing.T) {
	t.Parallel()

	got := checkRuntimeGaps(docker.DetectResult{}, errors.New("no Docker-compatible runtime found"))

	if got.status != "INFO" {
		t.Errorf("status = %q, want INFO — the container runtime check owns that finding", got.status)
	}
}

// The gap table is the docker provider's, not a copy of it kept here. If a
// future entry lands in docker.KnownRuntimeGaps, doctor must report it without
// anyone remembering to update this command.
func TestCheckRuntimeGaps_ReadsTheProviderTable(t *testing.T) {
	t.Parallel()

	d := docker.DetectResult{Runtime: "podman", Version: "4.9.3"}
	want := docker.KnownRuntimeGaps(d)
	if len(want) == 0 {
		t.Fatal("fixture no longer has a gap; pick a runtime/version that does")
	}
	got := checkRuntimeGaps(d, nil)
	for _, g := range want {
		if !strings.Contains(got.detail+" "+got.hint, g.Control) {
			t.Errorf("provider reports gap %q and doctor does not mention it", g.Control)
		}
	}
}
