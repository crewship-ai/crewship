import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, fireEvent, within } from "@testing-library/react"
import type { JournalEntry } from "@/lib/types/journal"
import type { AgentLookup, CrewLookup, JournalLookupValue } from "@/hooks/use-journal-lookup"

// Mock Virtuoso so all rows render synchronously into the DOM —
// happy-dom has no real layout, so the real Virtuoso renders zero items.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    data,
    itemContent,
  }: {
    data: JournalEntry[]
    itemContent: (i: number, e: JournalEntry) => React.ReactNode
  }) => (
    <div data-testid="virtuoso">
      {data.map((item, i) => (
        <div key={item.id} data-testid="virtuoso-row">
          {itemContent(i, item)}
        </div>
      ))}
    </div>
  ),
}))

// The lookup provider is mounted app-wide in the dashboard layout and
// fetches over the network. Stub the hook, not the transport, so the row
// can be handed an exact crew/agent table with no socket in the suite.
const state = vi.hoisted(() => ({
  lookup: null as unknown as JournalLookupValue,
}))

vi.mock("@/hooks/use-journal-lookup", () => ({
  useJournalLookup: () => state.lookup,
  JournalLookupProvider: ({ children }: { children: React.ReactNode }) => children,
}))

// An expanded row renders EgressAllowlistAction, which mounts
// useAbilities — an unmocked GET /api/v1/workspaces, which the suite's
// no-sockets guard rightly fails the test over.
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", loading: false }),
}))

// next/image is deliberately NOT mocked: the avatar is an `unoptimized`
// data URI, which is exactly the path next/image passes through
// untouched, and a stub here would hide a regression in how the row
// hands it over.

import { LogsList } from "@/components/features/logs/logs-list"

function agent(overrides: Partial<AgentLookup> = {}): AgentLookup {
  return {
    id: "agt_morgan",
    name: "Morgan",
    slug: "morgan",
    crew_id: "crw_backend",
    avatar_seed: "morgan",
    avatar_style: null,
    ...overrides,
  }
}

function crew(overrides: Partial<CrewLookup> = {}): CrewLookup {
  return {
    id: "crw_backend",
    name: "Backend",
    slug: "backend",
    icon: "ship",
    color: "emerald",
    ...overrides,
  }
}

function lookupWith(agents: AgentLookup[] = [], crews: CrewLookup[] = []): JournalLookupValue {
  return {
    agents: new Map(agents.map((a) => [a.id, a])),
    crews: new Map(crews.map((c) => [c.id, c])),
    missions: new Map(),
    loading: false,
    refresh: () => {},
  }
}

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "e1",
    workspace_id: "ws_test",
    ts: "2026-08-31T14:32:07.412Z",
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "exit 0 (3893ms)",
    payload: {},
    refs: {},
    ...overrides,
  }
}

function renderList(entries: JournalEntry[]) {
  return render(
    <LogsList entries={entries} wrap={false} followTail={false} newestFirst />,
  )
}

beforeEach(() => {
  state.lookup = lookupWith([agent()], [crew()])
})

describe("LogsList row identity", () => {
  it("renders the agent avatar for an agent-authored entry", () => {
    const { getAllByTestId } = renderList([
      entry({ agent_id: "agt_morgan", crew_id: "crw_backend" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    const avatar = within(row).getByRole("img", { name: "Morgan" })
    expect(avatar).toBeInTheDocument()
    // DiceBear renders to a data URI; anything else means the avatar
    // pipeline was not used.
    expect(avatar.getAttribute("src") ?? "").toMatch(/^data:image\/svg\+xml/)
  })

  it("falls back to a labelled glyph, not a blank, for a system actor", () => {
    const { getAllByTestId } = renderList([
      entry({ id: "sys", actor_type: "system", entry_type: "system.migration" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    const glyph = within(row).getByRole("img", { name: /system/i })
    expect(glyph).toBeInTheDocument()
    // Not an avatar image — a glyph.
    expect(glyph.tagName.toLowerCase()).not.toBe("img")
  })

  it("renders the crew icon for an entry that carries a crew", () => {
    const { getAllByTestId } = renderList([
      entry({ agent_id: "agt_morgan", crew_id: "crw_backend" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    expect(within(row).getByRole("img", { name: /Backend/ })).toBeInTheDocument()
  })

  it("renders the entry-type icon and the full dotted entry_type", () => {
    const { getAllByTestId } = renderList([entry({ entry_type: "exec.command" })])
    const row = getAllByTestId("virtuoso-row")[0]
    // lib/journal-icons.ts maps exec.command → Terminal.
    expect(row.querySelector("svg.lucide-terminal")).not.toBeNull()
    expect(within(row).getByText("exec.command")).toBeInTheDocument()
  })

  it("drops the redundant '<agent>:' prefix from the summary", () => {
    const { getAllByTestId } = renderList([
      entry({ agent_id: "agt_morgan", summary: "morgan: exit 0 (3893ms)" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    expect(within(row).getByText("exit 0 (3893ms)")).toBeInTheDocument()
    expect(within(row).queryByText(/morgan:/)).toBeNull()
  })

  it("leaves a colon that is not the agent's name alone", () => {
    const { getAllByTestId } = renderList([
      entry({ agent_id: "agt_morgan", summary: "curl: (7) failed to connect" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    expect(within(row).getByText("curl: (7) failed to connect")).toBeInTheDocument()
  })

  it("still renders a stable avatar for an agent the lookup cannot resolve", () => {
    state.lookup = lookupWith([], [])
    const { getAllByTestId } = renderList([entry({ agent_id: "agt_ghost_0000000001" })])
    const row = getAllByTestId("virtuoso-row")[0]
    // Name falls back to a shortened id, but the seat is never blank.
    const avatar = within(row).getByRole("img", { name: /agt_gh/ })
    expect(avatar.getAttribute("src") ?? "").toMatch(/^data:image\/svg\+xml/)
  })
})

describe("LogsList accessibility", () => {
  it("exposes the row as a disclosure whose aria-expanded tracks the detail", () => {
    const { getAllByTestId } = renderList([entry()])
    const row = getAllByTestId("virtuoso-row")[0]
    const disclosure = within(row).getByRole("button", { expanded: false })
    fireEvent.click(disclosure)
    expect(within(row).getByRole("button", { expanded: true })).toBeInTheDocument()
    fireEvent.click(disclosure)
    expect(within(row).getByRole("button", { expanded: false })).toBeInTheDocument()
  })

  it("exposes severity as text, not colour alone, without expanding the row", () => {
    const { getAllByTestId } = renderList([
      entry({ severity: "error", summary: "the sidecar is serving an old binary" }),
    ])
    const row = getAllByTestId("virtuoso-row")[0]
    expect(within(row).getByText("Severity: error")).toBeInTheDocument()
  })

  it("exposes the type group as text, not pill colour alone", () => {
    const { getAllByTestId } = renderList([entry({ entry_type: "exec.command" })])
    const row = getAllByTestId("virtuoso-row")[0]
    expect(within(row).getByText("exec group")).toBeInTheDocument()
  })
})
