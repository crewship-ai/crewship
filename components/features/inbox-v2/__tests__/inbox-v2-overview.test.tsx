import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import { inboxEntry } from "../inbox-v2-derive"
import { InboxV2Detail } from "../inbox-v2-detail"
import { InboxOverview, type InboxOverviewProps } from "../inbox-v2-overview"
import type { InboxLookup, InboxV2Entry } from "../inbox-v2-types"

// Harbor 2 Inbox (#3385): with nothing open the pane is the Overview — "Your
// inbox, at a glance" — never a previewed row and never a blank prompt.

vi.mock("@/components/ui/agent-avatar", () => ({
  AgentAvatar: ({ seed }: { seed: string }) => <span data-testid="avatar">{seed}</span>,
}))
afterEach(cleanup)

function item(id: string, crew: string | null, overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id, workspace_id: "ws", kind: "escalation", source_id: `esc-${id}`, title: `Agent escalation: q ${id}`,
    state: "unread", priority: "high", blocking: true, sender_type: "agent", sender_name: "riley",
    created_at: "2026-09-03T10:00:00Z", updated_at: "2026-09-03T10:00:00Z",
    payload: { escalation_type: "TEXT", reason: `q ${id}`, ...(crew ? { crew_id: crew } : {}) },
    ...overrides,
  }
}

const lookup: InboxLookup = {
  crewById: new Map([["c-ops", { id: "c-ops", name: "Ops", slug: "ops", color: "emerald" }]]),
  agentBySlug: new Map([["riley", { id: "a-1", name: "Riley", slug: "riley" }]]),
  agentById: new Map([["a-1", { id: "a-1", name: "Riley", slug: "riley" }]]),
  ready: true,
}

function overview(over: Partial<InboxOverviewProps> = {}) {
  const props: InboxOverviewProps = {
    action: [], updates: [], history: [], lookup,
    onOpen: vi.fn(), onAttention: vi.fn(), onCrew: vi.fn(), ...over,
  }
  render(<InboxOverview {...props} />)
  return props
}

