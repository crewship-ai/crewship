package docker

import (
	"os"
	"time"
)

// startupMtimeSkew is the grace window before a sidecar that is merely a little
// older than the server binary is called stale. A single deploy builds the
// server and the sidecar within the same run (dev.sh builds them back to back;
// a release Makefile makes build:go depend on build:sidecar), so their mtimes
// land within a minute of each other. Only a sidecar meaningfully older than
// the server indicates a missed rebuild/recopy for this deploy.
const startupMtimeSkew = 60 * time.Second

// sidecarStaleReason is the pure classifier behind the startup freshness
// assertion (#1390). It returns a short human reason when the configured
// sidecar binary predates the server binary it is deployed alongside by more
// than startupMtimeSkew — the signal that it was not rebuilt for this deploy
// and may be missing sidecar-side features shipped since (the live dev1 symptom
// was #1387 token_fp absent on /health, which makes orphan-reap detection
// inert). Returns "" — fail open — when either timestamp is unknown or the
// sidecar is at least as new as the server, so an ambiguous state never raises
// a false alarm.
func sidecarStaleReason(sidecarMtime, serverMtime time.Time) string {
	if sidecarMtime.IsZero() || serverMtime.IsZero() {
		return ""
	}
	if sidecarMtime.Before(serverMtime.Add(-startupMtimeSkew)) {
		return "configured sidecar binary is older than this server binary"
	}
	return ""
}

// assertSidecarFreshAtStartup surfaces a stale bind-mounted sidecar loudly at
// boot instead of leaving it silent until the first agent exec (#1390). It runs
// two independent checks against the configured CREWSHIP_SIDECAR_PATH:
//
//  1. The authoritative build-time hash comparison, if a hash was injected via
//     ldflags (release builds). ExpectedSidecarHash logs warnStaleSidecarArtifact
//     once when the on-disk binary diverges from the one baked into this server.
//     Calling it here makes that divergence visible at startup, not lazily on
//     the first agent run.
//
//  2. An mtime-predates check against this server executable. dev.sh builds the
//     server with a plain `go build` (no ldflags), so buildExpectedSidecarHash
//     is empty on dev slots and check (1) is blind there — exactly where #1390
//     was found. Comparing the sidecar's mtime to the server binary's catches a
//     stale artifact regardless of how it was deployed (dev.sh, infra
//     reconcile, manual copy).
//
// serverBinaryPath is this process's executable (os.Executable() at the call
// site); passing it in keeps the function testable. Every stat error fails open
// (no warning) — a freshness check must never itself become a boot-time noise
// source.
func (p *Provider) assertSidecarFreshAtStartup(serverBinaryPath string) {
	path := p.cfg.SidecarBinaryPath
	if path == "" {
		// No bind mount: crew containers use the sidecar baked into the image;
		// there is nothing on the host to compare.
		return
	}

	// (1) Authoritative hash comparison (release builds only — no-op when no
	// build hash was injected). Eagerly triggers warnStaleSidecarArtifact at
	// boot on an on-disk-vs-build divergence.
	_ = p.ExpectedSidecarHash()

	// (2) mtime-predates fallback (works without ldflags — the dev-slot case).
	si, err := os.Stat(path)
	if err != nil {
		return
	}
	ei, err := os.Stat(serverBinaryPath)
	if err != nil {
		return
	}
	reason := sidecarStaleReason(si.ModTime(), ei.ModTime())
	if reason == "" {
		return
	}
	p.logger.Warn("stale crewship-sidecar bind-mounted into crew containers: the configured sidecar predates this server build, so sidecar-side features shipped since (egress client, memory-auth chokepoint, #1387 token_fp / orphan-reap) may be running from an older binary — rebuild + recopy crewship-sidecar for this deploy (dev slots: it is co-located with the server binary; a stale CREWSHIP_SIDECAR_PATH pin in .env.local is ignored by dev.sh since #1402), then restart agents (#1390)",
		"sidecar_path", path,
		"sidecar_mtime", si.ModTime(),
		"server_binary", serverBinaryPath,
		"server_mtime", ei.ModTime(),
		"reason", reason,
	)
}
