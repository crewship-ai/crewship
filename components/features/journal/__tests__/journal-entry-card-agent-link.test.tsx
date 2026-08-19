import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({
  useRealtime: () => ({
    status: "connected",
    subscribe: () => () => {},
    subscribeChannel: () => () => {},
  }),
  useRealtimeEvent: () => undefined,
}))

const AGENT = {
  id: "ag_deadbeef",
  name: "Casey",
  slug: "casey",
  crew_id: null,
  avatar_seed: null,
  avatar_style: null,
}

const CREW = {
  id: "crew_1",
  name: "Quality",
  slug: "quality",
  color: null,
  icon: null,
}

vi.mock("@/hooks/use-journal-lookup", () => ({
  useJournalLookup: () => ({
    crews: new Map([[CREW.id, CREW]]),
    agents: new Map([[AGENT.id, AGENT]]),
    missions: new Map(),
    loading: false,
    refresh: () => {},
  }),
}))

import { JournalEntryCard } from "@/components/features/journal/journal-entry-card"
import type { JournalEntry } from "@/lib/types/journal"

// =============================================================================
// The journal's context chips. The crew chip has always been correct —
// /crews?crew=<slug> — and the agent chip right beside it pointed at
// /crews/agents/<id>, a route deleted with the redesign. Two chips, same row,
// one of them a 404, and the shape of the correct answer sitting next to it.
//
// The lookup already carries the slug (AgentLookup.slug), so the agent chip
// becomes /crews?agent=<slug>, matching its neighbour exactly.
// =============================================================================

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "j1",
    workspace_id: "ws_test",
    ts: new Date(Date.now() - 60_000).toISOString(),
    entry_type: "agent.run",
    severity: "info",
    actor_type: "agent",
    summary: "run finished",
    actor_id: AGENT.id,
    agent_id: AGENT.id,
    crew_id: CREW.id,
    ...overrides,
  } as JournalEntry
}

describe("JournalEntryCard context chips", () => {
  it("links the agent chip to the /crews canvas by slug", () => {
    render(<JournalEntryCard entry={entry()} />)
    expect(screen.getByRole("link", { name: /Casey/ })).toHaveAttribute(
      "href",
      "/crews?agent=casey",
    )
  })

  it("keeps the agent id out of the href", () => {
    render(<JournalEntryCard entry={entry()} />)
    expect(
      screen.getByRole("link", { name: /Casey/ }).getAttribute("href"),
    ).not.toContain(AGENT.id)
  })

  it("leaves the crew chip on its existing, working target", () => {
    render(<JournalEntryCard entry={entry()} />)
    expect(screen.getByRole("link", { name: /Quality/ })).toHaveAttribute(
      "href",
      "/crews?crew=quality",
    )
  })
})
