package database

import "strings"

// The default devcontainer a crew gets when it has none of its own.
//
// Why this exists, and why it is a READ-time default rather than a value
// written at insert:
//
// Four INSERT INTO crews statements omit devcontainer_config entirely
// (crew_templates.go, services/onboarding.go, recipes.go, internal_status.go),
// so every crew they create stores NULL. A crew with NULL config is refused by
// the provisioning gate (crew_provisioning_jobs.go, ErrCrewNoDevcontainer),
// never gets an image built, and falls through EnsureCrewRuntime's
// CachedImage > Image > provider-default chain to bare debian:bookworm-slim —
// which has no agent CLI, no node, no ca-certificates and no uid 1001. The
// agent then dies with exit 127, "claude: No such file or directory",
// reproduced end to end through the CLI on 2026-08-22.
//
// Adding the value to those four INSERTs is the obvious fix and the wrong one.
// This codebase has already paid for that lesson twice: internal/crewstart
// exists because thirteen callers each assembled their own CrewConfig and three
// forgot CachedImage (#1717), and the very bug above is a fifth caller
// (internal_status.go) that a write-side fix would have to remember. A default
// applied where the config is READ cannot be missed by a creation path that
// does not exist yet.
//
// The regression itself dates to 2026-04-15 (8780f3c4, PR #154), which deleted
// the pre-provisioned ghcr.io/crewship-ai/agent-runtime image, switched the
// platform default to bare Debian, and added these columns with no backfill.
// Seed data got its own config six weeks later; the templates never did, which
// is why `./dev.sh seed` produces working crews and onboarding does not.

// DefaultCrewDevcontainerConfig is the effective devcontainer for a crew that
// declares none.
//
// Deliberately narrower than cmd/crewship/seeddata/builtin/crews.yaml, which
// installs five vendor CLIs. Only Claude Code is verified end to end against
// the adapter; the Cursor and Droid installers are `curl | bash` from two more
// domains behind `|| true`, so in a default they would cost every crew two
// connection attempts to fail silently, and cost an operator behind a proxy two
// more domains to allowlist for no benefit.
//
// Verified on 2026-08-22 by building this exact config and running the result:
// `claude --version` → 2.1.239 as uid 1001. The base image ships node, npm,
// git, curl, wget, bash and ca-certificates but NOT uid 1001 (it has `node` at
// 1000), which is why common-utils is not optional here — the runtime execs as
// 1001:1001 and would have nothing to exec as.
const DefaultCrewDevcontainerConfig = `{
  "image": "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
  "containerEnv": {
    "PATH": "/home/agent/.local/bin:/home/agent/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
  },
  "features": {
    "ghcr.io/devcontainers/features/common-utils:2": {
      "username": "agent",
      "uid": "1001",
      "gid": "1001",
      "installZsh": false,
      "upgradePackages": false
    },
    "ghcr.io/devcontainers-extra/features/claude-code:2": {}
  }
}`

// EffectiveCrewDevcontainerConfig returns the devcontainer config a crew should
// actually be provisioned with: its own if it has one, otherwise the default.
//
// This is the chokepoint. Every reader of crews.devcontainer_config must go
// through it rather than testing the column itself, so that a crew created by
// any path — including one added after this comment was written — provisions
// into a usable environment.
//
// stored is the column value; ok is false for SQL NULL. Whitespace-only is
// treated as absent: every existing reader already collapses NULL and "" to the
// same meaning (crew_provisioning_jobs.go, crew_runtime_config.go,
// agent_config.go all test `!Valid || String == ""`), and `crewship crew config
// --clear` writes NULL, so there is no established way to express "deliberately
// bare" that this would take away.
//
// NOTE for whoever adds one: if a genuinely bare crew becomes a supported
// choice, it needs its own explicit representation — a column, a sentinel, a
// crew kind. It must not be spelled "empty", because empty is what four
// creation paths produce by accident.
func EffectiveCrewDevcontainerConfig(stored string, ok bool) string {
	if !ok || strings.TrimSpace(stored) == "" {
		return DefaultCrewDevcontainerConfig
	}
	return stored
}

// CrewDevcontainerIsDefaulted reports whether a crew is running on the default
// rather than a config of its own. Callers surface this — a crew provisioning
// from a default the operator never chose should be visible, not silent.
func CrewDevcontainerIsDefaulted(stored string, ok bool) bool {
	return !ok || strings.TrimSpace(stored) == ""
}
