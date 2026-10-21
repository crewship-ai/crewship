import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook, waitFor } from "@testing-library/react"

import { useInboxCounts, type InboxItem } from "@/hooks/use-inbox"
import { useInboxPages } from "@/hooks/use-inbox-pages"
import { useDebouncedValue } from "@/hooks/use-debounced-value"

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))

function wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function ok(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

function row(id: string): InboxItem {
  return {
    id, workspace_id: "ws", kind: "message", source_id: id, title: id, state: "resolved", priority: "medium",
    blocking: false, created_at: "2026-08-30T10:00:00Z", updated_at: "2026-08-30T10:00:00Z",
  }
}

const urls = () => apiFetch.mock.calls.map((c) => new URL(String(c[0]), "http://x"))

beforeEach(() => { apiFetch.mockReset() })
afterEach(() => { vi.useRealTimers() })

describe("useInboxPages", () => {
  it("walks the list by cursor, one page per loadMore, and stops when the server has no more", async () => {
    apiFetch
      .mockResolvedValueOnce(ok({ rows: [row("a"), row("b")], count: 2, unread_count: 0, has_more: true, next_cursor: "C1" }))
      .mockResolvedValueOnce(ok({ rows: [row("c")], count: 1, unread_count: 0, has_more: false }))
    const { result } = renderHook(() => useInboxPages("ws", { space: "history" }), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.items.map((i) => i.id)).toEqual(["a", "b"]))
    expect(result.current.hasMore).toBe(true)

    act(() => result.current.loadMore())
    await waitFor(() => expect(result.current.items.map((i) => i.id)).toEqual(["a", "b", "c"]))
    expect(result.current.hasMore).toBe(false)

    const [first, second] = urls()
    expect(first.searchParams.get("space")).toBe("history")
    expect(first.searchParams.get("state")).toBe("all")
    expect(first.searchParams.has("cursor")).toBe(false)
    expect(first.searchParams.has("offset")).toBe(false)
    expect(second.searchParams.get("cursor")).toBe("C1")
  })

  it("sends the trimmed search text to the server, and nothing when it is blank", async () => {
    apiFetch.mockResolvedValue(ok({ rows: [row("hit")], count: 1, unread_count: 0, has_more: false }))
    const { result } = renderHook(() => useInboxPages("ws", { q: "  deploy " }), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.items).toHaveLength(1))
    expect(urls()[0].searchParams.get("q")).toBe("deploy")

    apiFetch.mockClear()
    const blank = renderHook(() => useInboxPages("ws", { q: "   " }), { wrapper: wrapper() })
    await waitFor(() => expect(blank.result.current.items).toHaveLength(1))
    expect(urls()[0].searchParams.has("q")).toBe(false)
  })

  it("makes no request while disabled", async () => {
    const { result } = renderHook(() => useInboxPages("ws", { q: "x" }, false), { wrapper: wrapper() })
    expect(result.current.items).toEqual([])
    expect(result.current.loading).toBe(false)
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe("useInboxCounts facets", () => {
  it("asks for one space and the caller's day end, and keeps the facet maps", async () => {
    apiFetch.mockResolvedValueOnce(ok({
      total: 3, unread: 2, spaces: { action: 3, updates: 0, history: 0 }, by_kind: {},
      by_attention: { decision: 3 }, by_deadline: { soon: 1 },
    }))
    const { result } = renderHook(() => useInboxCounts("ws", { space: "action", dayEnd: "2026-10-19T21:59:59.999Z" }), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.data?.by_attention).toEqual({ decision: 3 }))
    expect(result.current.data?.by_deadline).toEqual({ soon: 1 })
    const url = urls()[0]
    expect(url.searchParams.get("space")).toBe("action")
    expect(url.searchParams.get("day_end")).toBe("2026-10-19T21:59:59.999Z")
  })
})

describe("useDebouncedValue", () => {
  it("follows the value only after it has been still for the delay", () => {
    vi.useFakeTimers()
    const { result, rerender } = renderHook(({ v }) => useDebouncedValue(v, 300), { initialProps: { v: "a" } })
    rerender({ v: "ab" })
    act(() => { vi.advanceTimersByTime(200) })
    rerender({ v: "abc" })
    act(() => { vi.advanceTimersByTime(200) })
    expect(result.current).toBe("a")
    act(() => { vi.advanceTimersByTime(100) })
    expect(result.current).toBe("abc")
  })
})
