package docker

import (
	"log/slog"
	"strconv"
	"strings"
)

// Gap is one control the crew HostConfig asks for that a particular runtime is
// known not to deliver. Reported, never enforced: the product is meant to run on
// every platform it can, so a runtime that cannot apply a setting says so rather
// than refusing the crew (internal/provider/capability.go makes the same
// argument for per-crew CrewConfig fields).
//
// These are not CrewConfig fields — nobody configured them — so they cannot ride
// CrewConfigSupport. They are hardening the provider applies on its own, and the
// operator has no way to discover a silent drop except by being told.
//
// Exported, with JSON tags, because being told at startup is not being told
// (#1672): the WARN in logRuntimeGaps is gone by the time an operator wonders
// why their agents forget things. GET /api/v1/system/runtime carries this
// verbatim on the `in_use` entry, `crewship system info` prints it, and
// `crewship doctor` raises it as an advisory.
type Gap struct {
	Control string `json:"control"`
	Detail  string `json:"detail"`
}

// KnownRuntimeGaps returns what the detected runtime will not honour.
//
// Every entry is measured by the runtime-conformance harness
// (runtime_conformance_test.go) against a real daemon, never inferred from
// release notes. An entry here means "we ran it and watched it not happen".
//
// A zero DetectResult — which is what the Apple provider reports, having no
// Docker daemon to name — yields nothing rather than a wrong answer.
func KnownRuntimeGaps(d DetectResult) []Gap {
	if !strings.EqualFold(strings.TrimSpace(d.Runtime), "podman") {
		return nil
	}
	var gaps []Gap
	// GroupAdd with bare numeric GIDs that have no /etc/group entry. Honoured on
	// podman 6.0.2 (verified: id reports groups=1001,1002); silently reduced to
	// the primary gid on podman 4.9.3, rootful and rootless alike.
	//
	// The cost is specific rather than theoretical: the crew's .memory subtrees
	// are chgrp'd to 1002 and made setgid 2775, so gid 1002 is what lets an
	// agent participate in crew-shared memory at all. Without it those reads
	// fail with EACCES and the agent looks like it has forgotten things.
	if major := majorVersion(d.Version); major > 0 && major < 5 {
		gaps = append(gaps, Gap{
			Control: "GroupAdd",
			Detail: "podman " + d.Version + " drops supplementary GIDs that have no /etc/group entry; " +
				"agents will not hold gid 1002 and crew-shared memory reads will fail with EACCES. " +
				"Fixed in podman 5; upgrading is the only remedy — the GID cannot be delivered any other way through the compat API",
		})
	}
	return gaps
}

// majorVersion parses the leading integer of a version string, returning 0 when
// there is nothing to parse. Deliberately lenient: a version this cannot read
// yields no gap report rather than a wrong one, because the report is advice
// and a wrong warning is worse than a missing one.
func majorVersion(v string) int {
	v = strings.TrimSpace(v)
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

// logRuntimeGaps states, once at startup, what this runtime will not do.
//
// It was the ONLY place an operator could learn it, which is the half of #1672
// that shipped first and the reason the rest exists: a warning emitted once at
// boot is unavailable to anyone debugging hours later. It stays because the log
// is the one surface that works with no client, no auth and no reachable API —
// it is now the first report rather than the only one.
func logRuntimeGaps(logger *slog.Logger, d DetectResult) {
	for _, g := range KnownRuntimeGaps(d) {
		logger.Warn("container runtime will not honour a crew hardening control",
			"runtime", d.Runtime, "version", d.Version, "control", g.Control, "detail", g.Detail)
	}
}

// ConformanceVerdict is what the conformance harness should do about one
// measured control, once the known-gap registry has had its say.
type ConformanceVerdict string

const (
	// ConformanceHonoured — the runtime delivered the control. Nothing to do.
	ConformanceHonoured ConformanceVerdict = "honoured"
	// ConformanceRegression — the runtime dropped a control nobody has
	// recorded as droppable here. This is the case the harness exists to
	// catch, and the only one that should redden a build.
	ConformanceRegression ConformanceVerdict = "regression"
	// ConformanceKnownGap — the runtime dropped a control this registry
	// already documents as undeliverable on it. Expected, and reported in
	// full, but not a failure: the registry's own contract is that Crewship
	// runs on every platform it can and says what it cannot do there
	// (see the Gap doc comment). A build that fails on a gap we have already
	// written down and cannot fix teaches nobody anything; it only trains
	// people to ignore the job.
	ConformanceKnownGap ConformanceVerdict = "known-gap"
	// ConformanceStaleGap — the runtime DID deliver a control the registry
	// claims it drops. The registry is the thing that is wrong, and silence
	// here is how it rots: KnownRuntimeGaps feeds `crewship doctor`, the
	// /system/runtime payload and the startup WARN, so a stale entry tells
	// operators their agents cannot read memory when they can.
	ConformanceStaleGap ConformanceVerdict = "stale-gap"
)

// ClassifyConformance judges one measured control against the gaps recorded
// for the runtime under test.
//
// This is the join the harness was missing. KnownRuntimeGaps documents itself
// as "measured by the runtime-conformance harness against a real daemon", but
// the harness never read it back, so the two contradicted each other: the
// registry said podman below 5 cannot carry gid 1002 and upgrading is the only
// remedy, while the harness failed the build over exactly that, nightly, on a
// runner where it could not be otherwise. Both statements were right and the
// pair was useless.
//
// control is the registry's Control name for the probe ("GroupAdd"); a probe
// that tracks no registry control passes an empty string and is judged on
// honoured alone. The matching Gap is returned when one applies, so the caller
// can print the operator-facing detail rather than restating the field name.
func ClassifyConformance(control string, honoured bool, gaps []Gap) (ConformanceVerdict, Gap) {
	var match Gap
	found := false
	if control != "" {
		for _, g := range gaps {
			if strings.EqualFold(strings.TrimSpace(g.Control), strings.TrimSpace(control)) {
				match, found = g, true
				break
			}
		}
	}
	switch {
	case honoured && found:
		return ConformanceStaleGap, match
	case honoured:
		return ConformanceHonoured, Gap{}
	case found:
		return ConformanceKnownGap, match
	default:
		return ConformanceRegression, Gap{}
	}
}
