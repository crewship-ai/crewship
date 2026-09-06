import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { InboxV2Detail } from "../inbox-v2-detail"
import type { InboxV2Entry } from "../inbox-v2-types"

afterEach(cleanup)
const entry: InboxV2Entry = { key: "approval:done", source: "approval", title: "Review preview change", summary: "", subject: "Ops", category: "approval", priority: "high", createdAt: "2026-09-06T12:00:00Z", actionable: true, historical: false, unread: true }
const base: Parameters<typeof InboxV2Detail>[0] = {
  entry, role: "OWNER", confirmation: { entry, action: "approved", at: entry.createdAt },
  onClearConfirmation: vi.fn(), onViewReceipt: vi.fn(), onInboxResolve: vi.fn(),
  onInboxArchive: vi.fn(), onInboxMarkUnread: vi.fn(), onInboxRefresh: vi.fn(),
  onInboxAct: vi.fn(), onApprovalDecide: vi.fn(), onArchiveGroup: vi.fn(),
}
describe("confirmation navigation", () => {
  it("offers a remaining decision without opening it automatically", () => {
    const onOpen = vi.fn()
    render(<InboxV2Detail {...base} nextReview={{count: 2, onOpen}} />)
    expect(onOpen).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Next to review · 2" }))
    expect(onOpen).toHaveBeenCalledOnce()
    expect(screen.getByRole("button", { name: "View record" })).toBeVisible()
  })
  it("has no dead next button when the queue is empty", () => {
    render(<InboxV2Detail {...base} />)
    expect(screen.queryByRole("button", { name: /Next to review/ })).toBeNull()
    expect(screen.getByRole("button", { name: "Back to inbox" })).toBeVisible()
  })
})
