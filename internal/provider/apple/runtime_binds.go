package apple

// Host paths the Apple Containers runtime can actually see (#1724).
//
// buildCreateArgs emits `-v <host>:<target>:ro` for crewship-sidecar and
// entrypoint.sh, and both are MANDATORY — it refuses to render a create without
// them, because the sidecar binary is the only way the egress fence for a
// `restricted` crew gets inside the container at all (#1648). The host side of
// those binds came straight from internal/config, which resolves them next to
// the crewship executable: /opt/homebrew/... on a Homebrew install,
// /usr/local/bin from install.sh.
//
// Every Apple container is a lightweight VM, so a bind source is a path the
// runtime resolves, not this process — the same distinction that made #1706 a
// release blocker on Docker/Colima. Whether Apple's VM sharing model lets an
// arbitrary host prefix through is not something this repo can answer without
// Apple hardware; what it can do is stop depending on the answer, by binding
// out of the ONE host subtree that already has to be reachable.
//
// That subtree is OutputBasePath: EnsureCrewRuntime creates /workspace,
// /output and /crew under it and binds all three, so a crew where the runtime
// cannot see the data dir does not start regardless. Staging the two artifacts
// there collapses two independent "this host path must be shared" requirements
// into the one that was already load-bearing, and on the default install
// ($HOME/.crewship/output) it needs no configuration at all.
//
// The mechanics live in internal/provider/runtimestage, shared verbatim with
// the docker provider rather than re-derived here — in particular the atomic,
// mtime-PRESERVING copy, which docker's assertSidecarFreshAtStartup depends on.

import (
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// stageRuntimeArtifacts returns cfg with SidecarBinaryPath and EntrypointPath
// repointed at copies under OutputBasePath/.runtime, so the mandatory binds
// live in the same host subtree as the crew data dirs.
//
// Unconditional, and best-effort: see runtimestage.Artifacts for why staging is
// not gated on a guess about which runtimes need it, and why a copy failure
// leaves the install paths in place instead of taking the provider down.
func stageRuntimeArtifacts(cfg Config, logger *slog.Logger) Config {
	cfg.SidecarBinaryPath, cfg.EntrypointPath = runtimestage.Artifacts(
		cfg.OutputBasePath, cfg.SidecarBinaryPath, cfg.EntrypointPath, logger)
	return cfg
}
