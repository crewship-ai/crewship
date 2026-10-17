import { describe, expect, it, vi } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import { InboxV2Explorer } from "../inbox-v2-explorer"
import { EMPTY_INBOX_V2_FILTERS, inboxEntry } from "../inbox-v2-derive"

// The three <select>s this replaces filtered on fields the server cannot
// answer — and two of them were client fictions. These tests pin the two
// properties that matter for the replacement: every facet counts the WHOLE
// feed, and choosing one narrows the list to rows that really carry the field.
function item(o: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "i1", workspace_id: "ws", kind: "message", source_id: "s", title: "An update",
    state: "unread", priority: "medium", blocking: false,
    created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z", ...o,
  }
}

const FEED = [
  inboxEntry(item({ id: "wp1", kind: "waitpoint", title: "Approve production deploy" })),
  inboxEntry(item({ id: "wp2", kind: "waitpoint", title: "Approve the hire", state: "read" })),
  inboxEntry(item({ id: "es1", kind: "escalation", title: "Credential needed" })),
]

function renderExplorer(over: Partial<React.ComponentProps<typeof InboxV2Explorer>> = {}) {
  const onFilters = vi.fn()
  const props = {
    view: "action" as const,
    onView: vi.fn(),
    viewCounts: { action: 3, updates: 2, history: 7 },
    entries: FEED,
    visible: FEED,
    filters: EMPTY_INBOX_V2_FILTERS,
    onFilters,
    selectedKey: null,
    onOpen: vi.fn(),
    ...over,
  }
  render(<InboxV2Explorer {...props} />)
  return { onFilters, props }
}

describe("inbox v2 explorer", () => {
  it("offers the three spaces with their counts, and the Overview row", () => {
    const onOverview = vi.fn()
    const { props } = renderExplorer({ onOverview })
    const spaces = screen.getByRole("tablist", { name: "Inbox spaces" })
    for (const [label, count] of [["To handle", "3"], ["Updates", "2"]]) {
      const tab = within(spaces).getByRole("tab", { name: new RegExp(label, "i") })
      expect(within(tab).getByText(count)).toBeTruthy()
    }
    // History is the record, not a queue: no count to work down.
    expect(within(spaces).getByRole("tab", { name: /History/ })).not.toHaveTextContent(/\d/)
    expect(within(spaces).getByRole("tab", { name: /To handle/ })).toHaveAttribute("aria-selected", "true")
    fireEvent.click(within(spaces).getByRole("tab", { name: /Updates/ }))
    expect(props.onView).toHaveBeenCalledWith("updates")
    fireEvent.click(screen.getByRole("button", { name: "Overview" }))
    expect(onOverview).toHaveBeenCalledOnce()
  })

  it("keeps the current space selected while the Overview is in the pane", () => {
    renderExplorer({ view: "updates", onOverview: vi.fn(), overviewSelected: true })
    const spaces = screen.getByRole("tablist", { name: "Inbox spaces" })
    // The list below is still that space's list; the Overview row is selected beside it.
    expect(within(spaces).getByRole("tab", { name: /Updates/ })).toHaveAttribute("aria-selected", "true")
    expect(within(spaces).getByRole("tab", { name: /To handle/ })).toHaveAttribute("aria-selected", "false")
  })

  it("renders in the v67 row anatomy: unread dot, crew · kind, expiry or age, merged count", () => {
    const soon = new Date(Date.now() + 3 * 3_600_000).toISOString()
    const lookup = { crewById: new Map([["c-r", { id: "c-r", name: "Research", slug: "research", color: "teal" }]]), agentBySlug: new Map(), agentById: new Map(), ready: true }
    const expiring = inboxEntry(item({ id: "x", kind: "waitpoint", title: "Publish report?", payload: { timeout_at: soon, crew_id: "c-r" } }))
    renderExplorer({ entries: [expiring], visible: [expiring], lookup })
    const row = screen.getByTestId("inbox-row")
    expect(row).toHaveTextContent("Research · Approval")
    expect(row).toHaveTextContent("in 3h")
    expect(within(row).getByText("Unread")).toBeInTheDocument()
  })

  it("separates human decisions from actionable schedule alerts", () => {
    const decision = inboxEntry(item({ id: "decision", kind: "waitpoint", title: "Approve deployment" }))
    const schedule = inboxEntry(item({ id: "schedule", kind: "schedule_missed", title: "Schedule missed", payload: { schedule_id: "schedule-1" } }))
    renderExplorer({ entries: [schedule, decision], visible: [schedule, decision] })
    const decisions = screen.getByText("Decision required")
    const alerts = screen.getByText("Operational alerts")
    expect(within(decisions.parentElement!.parentElement!.parentElement!).getByText("Approve deployment")).toBeInTheDocument()
    expect(within(alerts.parentElement!.parentElement!.parentElement!).getByText("Schedule missed")).toBeInTheDocument()
  })

  it("counts each type facet over the whole feed, not over what is on screen", () => {
    renderExplorer({ visible: [FEED[0]] })
    fireEvent.click(screen.getByRole("button", { name: /^Filter$/i }))
    const waitpoint = screen.getByRole("button", { name: /Approval step/i })
    expect(within(waitpoint).getByText("2")).toBeTruthy()
  })

  it("asks the parent to narrow when a facet is chosen", () => {
    const { onFilters } = renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /^Filter$/i }))
    fireEvent.click(screen.getByRole("button", { name: /Agent request or notice/i }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ type: "escalation" }))
  })

  it("opens the facets in the panel, Attention among them, and keeps them open across picks", () => {
    const { onFilters } = renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /^Filter$/i }))
    const facets = screen.getByRole("group", { name: "Filter inbox" })
    for (const label of ["State", "Attention", "Source type"]) expect(within(facets).getByText(label)).toBeInTheDocument()
    fireEvent.click(within(facets).getByRole("button", { name: /^Decision/ }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ attention: "decision" }))
    expect(screen.getByRole("group", { name: "Filter inbox" })).toBeInTheDocument()
  })

  it("names a chip by its facet: State: Unread only", () => {
    renderExplorer({ filters: { ...EMPTY_INBOX_V2_FILTERS, unreadOnly: true, attention: "repair" } })
    expect(screen.getByText("State: Unread only")).toBeInTheDocument()
    expect(screen.getByText("Attention: Repair")).toBeInTheDocument()
  })

  it("offers the crews that have items as a Crew facet", () => {
    const lookup = { crewById: new Map([["c-ops", { id: "c-ops", name: "Ops", slug: "ops", color: "green" }]]), agentBySlug: new Map(), agentById: new Map(), ready: true }
    const row = inboxEntry(item({ id: "c1", kind: "waitpoint", payload: { crew_id: "c-ops" } }))
    const { onFilters } = renderExplorer({ entries: [row], visible: [row], lookup })
    fireEvent.click(screen.getByRole("button", { name: /^Filter$/i }))
    fireEvent.click(within(screen.getByRole("group", { name: "Filter inbox" })).getByRole("button", { name: /^Ops/ }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ crew: "c-ops" }))
  })

  it("shows skeleton rows while the first load is in flight, not an empty state", () => {
    renderExplorer({ entries: [], visible: [], loading: true })
    expect(screen.getByRole("status", { name: "Loading inbox" })).toBeInTheDocument()
    expect(screen.queryByText(/Nothing waits for you/)).toBeNull()
  })

  it("shows an active facet as a removable chip", () => {
    const { onFilters } = renderExplorer({
      filters: { ...EMPTY_INBOX_V2_FILTERS, type: "waitpoint" },
    })
    const chip = screen.getByText(/Approval step/i)
    expect(chip).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /remove filter/i }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ type: null }))
  })

  it("explains an empty crew selection as a filter result", () => {
    renderExplorer({ visible: [], filters: { ...EMPTY_INBOX_V2_FILTERS, crew: "empty-crew" } })
    expect(screen.getByText("Nothing matches those filters.")).toBeInTheDocument()
  })

  it("never offers a filter the server cannot answer", () => {
    renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /^Filter$/i }))
    expect(screen.queryByText(/all subjects/i)).toBeNull()
    expect(screen.queryByText(/all priorities/i)).toBeNull()
  })
})

