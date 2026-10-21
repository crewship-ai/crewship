import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import { InboxV2Explorer } from "../inbox-v2-explorer"
import { InboxOverview } from "../inbox-v2-overview"
import { EMPTY_INBOX_V2_FILTERS, inboxEntry } from "../inbox-v2-derive"
import type { InboxLookup } from "../inbox-v2-types"

// Harbor 2 parity with the v67 wireframe (PAR-019 … PAR-023, 152, 153).
// jsdom has no layout, so sizes are asserted through the classes that set them.
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => null }))
afterEach(cleanup)

const item = (o: Partial<InboxItem> = {}): InboxItem => ({
  id: "i1", workspace_id: "ws", kind: "waitpoint", source_id: "s", title: "Approve production deploy",
  state: "unread", priority: "medium", blocking: true,
  created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z", ...o,
})
const FEED = [inboxEntry(item()), inboxEntry(item({ id: "i2", title: "Approve the hire", state: "read" }))]

function renderExplorer(over: Partial<React.ComponentProps<typeof InboxV2Explorer>> = {}) {
  const props = {
    view: "action" as const, onView: vi.fn(), viewCounts: { action: 2, updates: 2, history: 7 },
    entries: FEED, visible: FEED, filters: EMPTY_INBOX_V2_FILTERS, onFilters: vi.fn(),
    selectedKey: null, onOpen: vi.fn(), onSort: vi.fn(), onTogglePicking: vi.fn(), ...over,
  }
  render(<InboxV2Explorer {...props} />)
  return props
}

describe("PAR-019 list toolbar", () => {
  it("has no Sort button: search, Filter, select, and nothing else (v67)", () => {
    renderExplorer()
    expect(screen.queryByRole("button", { name: "View: sort" })).toBeNull()
    expect(screen.getByRole("button", { name: /Filter/ })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Select several" })).toBeInTheDocument()
  })

  it("keeps Sort, inside the Filter block as a Display facet", () => {
    const props = renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    const block = screen.getByRole("group", { name: "Filter inbox" })
    fireEvent.click(within(block).getByRole("button", { name: /^Sort/ }))
    fireEvent.click(within(block).getByRole("button", { name: /Newest/ }))
    expect(props.onSort).toHaveBeenCalledWith("newest")
  })

  it("shows the / shortcut hint in the search field until something is typed", () => {
    renderExplorer()
    expect(document.querySelector("[data-inbox-search] kbd")?.textContent).toBe("/")
    cleanup()
    renderExplorer({ filters: { ...EMPTY_INBOX_V2_FILTERS, search: "deploy" } })
    expect(document.querySelector("[data-inbox-search] kbd")).toBeNull()
  })

  it("counts the active filters on the Filter button", () => {
    renderExplorer({ filters: { ...EMPTY_INBOX_V2_FILTERS, unreadOnly: true } })
    expect(screen.getByRole("button", { name: /Filter/ })).toHaveTextContent("1")
  })
})

describe("PAR-020 / 152 / 153 space counts", () => {
  it("sets the counts in sans tabular figures, not mono", () => {
    renderExplorer()
    const tab = screen.getByRole("tab", { name: /To handle/ })
    const count = within(tab).getByText("2")
    expect(count.className).not.toContain("font-mono")
    expect(count.className).toContain("tabular-nums")
  })
})

const lookup: InboxLookup = {
  crewById: new Map([["c-ops", { id: "c-ops", name: "Ops", slug: "ops", color: "green" }]]),
  agentBySlug: new Map(), agentById: new Map(), ready: true,
}

describe("PAR-022 / 023 Overview typography", () => {
  const renderOverview = () =>
    render(<InboxOverview action={[inboxEntry(item({ payload: { crew_id: "c-ops" } }))]} updates={[]} history={[]} lookup={lookup} onOpen={vi.fn()} onAttention={vi.fn()} onCrew={vi.fn()} />)

  it("sets the tile title at 13 px (text-control) and gives the figure a 34 px square so the hint is not clipped", () => {
    renderOverview()
    const tile = screen.getByRole("button", { name: /^Decision: / })
    expect(within(tile).getByText("Decision").className).toContain("text-control")
    expect(within(tile).getByText("Decision").className).toContain("font-semibold")
    const figure = within(tile).getByText("1")
    expect(figure.className).toContain("h-[34px]")
    expect(figure.className).toContain("w-[34px]")
  })

  it("heads each section card like the Dashboard: 28 px tile, 13 px semibold title, no tight tracking", () => {
    renderOverview()
    const section = screen.getByRole("region", { name: "By crew" })
    const title = within(section).getByRole("heading", { name: "By crew" })
    expect(title.className).toContain("text-control")
    expect(title.className).toContain("font-semibold")
    expect(title.className).not.toContain("tracking-tight")
    const tile = section.querySelector(".icon-tile")!
    // 28 px in px: the Harbor spacing scale draws h-7 at 24 px.
    expect(tile.className.split(" ")).toContain("size-[28px]")
  })

  it("keeps the empty list note in sentence case at 12.5 px sans (PAR-021)", () => {
    render(<InboxV2Explorer view="action" onView={vi.fn()} viewCounts={{ action: 0, updates: 0, history: 0 }} entries={[]} visible={[]} filters={EMPTY_INBOX_V2_FILTERS} onFilters={vi.fn()} selectedKey={null} onOpen={vi.fn()} />)
    const note = screen.getByText(/Nothing waits for you/)
    expect(note.textContent).toBe("Nothing waits for you. Decisions, questions and reviews from agents land here.")
    expect(note.className).not.toMatch(/uppercase|font-mono/)
  })
})
