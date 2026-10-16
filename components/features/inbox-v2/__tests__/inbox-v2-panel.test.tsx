import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// Harbor 2 M3 (#3350): the inbox list is the section's SecondaryPanel, so
// width, collapse and the phone pattern are the panel's, not the page's.

const m = vi.hoisted(() => ({ mobile: false, push: vi.fn(), items: [] as InboxItem[] }))
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
    refresh: vi.fn(async () => {}), patch: vi.fn(async () => {}), actOnInboxItem: vi.fn(async () => {}),
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

  it("collapses to the panel rail and back", () => {
    show()
    fireEvent.click(screen.getByRole("button", { name: "Hide Inbox views" }))
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(panel).toHaveAttribute("data-collapsed", "true")
    expect(screen.queryByText("Approve the hire")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Show Inbox views" }))
    expect(within(panel).getByText("Approve the hire")).toBeInTheDocument()
  })

  it("on a phone puts the list in the panel sheet and closes it when a row opens", () => {
    m.mobile = true
    show()
    expect(document.querySelector("[data-slot=secondary-panel]")).toBeNull()
    expect(screen.getByRole("main", { name: "Inbox detail" })).toBeInTheDocument()
    act(() => useAppStore.getState().setSecondaryPanelOpen(true))
    const sheet = screen.getByRole("dialog", { name: "Inbox views" })
    fireEvent.click(within(sheet).getByText("Approve the hire"))
    expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
    expect(m.push).toHaveBeenCalledWith("/inbox?item=wp2")
  })
})