describe("history keeps archived noise out of the decision list", () => {
  const decided = inboxEntry(item({
    id: "d1", kind: "waitpoint", title: "Approve production deploy",
    // resolved_by_user_id is what makes it a decision rather than a sweep —
    // the expiry and timeout paths leave it empty.
    state: "resolved", resolved_action: "approved", resolved_at: "2026-08-30T10:00:00Z",
    resolved_by_user_id: "u-1",
  }))
  const archived = inboxEntry(item({
    id: "a1", kind: "escalation", sender_name: "Skill Curator", title: "Skill review: sk_f9e228c7",
    state: "resolved", resolved_action: "archived", resolved_at: "2026-08-30T10:00:00Z",
  }))

  it("lists a decision and an archived advisory under different headings", () => {
    renderExplorer({ view: "history", entries: [decided, archived], visible: [decided, archived] })
    const decisions = screen.getByText("Decisions")
    // The section heading comes first in the column; the row's outcome pill
    // says "Archived" too, and that is correct — one word for one state.
    const archive = screen.getAllByText("Archived")[0]
    expect(decisions).toBeTruthy()
    expect(archive).toBeTruthy()
    // …and the archived row is not counted as a decision: each heading owns one row.
    expect(within(decisions.parentElement!).getByText("1")).toBeTruthy()
    expect(within(archive.parentElement!).getByText("1")).toBeTruthy()
  })

  it("does not call six archived advisories six decision records", () => {
    const six = [1, 2, 3, 4, 5, 6].map((n) => inboxEntry(item({
      id: `sk-${n}`, kind: "escalation", sender_name: "Skill Curator",
      title: `Skill review: sk_${n}`, state: "resolved", resolved_action: "archived",
    })))
    renderExplorer({ view: "history", entries: six, visible: six })
    expect(screen.queryByText("Decisions")).toBeNull()
    expect(screen.getAllByText("Archived").length).toBeGreaterThan(0)
  })
})
