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
  it("renders grouped incident bodies as readable Markdown", () => {
    render(<InboxV2Detail {...base} confirmation={null} entry={{ ...entry, source: "group", groupedItems: [{ id: "incident", title: "Service update", created_at: entry.createdAt, body_md: "## Recovery steps\n\n[Open issue](/issues/ENG-7)\n\n```text\nservice restarted\n```" } as import("@/hooks/use-inbox").InboxItem] }} />)
    fireEvent.click(screen.getByText("View 1 related updates"))
    expect(screen.getByRole("heading", { name: "Recovery steps" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Open issue" })).toBeEnabled()
    expect(screen.getByText("service restarted").closest("pre")).toBeTruthy()
  })
  it("has no dead next button when the queue is empty", () => {
    render(<InboxV2Detail {...base} />)
    expect(screen.queryByRole("button", { name: /Next to review/ })).toBeNull()
    expect(screen.getByRole("button", { name: "Back to inbox" })).toBeVisible()
  })
})
