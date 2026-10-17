import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { InboxV2Detail } from "../inbox-v2-detail"
import type { InboxV2Entry } from "../inbox-v2-types"
import type { ApprovalRow } from "@/lib/types/approvals"
import type { InboxItem } from "@/hooks/use-inbox"
import { approvalEntry, inboxEntry } from "../inbox-v2-derive"

vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "me" } } }) }))
vi.mock("@/components/features/inbox/waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))

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
  it("receipt shows the outcome, that it is saved in History, and opens the record", () => {
    const onViewReceipt = vi.fn()
    render(<InboxV2Detail {...base} onViewReceipt={onViewReceipt} nextReview={{ count: 3, onOpen: vi.fn() }} />)
    const receipt = screen.getByTestId("inbox-receipt")
    expect(within(receipt).getByRole("heading", { name: "Approved" })).toBeInTheDocument()
    expect(receipt).toHaveTextContent("the record stays in History")
    // Primary last: View record, then Next to review.
    expect(within(receipt).getAllByRole("button").map((b) => b.textContent)).toEqual(["View record", "Next to review · 3"])
    fireEvent.click(within(receipt).getByRole("button", { name: "View record" }))
    expect(onViewReceipt).toHaveBeenCalledWith(entry)
  })
  it("renders grouped incident bodies as readable Markdown", () => {
    render(<InboxV2Detail {...base} confirmation={null} entry={{ ...entry, source: "group", groupedItems: [{ id: "incident", title: "Service update", created_at: entry.createdAt, body_md: "## Recovery steps\n\n[Open issue](/issues/ENG-7)\n\n```text\nservice restarted\n```" } as import("@/hooks/use-inbox").InboxItem] }} />)
    fireEvent.click(screen.getByText("View 1 related updates"))
    expect(screen.getByRole("heading", { name: "Recovery steps" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Open issue" })).toBeEnabled()
    expect(screen.getByText("service restarted").closest("pre")).toBeTruthy()
  })
  it("has no dead next button when the queue is empty, and no separate Back", () => {
    render(<InboxV2Detail {...base} />)
    expect(screen.queryByRole("button", { name: /Next to review/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Back to inbox" })).toBeNull()
    expect(screen.getByRole("button", { name: "View record" })).toBeVisible()
  })
})

describe("grouped incident", () => {
  const items = [1, 2, 3, 4, 5].map((n) => ({ id: `g${n}`, title: `Skill check timed out: s${n}`, sender_name: "Skill Curator", created_at: entry.createdAt, state: "unread" }) as InboxItem)
  const group: InboxV2Entry = { ...entry, key: "group:x", source: "group", groupedItems: items }

  it("previews three, counts the rest, and archives all of them in one call", () => {
    const onArchiveGroup = vi.fn(async () => {})
    render(<InboxV2Detail {...base} confirmation={null} entry={group} onArchiveGroup={onArchiveGroup} />)
    const list = screen.getByRole("list", { name: "Grouped updates" })
    expect(within(list).getAllByRole("listitem")).toHaveLength(4)
    expect(list).toHaveTextContent("+2 more")
    fireEvent.click(screen.getByRole("button", { name: /Archive 5 updates/ }))
    expect(onArchiveGroup).toHaveBeenCalledWith(group)
  })

  it("offers System Health only to an admin tier", () => {
    render(<InboxV2Detail {...base} confirmation={null} entry={group} role="MEMBER" />)
    expect(screen.queryByRole("link", { name: "Open System Health" })).toBeNull()
    cleanup()
    render(<InboxV2Detail {...base} confirmation={null} entry={group} role="ADMIN" />)
    expect(screen.getByRole("link", { name: "Open System Health" })).toHaveAttribute("href", "/admin")
  })
})

describe("approval decide (v67 dock)", () => {
  const row = { id: "ap1", kind: "destructive_op", reason: "Merge acme/web#812", status: "pending", created_at: entry.createdAt, payload: { tool: "github.merge_pr" } } as ApprovalRow

  it("ADMIN approves with a comment; Approve is the right-most button", () => {
    const onApprovalDecide = vi.fn(async () => {})
    render(<InboxV2Detail {...base} confirmation={null} entry={approvalEntry(row)} role="ADMIN" onApprovalDecide={onApprovalDecide} />)
    const dock = screen.getByTestId("decision-dock")
    expect(within(dock).getAllByRole("button").map((b) => b.textContent)).toEqual(["Deny", "Approve"])
    fireEvent.change(screen.getByRole("textbox", { name: "Decision comment" }), { target: { value: "ok to ship" } })
    fireEvent.click(within(dock).getByRole("button", { name: "Approve" }))
    expect(onApprovalDecide).toHaveBeenCalledWith(expect.objectContaining({ key: "approval:ap1" }), "approved", "ok to ship")
    expect(screen.getByTestId("decision-block")).toHaveAttribute("data-tone", "danger")
  })

  it("MEMBER sees a disabled dock and who decides", () => {
    render(<InboxV2Detail {...base} confirmation={null} entry={approvalEntry(row)} role="MEMBER" />)
    const dock = screen.getByTestId("decision-dock")
    for (const b of within(dock).getAllByRole("button")) expect(b).toBeDisabled()
    expect(within(dock).getByTestId("inbox-role-note")).toHaveTextContent("An owner or admin decides this")
  })
})

describe("history item", () => {
  it("renders the receipt with View in Activity above the preserved request", () => {
    const resolved = inboxEntry({
      id: "h1", workspace_id: "ws", kind: "waitpoint", source_id: "w", title: "Publish weekly report?",
      state: "resolved", resolved_action: "approved", resolved_at: entry.createdAt, priority: "high", blocking: true,
      created_at: entry.createdAt, updated_at: entry.createdAt, payload: { pipeline_run_id: "run_1" },
    } as InboxItem)
    render(<InboxV2Detail {...base} confirmation={null} entry={resolved} />)
    const receipt = screen.getByTestId("history-receipt")
    expect(within(receipt).getByRole("heading", { name: "Approved" })).toBeInTheDocument()
    expect(within(receipt).getByRole("link", { name: "View in Activity" })).toHaveAttribute("href", "/activity?run=run_1")
    expect(screen.getByTestId("decision-card")).toHaveTextContent("Decision record")
  })
})
