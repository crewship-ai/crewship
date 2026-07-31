import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

import { InboxBellView } from "../inbox-bell-new"

afterEach(cleanup)

const now = Date.now()

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  return {
    workspace_id: "ws",
    source_id: `s-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: new Date(now - 60_000).toISOString(),
    updated_at: new Date(now - 60_000).toISOString(),
    ...over,
  } as InboxItem
}

const ITEMS: InboxItem[] = [
  item({
    id: "wp",
    kind: "waitpoint",
    title: "Approve promote",
    blocking: true,
    sender_type: "pipeline",
    sender_name: "docs-publish",
    created_at: new Date(now - 3 * 3600_000).toISOString(),
    payload: { timeout_at: new Date(now + 9 * 60_000).toISOString() },
  }),
  item({
    id: "esc",
    kind: "escalation",
    title: "Skill review",
    blocking: true,
    state: "read",
    sender_type: "agent",
    sender_name: "casey",
    payload: { kind: "skill_proposal" },
  }),
  item({ id: "msg", kind: "message", title: "Atlas replied", sender_type: "agent", sender_name: "atlas" }),
]

describe("InboxBellView", () => {
  it("puts what expires soonest above what arrived last", () => {
    render(<InboxBellView items={ITEMS} role="OWNER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    const rows = within(screen.getByTestId("bell-popover"))
      .getAllByRole("button")
      .filter((b) => b.getAttribute("data-testid")?.startsWith("bell-row-"))

    expect(rows[0]).toHaveAttribute("data-testid", "bell-row-wp")
  })

  it("keeps a blocking item that was already read", () => {
    render(<InboxBellView items={ITEMS} role="OWNER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    // state=read, still parked. The bell this replaces filtered on unread and
    // dropped it while the agent kept waiting.
    expect(screen.getByTestId("bell-row-esc")).toBeInTheDocument()
  })

  it("marks every unread row read, and says how many", async () => {
    const onMarkAllRead = vi.fn().mockResolvedValue(undefined)
    render(
      <InboxBellView
        items={ITEMS}
        role="OWNER"
        onOpenItem={vi.fn()}
        onOpenInbox={vi.fn()}
        onMarkAllRead={onMarkAllRead}
      />,
    )
    fireEvent.click(screen.getByTestId("bell-trigger"))

    // Two of the three are unread; the label counts rather than promising "all".
    fireEvent.click(screen.getByRole("button", { name: /Mark 2 read/ }))

    await waitFor(() => expect(onMarkAllRead).toHaveBeenCalledWith(["wp", "msg"]))
  })

  it("hides the affordance when there is nothing unread", () => {
    const read = ITEMS.map((i) => ({ ...i, state: "read" as const }))
    render(
      <InboxBellView items={read} role="OWNER" onOpenItem={vi.fn()} onOpenInbox={vi.fn()} onMarkAllRead={vi.fn()} />,
    )
    fireEvent.click(screen.getByTestId("bell-trigger"))

    expect(screen.queryByRole("button", { name: /Mark .* read/ })).not.toBeInTheDocument()
  })
})
