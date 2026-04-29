export interface SuggestionPack {
  empty: string[]
  followUps: string[]
}

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

export function getSuggestions(role?: string | null): SuggestionPack {
  if (!role) return ROLE_PACKS.default
  const key = role.toLowerCase().replaceAll(" ", "_")
  return ROLE_PACKS[key] ?? ROLE_PACKS.default
}
