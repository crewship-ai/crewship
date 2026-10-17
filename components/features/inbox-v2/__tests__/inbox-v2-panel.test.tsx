import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// Harbor 2 M3 (#3350): the inbox list is the section's SecondaryPanel, so
// width, collapse and the phone pattern are the panel's, not the page's.

const m = vi.hoisted(() => ({ mobile: false, push: vi.fn(), patch: vi.fn(async () => {}), bulk: vi.fn(async () => ({ ok: true as const, result: { updated: 1 } })), items: [] as InboxItem[] }))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: m.bulk }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => m.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: m.push, replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => "/inbox",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("[]")) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-inbox", () => ({
  useInbox: () => ({
    items: m.items, loading: false, error: null, unreadCount: 0,
    refresh: vi.fn(async () => {}), patch: m.patch, actOnInboxItem: vi.fn(async () => {}),
  }),
  useInboxItem: () => ({ data: undefined, isFetching: false }),
}))
vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({ rows: [], loading: false, error: null, notConfigured: false, refresh: vi.fn(async () => {}), patchRow: vi.fn() }),
  decideApproval: vi.fn(),
}))
vi.mock("@/components/features/inbox/use-inbox-lookup", () => ({
  useInboxLookup: () => ({ crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }),
}))
vi.mock("../inbox-v2-detail", () => ({
  InboxV2Detail: ({ entry }: { entry: { title?: string } | null }) => <p>Detail {entry?.title ?? "none"}</p>,
}))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

import { useAppStore } from "@/lib/store"
import { InboxV2 } from "../inbox-v2"

function item(o: Partial<InboxItem>): InboxItem {
  return {
    id: "i1", workspace_id: "ws", kind: "waitpoint", source_id: "s", title: "Approve production deploy",
    state: "unread", priority: "medium", blocking: true,
    created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z", ...o,
  }
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><InboxV2 /></QueryClientProvider>)
}

beforeEach(() => {
  m.mobile = false
  m.items = [item({ id: "wp1" }), item({ id: "wp2", title: "Approve the hire" })]
  try { window.localStorage.removeItem("crewship.pref.panel.inbox.collapsed") } catch { /* no storage */ }
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe("Inbox on SecondaryPanel", () => {
  it("renders the explorer inside the inbox panel, with the kit toolbar as its toolbar", () => {
    show()
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(panel).toHaveAttribute("data-panel-id", "inbox")
    expect(panel).toHaveAccessibleName("Inbox views")
    expect(within(panel).getByText("Approve the hire")).toBeInTheDocument()
    // One collapse button: the explorer's own is gone, the panel's sits in its toolbar.
    const toggles = screen.getAllByRole("button", { name: "Hide Inbox views" })
    expect(toggles).toHaveLength(1)
    expect(toggles[0].closest(".secondary-panel-toolbar")).not.toBeNull()
    // The reading pane is beside the list, not behind a phone-only toggle.
    expect(screen.getByRole("main", { name: "Inbox detail" })).not.toHaveClass("hidden")
  })

  it("never opens a row by itself on desktop: the pane is the Overview and nothing is marked read", () => {
    show()
    expect(screen.getByRole("main", { name: "Inbox detail" })).toHaveTextContent("Detail none")
    expect(m.patch).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Overview" })).toHaveAttribute("aria-pressed", "true")
  })

  it("walks the list with J and K, shows n / N, and keeps Archive off an undecided decision", () => {
    show()
    fireEvent.keyDown(window, { key: "j" })
    expect(m.push).toHaveBeenLastCalledWith("/inbox?item=wp1")
    expect(screen.getByText("1 / 2")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Archive (E)" })).toBeDisabled()
    fireEvent.keyDown(window, { key: "j" })
    expect(m.push).toHaveBeenLastCalledWith("/inbox?item=wp2")
    expect(screen.getByText("2 / 2")).toBeInTheDocument()
    fireEvent.keyDown(window, { key: "k" })
    expect(m.push).toHaveBeenLastCalledWith("/inbox?item=wp1")
  })

  it("switches spaces with 1–3 but never while typing", () => {
    show()
    fireEvent.keyDown(window, { key: "2" })
    expect(m.push).toHaveBeenLastCalledWith("/inbox?view=updates")
    m.push.mockClear()
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Search inbox…" }), { key: "3" })
    expect(m.push).not.toHaveBeenCalled()
  })

  it("selects several rows and marks them read in one call; open decisions cannot be bulk-archived", async () => {
    show()
    fireEvent.click(screen.getByRole("button", { name: "Select several" }))
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    fireEvent.click(within(panel).getByText("Approve the hire"))
    expect(m.push).not.toHaveBeenCalled()
    const bar = screen.getByTestId("inbox-bulk")
    expect(bar).toHaveTextContent("1 selected")
    expect(within(bar).getByRole("button", { name: /Archive/ })).toBeDisabled()
    await act(async () => { fireEvent.click(within(bar).getByRole("button", { name: /Mark read/ })) })
    expect(m.bulk).toHaveBeenCalledWith("ws", ["wp2"], "read")
    fireEvent.click(within(bar).getByRole("button", { name: "Done selecting" }))
    expect(screen.queryByTestId("inbox-bulk")).toBeNull()
  })

  it("Mark N read marks only visible unread rows", async () => {
    show()
    fireEvent.change(screen.getByRole("textbox", { name: "Search inbox…" }), { target: { value: "hire" } })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Mark 1 read" })) })
    expect(m.bulk).toHaveBeenCalledWith("ws", ["wp2"], "read")
  })

  it("collapses to the panel rail and back", () => {
    show()
    fireEvent.click(screen.getByRole("button", { name: "Hide Inbox views" }))
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(panel).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByText("Approve the hire")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Show Inbox views" }))
    expect(within(panel).getByText("Approve the hire")).toBeInTheDocument()
  })

  it("on a phone goes list → detail → back to the list (PNL-05), with no sheet button", () => {
    m.mobile = true
    show()
    // List first: the panel content is the page, no preview, nothing marked read.
    const list = screen.getByRole("region", { name: "Inbox views" })
    expect(list).toHaveAttribute("data-phone", "inline")
    expect(useAppStore.getState().secondaryPanel).toBeNull()
    expect(screen.queryByRole("main", { name: "Inbox detail" })).toBeNull()
    expect(m.patch).not.toHaveBeenCalled()
    // Open → only the detail, and opening is what marks the row read.
    fireEvent.click(within(list).getByText("Approve the hire"))
    expect(m.push).toHaveBeenCalledWith("/inbox?item=wp2")
    expect(m.patch).toHaveBeenCalledWith("wp2", "read")
    expect(screen.getByRole("main", { name: "Inbox detail" })).toHaveTextContent("Detail Approve the hire")
    expect(screen.queryByRole("region", { name: "Inbox views" })).toBeNull()
    expect(document.querySelector("[data-slot=secondary-panel]")).toBeNull()
    // Back → the list again.
    fireEvent.click(screen.getByRole("button", { name: "Back to inbox" }))
    expect(screen.getByRole("region", { name: "Inbox views" })).toBeInTheDocument()
    expect(screen.queryByRole("main", { name: "Inbox detail" })).toBeNull()
  })
})
