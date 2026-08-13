import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { SessionsSidebar, type SessionRow } from "../sessions-sidebar"

// =============================================================================
// The per-agent session list is being re-clothed in the shared sidebar kit
// (SidebarSearch / SidebarSection / SidebarRow) so chat and /routines read as
// one system. Chrome is allowed to change; behaviour is not. This file is the
// contract the swap has to keep: it lists, it selects, it searches, it hides
// empty sessions and it says how many it hid.
// =============================================================================

function session(id: string, title: string | null, over: Partial<SessionRow> = {}): SessionRow {
  return {
    id,
    title,
    status: "ACTIVE",
    message_count: 3,
    started_at: "2026-08-12T10:00:00.000Z",
    ended_at: null,
    last_activity_at: "2026-08-12T10:00:00.000Z",
    ...over,
  }
}

function renderSidebar(sessions: SessionRow[], activeSessionId: string | null = null) {
  const onSelect = vi.fn()
  render(
    <SessionsSidebar
      sessions={sessions}
      activeSessionId={activeSessionId}
      agentSlug="ada"
      onSelect={onSelect}
    />,
  )
  return { onSelect }
}

describe("SessionsSidebar", () => {
  beforeEach(() => cleanup())

  it("lists sessions, newest activity first", () => {
    renderSidebar([
      session("s-old", "Older thread", { last_activity_at: "2026-08-11T09:00:00.000Z" }),
      session("s-new", "Newer thread", { last_activity_at: "2026-08-12T09:00:00.000Z" }),
    ])
    const rows = screen.getAllByRole("button", { name: /thread/i })
    expect(rows[0].textContent).toContain("Newer thread")
    expect(rows[1].textContent).toContain("Older thread")
  })

  it("calls onSelect with the session id when a row is activated", () => {
    const { onSelect } = renderSidebar([session("s-1", "Ship the export")])
    fireEvent.click(screen.getByRole("button", { name: /ship the export/i }))
    expect(onSelect).toHaveBeenCalledWith("s-1")
  })

  it("marks the active session as selected", () => {
    renderSidebar([session("s-1", "Ship the export"), session("s-2", "Other thread")], "s-1")
    const active = screen.getByRole("button", { name: /ship the export/i })
    expect(active).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: /other thread/i })).toHaveAttribute("aria-pressed", "false")
  })

  it("filters by title as the user searches", () => {
    renderSidebar([session("s-1", "Ship the export"), session("s-2", "Rename the sidebar")])
    fireEvent.change(screen.getByLabelText(/search chat sessions/i), { target: { value: "export" } })
    expect(screen.getByText("Ship the export")).toBeInTheDocument()
    expect(screen.queryByText("Rename the sidebar")).toBeNull()
  })

  it("says so when a search matches nothing", () => {
    renderSidebar([session("s-1", "Ship the export")])
    fireEvent.change(screen.getByLabelText(/search chat sessions/i), { target: { value: "zzz" } })
    expect(screen.getByText(/no matches/i)).toBeInTheDocument()
  })

  it("hides empty sessions and offers to reveal them", () => {
    renderSidebar([session("s-1", "Ship the export"), session("s-2", "Nothing said", { message_count: 0 })])
    expect(screen.queryByText("Nothing said")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /1 empty session/i }))
    expect(screen.getByText("Nothing said")).toBeInTheDocument()
  })

  it("keeps a 0-message session visible while it is the active one", () => {
    renderSidebar([session("s-1", "Just created", { message_count: 0 })], "s-1")
    expect(screen.getByText("Just created")).toBeInTheDocument()
  })

  it("badges unread messages on sessions that are not open", () => {
    renderSidebar([session("s-1", "Ship the export", { unread_count: 3 })], "s-2")
    expect(screen.getByLabelText("3 unread messages")).toBeInTheDocument()
  })

  // --- the kit-specific expectations ------------------------------------

  it("carries the shared section header with a trailing count", () => {
    renderSidebar([session("s-1", "One"), session("s-2", "Two")])
    const header = screen.getByText(/sessions/i)
    // The count sits beside the label in the same header, as on /routines.
    expect(header.parentElement?.textContent).toMatch(/2/)
  })

  it("renders rows through the shared selection primitive, not a bespoke button", () => {
    // data-selected is what ListRow stamps; it is the tokenised accent bar
    // rather than a hand-rolled border-l-2 border-primary.
    renderSidebar([session("s-1", "Ship the export")], "s-1")
    const row = screen.getByRole("button", { name: /ship the export/i })
    expect(row).toHaveAttribute("data-selected", "true")
  })

  it("still shows origin and message count on a row", () => {
    renderSidebar([session("s-1", "Ship the export", { origin: "CLI", message_count: 7 })])
    const row = screen.getByRole("button", { name: /ship the export/i })
    expect(within(row).getByText("CLI")).toBeInTheDocument()
    expect(row.textContent).toContain("7 msgs")
  })
})
