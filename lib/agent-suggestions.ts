export interface SuggestionPack {
  empty: string[]
  followUps: string[]
}

/** Mirrors maxSuggestedPrompts in internal/api/agents_suggested_prompts.go. */
export const MAX_SUGGESTED_PROMPTS = 8
/** Mirrors maxSuggestedPromptLength — characters, not bytes. */
export const MAX_SUGGESTED_PROMPT_LENGTH = 120

const ROLE_PACKS: Record<string, SuggestionPack> = {
  data_analyst: {
    empty: [
      "Explore the latest dataset",
      "Build a SQL report",
      "Find anomalies in last week's metrics",
      "Suggest a cohort analysis",
    ],
    followUps: [
      "Visualize the result",
      "Group by week",
      "Export to CSV",
    ],
  },
  research: {
    empty: [
      "Summarize the top 5 sources",
      "Compare two papers",
      "Build an outline",
      "Suggest counter-arguments",
    ],
    followUps: ["Cite sources", "Expand section 2", "Translate to Czech"],
  },
  engineering: {
    empty: [
      "Plan a refactor of the chat module",
      "Find dead code in /internal/api",
      "Add a missing test",
      "Audit dependencies",
    ],
    followUps: ["Open a PR", "Add tests", "Run benchmarks"],
  },
  default: {
    empty: [
      "Help me get started",
      "What can you do?",
      "Show me your skills",
      "Run a quick task",
    ],
    followUps: ["Tell me more", "Give me an example", "What's next?"],
  },
}

/**
 * Splits the stored `agents.suggested_prompts` column into chips.
 *
 * The server normalises on write (LF, trimmed, blank lines dropped, capped),
 * so this is defensive rather than corrective — but it is still the render
 * path for whatever a row happens to hold, including rows written before a
 * cap existed, so it trims and caps again rather than trusting the column.
 */
export function parseSuggestedPrompts(raw?: string | null): string[] {
  if (!raw) return []
  return raw
    .split(/\r\n|\r|\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0)
    .slice(0, MAX_SUGGESTED_PROMPTS)
}

/**
 * The chips shown under a chat.
 *
 * `agentPrompts` is the agent's own list (the raw column value); when it holds
 * anything, it wins. When it is null, empty, or only whitespace — which is
 * every agent that has not been configured — the role packs answer exactly as
 * they did before the column existed. That fallback is the requirement, not a
 * nicety: nothing may regress for an agent nobody has touched.
 *
 * Only the empty-state chips are per-agent. Follow-ups stay on the role pack:
 * they are conversational continuations ("Add tests", "Cite sources"), not
 * questions an owner would write for their agent, and one textarea is the
 * whole mechanism here.
 */
export function getSuggestions(role?: string | null, agentPrompts?: string | null): SuggestionPack {
  const pack = (() => {
    if (!role) return ROLE_PACKS.default
    const key = role.toLowerCase().replaceAll(" ", "_")
    return ROLE_PACKS[key] ?? ROLE_PACKS.default
  })()

  const own = parseSuggestedPrompts(agentPrompts)
  if (own.length === 0) return pack
  return { empty: own, followUps: pack.followUps }
}
