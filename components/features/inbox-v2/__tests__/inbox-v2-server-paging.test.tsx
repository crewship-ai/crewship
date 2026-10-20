import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

import { filterInboxEntries } from "../inbox-entry-identity"
import { EMPTY_INBOX_V2_FILTERS, inboxEntry } from "../inbox-v2-derive"
import { EMPTY_INBOX_LOOKUP } from "../inbox-v2-types"

// The Inbox loads the newest page of history and walks the active items. Older
// history and search over everything are the server's: "Load more" pages the
// list by cursor, and the search box sends its text to the server instead of
// filtering the rows that happen to be loaded.

const m = vi.hoisted(() => ({
  items: [] as InboxItem[],
  pageItems: [] as InboxItem[],
  pagesCalls: [] as Array<{ params: { space?: string; q?: string }; enabled: boolean }>,
  loadMore: vi.fn(),
  hasMore: true,
  counts: null as unknown,
  countsCalls: [] as unknown[],
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
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
  useInboxCounts: (_ws: string, params?: unknown) => { m.countsCalls.push(params); return { data: m.counts } },
}))
vi.mock("@/hooks/use-inbox-pages", () => ({
  useInboxPages: (_ws: string, params: { space?: string; q?: string }, enabled = true) => {
    m.pagesCalls.push({ params, enabled })
    return { items: enabled ? m.pageItems : [], hasMore: enabled && m.hasMore, loadMore: m.loadMore, loading: false, loadingMore: false, error: null }
  },
}))
vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({ rows: [], loading: false, error: null, notConfigured: false, refresh: vi.fn(async () => {}), patchRow: vi.fn() }),
  decideApproval: vi.fn(),
}))
vi.mock("@/components/features/inbox/use-inbox-lookup", () => ({
  useInboxLookup: () => ({ crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }),
}))
vi.mock("../inbox-v2-detail", () => ({ InboxV2Detail: () => <p>Detail</p> }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

import { InboxV2 } from "../inbox-v2"

function item(o: Partial<InboxItem>): InboxItem {
  return {
    id: "i1", workspace_id: "ws", kind: "waitpoint", source_id: "s", title: "Approve production deploy",
    state: "unread", priority: "medium", blocking: true,
    created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z", ...o,
  }
}

const SERVER_COUNTS = {
  total: 9, unread: 4,
  spaces: { action: 2, updates: 0, history: 7 },
  by_kind: { waitpoint: { action: 2, updates: 0, history: 7 } },
  by_attention: { decision: 2 },
  by_deadline: { none: 9 },
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><InboxV2 /></QueryClientProvider>)
}

beforeEach(() => {
  vi.useFakeTimers()
  m.items = [item({ id: "wp1" }), item({ id: "wp2", title: "Approve the hire" })]
  m.pageItems = []
  m.pagesCalls = []
  m.countsCalls = []
  m.hasMore = true
  m.counts = SERVER_COUNTS
  m.loadMore.mockClear()
})
afterEach(() => { vi.useRealTimers(); cleanup() })

const search = () => screen.getByRole("textbox", { name: "Search inbox…" })
const lastPages = () => m.pagesCalls[m.pagesCalls.length - 1]

describe("Inbox paging and search on the server", () => {
  it("sends the search text to the server after a pause, not on every keystroke", () => {
    show()
    expect(m.pagesCalls.every((c) => !c.enabled)).toBe(true)
    fireEvent.change(search(), { target: { value: "de" } })
    fireEvent.change(search(), { target: { value: "deploy" } })
    expect(m.pagesCalls.some((c) => c.params.q === "deploy")).toBe(false)
    act(() => { vi.advanceTimersByTime(300) })
    expect(lastPages()).toEqual({ params: expect.objectContaining({ q: "deploy" }), enabled: true })
    expect(m.pagesCalls.some((c) => c.params.q === "de")).toBe(false)
  })

  it("lists what the server found, including rows that were never loaded, and hides loaded rows it did not match", () => {
    show()
    m.pageItems = [item({ id: "old", title: "Approve a deploy from last spring", state: "unread" })]
    fireEvent.change(search(), { target: { value: "deploy" } })
    act(() => { vi.advanceTimersByTime(300) })
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(within(panel).getByText("Approve a deploy from last spring")).toBeInTheDocument()
    // loaded, matches the text in the browser, but the server did not return it
    expect(within(panel).queryByText("Approve production deploy")).toBeNull()
    expect(within(panel).queryByText("Approve the hire")).toBeNull()
  })

  it("keeps filtering the loaded rows while the pause runs, so typing answers at once", () => {
    show()
    fireEvent.change(search(), { target: { value: "hire" } })
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(within(panel).getByText("Approve the hire")).toBeInTheDocument()
    expect(within(panel).queryByText("Approve production deploy")).toBeNull()
  })

  it("offers Load more in History and pages the history space by cursor", () => {
    show()
    fireEvent.click(within(screen.getByRole("tablist", { name: "Inbox spaces" })).getByRole("tab", { name: /History/ }))
    expect(lastPages()).toEqual({ params: expect.objectContaining({ space: "history" }), enabled: true })
    fireEvent.click(screen.getByRole("button", { name: "Load more" }))
    expect(m.loadMore).toHaveBeenCalledOnce()
  })

  it("has no Load more when the server has no further page", () => {
    m.hasMore = false
    show()
    fireEvent.click(within(screen.getByRole("tablist", { name: "Inbox spaces" })).getByRole("tab", { name: /History/ }))
    expect(screen.queryByRole("button", { name: /Load more/ })).toBeNull()
  })

  it("on a server without search and facet counts, searches the loaded rows as before and pages nothing", () => {
    m.counts = null
    show()
    fireEvent.change(search(), { target: { value: "hire" } })
    act(() => { vi.advanceTimersByTime(300) })
    expect(m.pagesCalls.every((c) => !c.enabled || c.params.q === undefined || c.params.q === "")).toBe(true)
    const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
    expect(within(panel).getByText("Approve the hire")).toBeInTheDocument()
  })

  it("asks the server for the viewed space's facet counts with the end of the local day", () => {
    show()
    const scoped = m.countsCalls.find((c) => c && typeof c === "object" && "space" in (c as object)) as { space: string; dayEnd: string }
    expect(scoped.space).toBe("action")
    expect(new Date(scoped.dayEnd).getHours()).toBe(23)
  })
})

describe("filterInboxEntries with the server's matches", () => {
  const a = inboxEntry(item({ id: "a", title: "Alpha" }))
  const b = inboxEntry(item({ id: "b", title: "Beta" }))
  const filters = { ...EMPTY_INBOX_V2_FILTERS, search: "zzz" }

  it("keeps exactly the inbox rows the server returned, whatever the text says", () => {
    const kept = filterInboxEntries([a, b], filters, EMPTY_INBOX_LOOKUP, "urgency", new Set(["b"]))
    expect(kept.map((e) => e.key)).toEqual(["inbox:b"])
  })

  it("without server matches, matches the text in the browser as before", () => {
    expect(filterInboxEntries([a, b], filters, EMPTY_INBOX_LOOKUP)).toEqual([])
    expect(filterInboxEntries([a, b], { ...filters, search: "alp" }, EMPTY_INBOX_LOOKUP).map((e) => e.key)).toEqual(["inbox:a"])
  })
})
