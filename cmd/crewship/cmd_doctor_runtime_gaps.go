//go:build !clionly

package main

// Container-runtime gap check for `crewship doctor`.
//
// The five runtimes Crewship drives are not equivalent, and the ways they
// differ are measured rather than assumed: internal/provider/docker's
// conformance harness runs the product's own container config against a live
// daemon and records what each one silently declines to apply (#1672/#1673).
//
// Until this check existed, that measurement reached an operator through
// exactly one channel — a WARN the server emits once at startup. Anyone
// debugging later, or running doctor precisely because something is wrong, saw
// nothing. The failure the table currently describes is the worst possible
// shape for that: podman below 5 drops the supplementary GID that grants access
// to the crew's shared memory, so the agent reads nothing and presents as one
// that has forgotten things. No part of that symptom points at the runtime.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// runtimeGapsCheckName is the doctor row label. Adjacent to `container
// runtime`, and phrased so the two read as a pair: one says whether there is a
// runtime, this one says what that runtime will not do.
const runtimeGapsCheckName = "container runtime gaps"

// runCheckRuntimeGaps wires the live probe into the testable helper.
//
// It re-probes rather than reusing checkContainerRuntime's DetectResult.
// Detection is a socket dial against candidates that either answer immediately
// or fail immediately — single-digit milliseconds on a one-shot CLI — and the
// alternative is threading state between two entries in a probe list whose
// order would then be load-bearing. A cheap second call beats a hidden
// ordering contract.
func runCheckRuntimeGaps(ctx context.Context) checkResult {
	d, err := docker.Detect(ctx)
	if err != nil || d == nil {
		// A nil result with a nil error is not a shape Detect produces, but the
		// alternative to handling it is a panic inside a diagnostic command —
		// the one place a crash is least excusable.
		return checkRuntimeGaps(docker.DetectResult{}, errNoDockerRuntimeForGaps(err))
	}
	return checkRuntimeGaps(*d, nil)
}

// errNoDockerRuntimeForGaps keeps the "nothing answered" branch honest when
// Detect reports neither a result nor a reason.
func errNoDockerRuntimeForGaps(err error) error {
	if err != nil {
		return err
	}
	return errors.New("no Docker-API runtime detected")
}

// checkRuntimeGaps maps a detection result to a doctor row.
//
// WARN, never FAIL. A gap means "this runtime has known limitations", not
// "your install is broken" — crews start, crews run, and every other control is
// applied. Exiting non-zero over it would put a hard failure on a working host,
// and doctor's documented contract is that FAIL means Crewship cannot start.
// It would also train people to ignore the command, which is the same way the
// startup WARN this supplements came to be missed.
func checkRuntimeGaps(d docker.DetectResult, detectErr error) checkResult {
	if detectErr != nil {
		// INFO, not FAIL: the `container runtime` check immediately above
		// already reports a host with no runtime, and doctor must not count one
		// fact twice — two FAILs read as two problems. Apple Containers lands
		// here too, correctly: it is not a Docker-API daemon and the gap table
		// has no entry for it.
		return checkResult{
			name:   runtimeGapsCheckName,
			status: "INFO",
			detail: "no Docker-API runtime answered — skipped (see 'container runtime')",
		}
	}

	gaps := docker.KnownRuntimeGaps(d)
	if len(gaps) == 0 {
		// A clean row rather than no row. "We looked and there is nothing" is a
		// different statement from "we did not look", and a check that vanishes
		// when it has nothing to say is one nobody can rely on having run.
		return checkResult{
			name:   runtimeGapsCheckName,
			status: "PASS",
			detail: fmt.Sprintf("%s %s honours every crew hardening control", d.Runtime, d.Version),
		}
	}

	controls := make([]string, 0, len(gaps))
	details := make([]string, 0, len(gaps))
	for _, g := range gaps {
		controls = append(controls, g.Control)
		details = append(details, g.Detail)
	}
	return checkResult{
		name:   runtimeGapsCheckName,
		status: "WARN",
		detail: fmt.Sprintf("%s %s will not honour: %s", d.Runtime, d.Version, strings.Join(controls, ", ")),
		// The consequence goes in the hint, where doctor already puts the
		// "so what do I do" line. The control name alone is not actionable:
		// an operator cannot get from "GroupAdd" to the EACCES they are seeing.
		hint: strings.Join(details, " · "),
	}
}
