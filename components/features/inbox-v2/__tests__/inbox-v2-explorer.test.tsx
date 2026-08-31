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
    collapsed: false,
    onToggleCollapse: vi.fn(),
    ...over,
  }
  render(<InboxV2Explorer {...props} />)
  return { onFilters, props }
}

describe("inbox v2 explorer", () => {
  it("offers the three views with their counts, like the routines rail", () => {
    renderExplorer()
    for (const [label, count] of [["Needs action", "3"], ["Updates", "2"], ["History", "7"]]) {
      const row = screen.getByRole("button", { name: new RegExp(label, "i") })
      expect(within(row).getByText(count)).toBeTruthy()
    }
  })

  it("counts each type facet over the whole feed, not over what is on screen", () => {
    renderExplorer({ visible: [FEED[0]] })
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    const waitpoint = screen.getByRole("button", { name: /waitpoint/i })
    expect(within(waitpoint).getByText("2")).toBeTruthy()
  })

  it("asks the parent to narrow when a facet is chosen", () => {
    const { onFilters } = renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    fireEvent.click(screen.getByRole("button", { name: /escalation/i }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ type: "escalation" }))
  })

  it("shows an active facet as a removable chip", () => {
    const { onFilters } = renderExplorer({
      filters: { ...EMPTY_INBOX_V2_FILTERS, type: "waitpoint" },
    })
    const chip = screen.getByText(/waitpoint/i)
    expect(chip).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /remove filter/i }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ type: null }))
  })

  it("never offers a filter the server cannot answer", () => {
    renderExplorer()
    fireEvent.click(screen.getByRole("button", { name: /filter/i }))
    expect(screen.queryByText(/all subjects/i)).toBeNull()
    expect(screen.queryByText(/all priorities/i)).toBeNull()
  })
})

describe("history keeps archived noise out of the decision list", () => {
  const decided = inboxEntry(item({
    id: "d1", kind: "waitpoint", title: "Approve production deploy",
    state: "resolved", resolved_action: "approved", resolved_at: "2026-08-30T10:00:00Z",
  }))
  const archived = inboxEntry(item({
    id: "a1", kind: "escalation", sender_name: "Skill Curator", title: "Skill review: sk_f9e228c7",
    state: "resolved", resolved_action: "archived", resolved_at: "2026-08-30T10:00:00Z",
  }))

  it("lists a decision and an archived advisory under different headings", () => {
    renderExplorer({ view: "history", entries: [decided, archived], visible: [decided, archived] })
    const decisions = screen.getByText("Decisions")
    const archive = screen.getByText("Archived")
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
    expect(screen.getByText("Archived")).toBeTruthy()
  })
})
