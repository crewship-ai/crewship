/**
 * What a file in an agent's tree IS, from the reader's point of view.
 *
 * The Files panel lists an agent's whole storage namespace, and most of what
 * is in there was put there by us, not by the agent: five copies of the system
 * prompt under five different names (AGENTS.md, CLAUDE.md, GEMINI.md,
 * .cursor/rules/crewship.md, .factory/AGENTS.md — see
 * orchestrator/cli_adapter.go writeCanonicalMemoryFiles), one skill body per
 * assigned skill under five discovery roots, and a per-CLI MCP config. So the
 * first thing a customer sees when they open Files is a folder called
 * `.opencode`, and the three biggest files in the list are the same 40 KB
 * document three times.
 *
 * None of that is a secret and none of it is deleted — `Show internals` puts
 * it back. It is simply not the answer to "what has this agent made for me".
 */

export type FileScope =
  /** The agent's own work. What the panel is for. */
  | "created"
  /** Files WE sent the agent — chat attachments. Its inbox, effectively. */
  | "shared"
  /** Crewship's own scaffolding: prompt copies, skill bodies, CLI configs. */
  | "plumbing"

/**
 * Directory roots Crewship writes into an agent's namespace.
 *
 * Kept as a list of ROOTS rather than a list of files because each of these
 * carries a whole subtree — `.claude/skills/<slug>/SKILL.md` for every
 * assigned skill — and enumerating leaves would go stale the first time a
 * skill is assigned.
 */
const PLUMBING_DIRS = [
  ".agents",
  ".claude",
  ".codex",
  ".cursor",
  ".factory",
  ".gemini",
  ".opencode",
]

/** Files Crewship writes at the root of the namespace. */
const PLUMBING_FILES = [
  "AGENTS.md",
  "CLAUDE.md",
  "GEMINI.md",
  "opencode.json",
  ".mcp.json",
]

/** Where chat attachments land — proxy_attachments.go writes here. */
const SHARED_DIR = "attachments"

/**
 * Classify one entry by its path RELATIVE to the agent's namespace.
 *
 * `relPath` is what the tree shows, not the storage key: the caller strips
 * `<crewId>/<slug>/` (or `<crewId>/`) first. Passing a full storage key would
 * classify everything as "created", because none of the markers above would
 * sit at position zero.
 */
export function classifyAgentFile(relPath: string): FileScope {
  const clean = relPath.replace(/^\/+/, "")
  if (clean === "") return "created"

  const [head] = clean.split("/")

  if (head === SHARED_DIR) return "shared"
  if (PLUMBING_DIRS.includes(head)) return "plumbing"
  // A plumbing NAME only counts at the root. An agent that writes its own
  // `docs/CLAUDE.md` has made something, and hiding it would be the panel
  // lying about the agent's work.
  if (!clean.includes("/") && PLUMBING_FILES.includes(head)) return "plumbing"

  return "created"
}

/**
 * The path a classifier can read, given a storage key and the agent it
 * belongs to. Returns the key unchanged when it carries neither prefix, which
 * is what a relative path from an older caller looks like.
 */
export function relativeToAgent(
  storagePath: string,
  crewId: string | null | undefined,
  slug: string | null | undefined,
): string {
  if (crewId && slug) {
    const agentPrefix = `${crewId}/${slug}/`
    if (storagePath.startsWith(agentPrefix)) return storagePath.slice(agentPrefix.length)
  }
  if (crewId) {
    const crewPrefix = `${crewId}/`
    if (storagePath.startsWith(crewPrefix)) return storagePath.slice(crewPrefix.length)
  }
  return storagePath
}
