package docker

import (
	"log/slog"
	"os"
	"time"
)

// startupMtimeSkew is the grace window before a sidecar older than the server
// binary is called stale. It must be wide enough to absorb the LEGITIMATE mtime
// spread between a server and a sidecar built in the SAME deploy but not
// necessarily back-to-back:
//
//   - dev.sh / `make` build them seconds apart (same shell run) — tiny spread.
//   - GoReleaser builds `crewship` (which //go:embed's the ~55MB Next.js export,
//     so it compiles+links measurably slower) and the small, embed-free
//     `crewship-sidecar` as INDEPENDENT `builds:` entries with no ordering or
//     archive mtime normalization, so within one release run their mtimes can
//     legitimately differ by minutes on a busy multi-arch matrix — with zero
//     actual staleness. A 60s window would cry wolf on a fresh release install
//     (the primary install.sh / Homebrew channel), the exact false alarm this
//     check must avoid.
//
// Genuine staleness — a sidecar that was never rebuilt for this deploy — is
// days-to-weeks old (the live dev1 case was a ~4-week-old pinned binary), so a
// 24h window cleanly separates real staleness from same-run build-order spread:
// no single release/deploy run takes anywhere near a day, and a genuinely stale
// sidecar is far older than one. (The authoritative signal for release builds
// is the injected build hash — see assertSidecarFreshAtStartup; wiring that
// hash into .goreleaser.yml so it is populated on release binaries too is a
// tracked follow-up. This mtime check is the coarse, deploy-agnostic backstop.)
const startupMtimeSkew = 24 * time.Hour

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
//
// It stats the host filesystem directly (os.Stat), mirroring onDiskSidecarHash
// in this package — there is no filesystem-provider abstraction here, and two
// boot-time stat calls neither need nor benefit from context cancellation.
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
	// Mirror warnStaleSidecarArtifact's nil-logger guard: this method promises
	// never to become a boot-time noise source, and a hand-constructed Provider
	// (as several tests build) could have a nil logger — Warn on it would panic
	// rather than fail open. New() always defaults it, so this only matters off
	// that path.
	log := p.logger
	if log == nil {
		log = slog.Default()
	}
	log.Warn("stale crewship-sidecar bind-mounted into crew containers: the configured sidecar predates this server build, so sidecar-side features shipped since (egress client, memory-auth chokepoint, #1387 token_fp / orphan-reap) may be running from an older binary — rebuild + recopy crewship-sidecar for this deploy (dev slots: it is co-located with the server binary; a stale CREWSHIP_SIDECAR_PATH pin in .env.local is ignored by dev.sh since #1402), then restart agents (#1390)",
		"sidecar_path", path,
		"sidecar_mtime", si.ModTime(),
		"server_binary", serverBinaryPath,
		"server_mtime", ei.ModTime(),
		"reason", reason,
	)
}
