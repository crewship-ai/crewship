import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"
import { InboxDetail } from "../inbox-list"
import type { InboxItem } from "@/hooks/use-inbox"

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

function item(state: InboxItem["state"]): InboxItem {
  return {
    id: "inbox_1",
    workspace_id: "ws_test",
    kind: "message",
    source_id: "src_1",
    title: "A note from the crew",
    body_md: "hello",
    state,
    priority: "medium",
    blocking: false,
    created_at: "2026-06-25T11:00:00Z",
    updated_at: "2026-06-25T11:00:00Z",
  }
}

describe("InboxDetail — explicit mark-read", () => {
  afterEach(() => cleanup())

  it("an unread item shows 'Mark read' and marks read only on click — never implicitly", () => {
    const onMarkRead = vi.fn()
    const onMarkUnread = vi.fn()

    render(
      <InboxDetail
        item={item("unread")}
        onResolve={vi.fn()}
        onMarkRead={onMarkRead}
        onMarkUnread={onMarkUnread}
        onRefresh={vi.fn()}
      />,
    )

    // Rendering (i.e. opening) must NOT have marked it read.
    expect(onMarkRead).not.toHaveBeenCalled()

    const btn = screen.getByRole("button", { name: /mark read/i })
    expect(screen.queryByRole("button", { name: /mark unread/i })).toBeNull()

    fireEvent.click(btn)
    expect(onMarkRead).toHaveBeenCalledTimes(1)
  })

  it("a read item shows 'Mark unread'", () => {
    render(
      <InboxDetail
        item={item("read")}
        onResolve={vi.fn()}
        onMarkRead={vi.fn()}
        onMarkUnread={vi.fn()}
        onRefresh={vi.fn()}
      />,
    )
    expect(screen.getByRole("button", { name: /mark unread/i })).toBeTruthy()
    expect(screen.queryByRole("button", { name: /mark read/i })).toBeNull()
  })
})
