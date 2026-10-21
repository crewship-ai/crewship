import { StrictMode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// Phone pilot, Inbox (PILOT-TELEFON-STAV rows 2–7): the detail header is
// Back + the item's name + ⋯; Back from a direct link stays in the app; the
// list's search and filters come back after a detail and after a reload, but
// never in another workspace; active filters are chips on the page with a
// "Clear filters"; empty, no-results, loading and error read differently.

const m = vi.hoisted(() => ({
  mobile: true, ws: "ws-a", search: "", push: vi.fn(), replace: vi.fn(),
  items: [] as InboxItem[], error: null as string | null, loading: false,
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: vi.fn(async () => ({ ok: true, result: { updated: 1 } })) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => m.mobile }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: m.push, replace: m.replace }),
  useSearchParams: () => new URLSearchParams(m.search),
  usePathname: () => "/inbox",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("[]")) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: m.ws, role: "MEMBER" }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-inbox", () => ({
  useInbox: () => ({
    items: m.items, loading: m.loading, error: m.error, unreadCount: 0,
    refresh: vi.fn(async () => {}), patch: vi.fn(async () => {}), actOnInboxItem: vi.fn(async () => {}),
  }),
  useInboxItem: () => ({ data: undefined, isFetching: false }),
  useInboxCounts: () => ({ data: null }),
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
import { listDetailKey } from "@/hooks/use-phone-list-detail"
import { InboxV2 } from "../inbox-v2"

const LONG = "Approve production deploy of billing-service v2.14 for Finance & Marketing"

function item(o: Partial<InboxItem>): InboxItem {
  return {
    id: "i1", workspace_id: "ws-a", kind: "message", source_id: "s", title: "Week in sales",
    state: "unread", priority: "medium", blocking: false,
    created_at: "2026-10-19T10:00:00Z", updated_at: "2026-10-19T10:00:00Z", ...o,
  }
}

let slot: HTMLElement
const client = () => new QueryClient({ defaultOptions: { queries: { retry: false } } })
function show() {
  const c = client()
  const view = render(<QueryClientProvider client={c}><InboxV2 /></QueryClientProvider>)
  return { ...view, again: () => view.rerender(<QueryClientProvider client={c}><InboxV2 /></QueryClientProvider>) }
}
const searchBox = () => screen.getByRole("textbox", { name: "Search inbox…" }) as HTMLInputElement

beforeEach(() => {
  m.mobile = true
  m.ws = "ws-a"
  m.search = ""
  m.error = null
  m.loading = false
  m.items = [
    item({ id: "n1", title: "31 new newsletter sign-ups" }),
    item({ id: "n2", title: "Low stock: Oak storage box" }),
    item({ id: "w1", kind: "waitpoint", blocking: true, title: LONG }),
  ]
  sessionStorage.clear()
  window.history.replaceState(null, "", "/inbox")
  slot = document.createElement("div")
  slot.dataset.areaTitle = "Inbox"
  document.body.appendChild(slot)
  act(() => useAppStore.getState().setPageBarSlot(slot))
  act(() => useAppStore.getState().setSecondaryPanelOpen(false))
})
afterEach(() => {
  cleanup()
  slot.remove()
  act(() => useAppStore.getState().setPageBarSlot(null))
  vi.clearAllMocks()
})

describe("Inbox on a phone: the detail header", () => {
  it("is Back, the item's whole name in two lines, and ⋯ — not four icons", () => {
    m.search = "item=w1"
    show()
    expect(within(slot).getByRole("button", { name: "Back to inbox" })).toHaveAttribute("data-tap", "")
    expect(slot.querySelector("[data-slot=sub-bar-detail-title]")).toHaveTextContent(LONG)
    expect(within(slot).getByRole("button", { name: "More actions" })).toBeInTheDocument()
    expect(within(slot).queryByRole("button", { name: "Next (J)" })).toBeNull()
    expect(within(slot).queryByRole("button", { name: "Archive (E)" })).toBeNull()
  })

  it("keeps Previous, Next and Archive in the ⋯ sheet", () => {
    m.search = "item=n1"
    show()
    fireEvent.click(within(slot).getByRole("button", { name: "More actions" }))
    const sheet = screen.getByRole("dialog", { name: "More actions" })
    expect(within(sheet).getByRole("button", { name: /previous/i })).toBeInTheDocument()
    expect(within(sheet).getByRole("button", { name: /next/i })).toBeInTheDocument()
    expect(within(sheet).getByRole("button", { name: /archive/i })).toBeInTheDocument()
  })

  it("Back from a direct link goes to the inbox list, never out of the app", () => {
    m.search = "item=n1"
    show()
    fireEvent.click(within(slot).getByRole("button", { name: "Back to inbox" }))
    expect(m.replace).toHaveBeenCalledWith("/inbox")
  })

  it("leaves the desktop header as it was", () => {
    m.mobile = false
    m.search = "item=n1"
    show()
    expect(within(slot).getByRole("button", { name: "Next (J)" })).toBeInTheDocument()
    expect(within(slot).queryByRole("button", { name: "More actions" })).toBeNull()
  })
})