describe("InboxOverview", () => {
  it("heads with what waits: to handle, unread, expiring within the hour", () => {
    const soon = new Date(Date.now() + 20 * 60_000).toISOString()
    overview({
      action: [
        inboxEntry(item("1", "c-ops", { kind: "waitpoint", payload: { timeout_at: soon, crew_id: "c-ops" } })),
        inboxEntry(item("2", "c-ops", { state: "read" })),
      ],
      updates: [inboxEntry(item("3", null, { kind: "message", blocking: false }))],
    })
    expect(screen.getByRole("heading", { level: 1, name: "Your inbox, at a glance" })).toBeInTheDocument()
    expect(screen.getByText("2 to handle · 2 unread · 1 expires within an hour")).toBeInTheDocument()
  })

  it("offers the four attention tiles, each narrowing the list", () => {
    const props = overview({
      action: [
        inboxEntry(item("q", "c-ops")),
        inboxEntry(item("w", "c-ops", { kind: "waitpoint" })),
        inboxEntry(item("f", "c-ops", { kind: "failed_run", blocking: false })),
      ],
    })
    expect(screen.getByRole("button", { name: "Decision: 1" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Input needed: 1" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Review: 0" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Repair: 1" }))
    expect(props.onAttention).toHaveBeenCalledWith("repair")
  })

  it("shows the load by crew and narrows to a crew by id", () => {
    const props = overview({ action: [inboxEntry(item("1", "c-ops")), inboxEntry(item("2", "c-ops")), inboxEntry(item("3", null))] })
    const byCrew = screen.getByRole("region", { name: "By crew" })
    expect(within(byCrew).getByText("2 items")).toBeInTheDocument()
    fireEvent.click(within(byCrew).getByRole("button", { name: "Filter inbox by Ops" }))
    expect(props.onCrew).toHaveBeenCalledWith("c-ops")
  })

  it("lists recent decisions with an outcome word, never the raw action", () => {
    overview({ history: [inboxEntry(item("d", "c-ops", { state: "resolved", resolved_action: "reject", resolved_at: "2026-09-03T11:00:00Z" }))] })
    const recent = screen.getByRole("region", { name: "Recent history" })
    expect(within(recent).getByText("Rejected")).toBeInTheDocument()
    expect(within(recent).queryByText("reject")).toBeNull()
  })

  it("is never blank: the zero form says what lands here", () => {
    overview()
    expect(screen.getByText(/Nothing expires soon/)).toBeInTheDocument()
    expect(screen.getByText(/Nothing has been decided yet/)).toBeInTheDocument()
  })

  it("does not claim all caught up when a source is unavailable", () => {
    overview({ incomplete: true })
    expect(screen.queryByText("All caught up")).toBeNull()
    expect(screen.getByText(/Partial information/)).toBeInTheDocument()
  })

  it("follows a Dashboard attention link with exactly the rows it promised", () => {
    const onClear = vi.fn()
    const onOpen = vi.fn()
    const row = inboxEntry(item("approval", "c-ops"))
    overview({ onOpen, focus: { label: "Decisions waiting", entries: [row], onClear } })
    const focus = screen.getByTestId("inbox-focus-overview")
    expect(within(focus).getByRole("heading", { name: "Decisions waiting" })).toBeInTheDocument()
    fireEvent.click(within(focus).getByText("q approval"))
    expect(onOpen).toHaveBeenCalledWith(row)
    fireEvent.click(within(focus).getByRole("button", { name: /All inbox/ }))
    expect(onClear).toHaveBeenCalledOnce()
  })

  it("says so when the attention link matches nothing", () => {
    overview({ focus: { label: "Run alerts", entries: [], onClear: vi.fn() } })
    expect(screen.getByText("Nothing matches this filter right now.")).toBeInTheDocument()
  })
})

describe("InboxV2Detail with nothing open", () => {
  const base: Parameters<typeof InboxV2Detail>[0] = {
    entry: null, role: "OWNER", confirmation: null,
    onClearConfirmation: vi.fn(), onViewReceipt: vi.fn(), onInboxResolve: vi.fn(),
    onInboxArchive: vi.fn(), onInboxMarkUnread: vi.fn(), onInboxRefresh: vi.fn(),
    onInboxAct: vi.fn(), onApprovalDecide: vi.fn(), onArchiveGroup: vi.fn(),
  }
  const props = (entries: InboxV2Entry[] = []): InboxOverviewProps => ({
    action: entries, updates: [], history: [], lookup, onOpen: vi.fn(), onAttention: vi.fn(), onCrew: vi.fn(),
  })

  it("renders the Overview, never an automatically opened row", () => {
    render(<InboxV2Detail {...base} overview={props([inboxEntry(item("wait", "c-ops"))])} />)
    expect(screen.getByTestId("inbox-overview")).toBeInTheDocument()
    expect(screen.queryByTestId("decision-card")).toBeNull()
    expect(screen.queryByText("Select an item to see its context and actions.")).toBeNull()
  })

  it("says an unknown ?item is no longer in the inbox, with History and back", () => {
    const onOpenHistory = vi.fn()
    const onBackToHandle = vi.fn()
    render(<InboxV2Detail {...base} selectionMissing overview={props()} onOpenHistory={onOpenHistory} onBackToHandle={onBackToHandle} />)
    expect(screen.getByRole("heading", { name: "No longer in your inbox" })).toBeInTheDocument()
    expect(screen.getByText("It was handled, archived or moved. Your history keeps the record.")).toBeInTheDocument()
    expect(screen.queryByTestId("inbox-overview")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Open History" }))
    expect(onOpenHistory).toHaveBeenCalledOnce()
    fireEvent.click(screen.getByRole("button", { name: "Back to To handle" }))
    expect(onBackToHandle).toHaveBeenCalledOnce()
  })
})
