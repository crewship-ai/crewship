import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, within } from "@testing-library/react"
import type { JournalEntry } from "@/lib/types/journal"
import type { AgentLookup, JournalLookupValue } from "@/hooks/use-journal-lookup"

// The rail now reads the workspace-wide lookup for avatars and names.
// Stub the hook, not the transport, so the card gets an exact table with
// no socket in the suite.
const state = vi.hoisted(() => ({
  lookup: null as unknown as JournalLookupValue,
}))

vi.mock("@/hooks/use-journal-lookup", () => ({
  useJournalLookup: () => state.lookup,
  JournalLookupProvider: ({ children }: { children: React.ReactNode }) => children,
}))

import { LogsStatsRail } from "@/components/features/logs/logs-stats-rail"

function lookupWith(agents: AgentLookup[]): JournalLookupValue {
  return {
    agents: new Map(agents.map((a) => [a.id, a])),
    crews: new Map(),
    missions: new Map(),
    loading: false,
    refresh: () => {},
  }
}

function entry(agentId: string, i: number): JournalEntry {
  return {
    id: `e${i}`,
    workspace_id: "ws_test",
    ts: new Date().toISOString(),
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    agent_id: agentId,
    summary: "exit 0",
    payload: {},
    refs: {},
  }
}

beforeEach(() => {
  state.lookup = lookupWith([
    {
      id: "agt_morgan",
      name: "Morgan",
      slug: "morgan",
      crew_id: null,
      avatar_seed: "morgan",
      avatar_style: null,
    },
  ])
})

describe("LogsStatsRail — Top agents", () => {
  function topAgentsCard(container: HTMLElement): HTMLElement {
    const heading = within(container).getByText("Top agents")
    // The card is the heading's parent block.
    return heading.parentElement as HTMLElement
  }

  it("renders the agent's avatar and resolved name, not a shortened uuid", () => {
    const { container } = render(
      <LogsStatsRail entries={[entry("agt_morgan", 1), entry("agt_morgan", 2)]} />,
    )
    const card = topAgentsCard(container)
    expect(within(card).getByText("Morgan")).toBeInTheDocument()
    const avatar = card.querySelector("img")
    expect(avatar).not.toBeNull()
    expect(avatar?.getAttribute("src") ?? "").toMatch(/^data:image\/svg\+xml/)
  })

  it("falls back to the scope-limited prop, then to a shortened id", () => {
    const { container } = render(
      <LogsStatsRail
        entries={[entry("agt_scoped_000000001", 1), entry("agt_unknown_00000001", 2)]}
        agentLookup={{ agt_scoped_000000001: "Scoped" }}
      />,
    )
    const card = topAgentsCard(container)
    expect(within(card).getByText("Scoped")).toBeInTheDocument()
    // Neither name resolves from the workspace lookup, so the unnamed one
    // shows a shortened id. Assert the elision explicitly — a loose
    // pattern would match the raw uuid too and pass without shortening.
    expect(within(card).getByText("agt_un…0001")).toBeInTheDocument()
    expect(within(card).queryByText("agt_unknown_00000001")).toBeNull()
  })
})