describe("Inbox on a phone: the list's state", () => {
  it("opening a row from the list remembers it as the entry under the detail", () => {
    m.search = "view=updates"
    show()
    fireEvent.change(searchBox(), { target: { value: "newsletter" } })
    fireEvent.click(screen.getByText("31 new newsletter sign-ups"))
    expect(m.push).toHaveBeenCalledWith("/inbox?view=updates&item=n1")
    const stored = JSON.parse(sessionStorage.getItem(listDetailKey("inbox", "ws-a"))!)
    expect(stored.detail).toBe("/inbox?view=updates&item=n1")
    expect(stored.state.filters.search).toBe("newsletter")
  })

  it("comes back with its search after the detail", () => {
    sessionStorage.setItem(listDetailKey("inbox", "ws-a"), JSON.stringify({ detail: "/inbox?item=n1", state: { filters: { search: "newsletter", unreadOnly: true }, view: "updates" } }))
    show()
    expect(searchBox().value).toBe("newsletter")
    expect(screen.getByRole("button", { name: "Remove filter State: Unread only" })).toBeInTheDocument()
  })

  it("keeps the restored search under React's development double-run (and does not save over it)", () => {
    sessionStorage.setItem(listDetailKey("inbox", "ws-a"), JSON.stringify({ detail: "/inbox?item=n1", state: { filters: { search: "newsletter" }, view: "updates" } }))
    render(<StrictMode><QueryClientProvider client={client()}><InboxV2 /></QueryClientProvider></StrictMode>)
    expect(searchBox().value).toBe("newsletter")
    expect(JSON.parse(sessionStorage.getItem(listDetailKey("inbox", "ws-a"))!).state.filters.search).toBe("newsletter")
  })

  it("opens fresh when the inbox is entered anew (no detail under it)", () => {
    sessionStorage.setItem(listDetailKey("inbox", "ws-a"), JSON.stringify({ detail: null, state: { filters: { search: "newsletter" } } }))
    show()
    expect(searchBox().value).toBe("")
  })

  it("does not carry one workspace's search into another", () => {
    const view = show()
    fireEvent.change(searchBox(), { target: { value: "newsletter" } })
    m.ws = "ws-b"
    view.again()
    expect(searchBox().value).toBe("")
  })
})

describe("Inbox list: filters and states", () => {
  it("shows active filters as chips on the page with Clear filters", () => {
    sessionStorage.setItem(listDetailKey("inbox", "ws-a"), JSON.stringify({ detail: "/inbox?item=n1", state: { filters: { search: "", unreadOnly: true, type: "message" } } }))
    show()
    // The chip row's; the empty list below offers its own.
    const clear = screen.getAllByRole("button", { name: "Clear filters" })[0]
    fireEvent.click(clear)
    expect(screen.queryByRole("button", { name: /Remove filter/ })).toBeNull()
  })

  it("says what a search found nothing for, and where", () => {
    show()
    fireEvent.change(searchBox(), { target: { value: "zzz-nothing" } })
    expect(screen.getByText("Nothing matches “zzz-nothing”")).toBeInTheDocument()
  })

  it("an empty inbox is not a failed search", () => {
    m.items = []
    show()
    expect(screen.queryByText(/Nothing matches/)).toBeNull()
  })

  it("a failed load is an error with Retry in the list, not an empty inbox", async () => {
    m.items = []
    m.error = "boom"
    show()
    const list = await screen.findByTestId("inbox-list-error")
    expect(list).toHaveTextContent("The inbox could not load")
    expect(within(list).getByRole("button", { name: "Retry" })).toBeInTheDocument()
    expect(screen.queryByText(/Nothing waits for you/)).toBeNull()
  })
})
