import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// COM-03: the partial-source banner is the kit PageBanner (bad tone), the
// page's one banner, with Retry kept.

const m = vi.hoisted(() => ({ mobile: false, push: vi.fn(), patch: vi.fn(async () => {}), bulk: vi.fn(async () => ({ ok: true as const, result: { updated: 1 } })), items: [] as InboxItem[], approvalsError: null as string | null, refreshApprovals: vi.fn(async () => {}) }))
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
  // Server counts (#3675): absent here, so the page counts what it loaded.
  useInboxCounts: () => ({ data: null }),
}))
vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({ rows: [], loading: false, error: m.approvalsError, notConfigured: false, refresh: m.refreshApprovals, patchRow: vi.fn() }),
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
  m.approvalsError = "approvals unavailable"
  m.items = [item({ id: "wp1" })]
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe("Inbox partial-source banner (COM-03)", () => {
  it("renders through the kit PageBanner: alert role, bad tone, one banner", () => {
    show()
    const banner = screen.getByRole("alert")
    expect(banner).toHaveAttribute("data-tone", "bad")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
    expect(banner).toHaveTextContent("This inbox may be incomplete")
    expect(banner.querySelector(".page-banner-text")).not.toBeNull()
    expect(document.querySelectorAll(".page-banner")).toHaveLength(1)
  })

  it("keeps Retry working", () => {
    show()
    fireEvent.click(within(screen.getByRole("alert")).getByRole("button", { name: "Retry" }))
    expect(m.refreshApprovals).toHaveBeenCalledTimes(1)
  })

  it.each([true, false])("is the same banner with the phone hook at %s (tablet 1024 px is the non-mobile path)", (mobile) => {
    m.mobile = mobile
    show()
    expect(screen.getByRole("alert")).toHaveAttribute("data-tone", "bad")
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  })

  it("is absent when every source answers", () => {
    m.approvalsError = null
    show()
    expect(screen.queryByRole("alert")).toBeNull()
  })
})
