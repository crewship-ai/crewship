import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import type { InboxItem } from "@/hooks/use-inbox"
import { InboxDetail } from "../inbox-detail"

vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
afterEach(cleanup)

function message(overrides: Partial<InboxItem> = {}): InboxItem {
  return { id: "notification", workspace_id: "ws", kind: "message", source_id: "issue", title: "ENG-6 ready for review", body_md: "The original work summary.", state: "unread", priority: "medium", blocking: false, created_at: "2026-09-06T12:00:00Z", updated_at: "2026-09-06T12:00:00Z", payload: { issue_identifier: "ENG-6" }, ...overrides }
}
function show(item: InboxItem) {
  return render(<InboxDetail item={item} role="OWNER" onResolve={vi.fn()} onArchive={vi.fn()} onMarkUnread={vi.fn()} onRefresh={vi.fn()} />)
}

describe("client message reading", () => {
  it("offers one source link with real actions and retains the original body", () => {
    show(message())
    expect(screen.getAllByRole("link", { name: "Open ENG-6" })).toHaveLength(1)
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("ENG-6 ready for review")
    expect(screen.getByText("The original work summary.")).toBeVisible()
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeEnabled()
  })
  it("keeps the source accessible in history without a stale Dismiss action", () => {
    show(message({ state: "resolved", resolved_action: "dismissed" }))
    expect(screen.getByRole("link", { name: "Open ENG-6" })).toHaveAttribute("href", "/issues/ENG-6")
    expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull()
  })
  it("makes long code reachable by keyboard", async () => {
    const { container } = show(message({ body_md: "```text\n" + "output_".repeat(150) + "\n```" }))
    await waitFor(() => expect(container.querySelector("pre")).toHaveAttribute("tabindex", "0"))
  })
})
