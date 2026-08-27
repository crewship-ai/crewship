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
