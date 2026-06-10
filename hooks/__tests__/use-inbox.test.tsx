import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useInbox, useInboxUnreadCount, inboxKeys, type InboxItem } from "@/hooks/use-inbox"
import * as realtime from "@/hooks/use-realtime"

// Replace the realtime layer with an in-test event bus: useRealtimeEvent
// registers callbacks, __emitRealtime fires them. This lets the tests
// assert WS-event-driven invalidation without standing up a WebSocket
// or the RealtimeProvider context.
vi.mock("@/hooks/use-realtime", async () => {
  const ReactMod = await import("react")
  const listeners = new Map<string, Set<(event: unknown) => void>>()
  return {
    useRealtimeEvent: (type: string, cb: (event: unknown) => void) => {
      const cbRef = ReactMod.useRef(cb)
      cbRef.current = cb
      ReactMod.useEffect(() => {
        let set = listeners.get(type)
        if (!set) {
          set = new Set()
          listeners.set(type, set)
        }
        const fn = (event: unknown) => cbRef.current(event)
        set.add(fn)
        return () => { set.delete(fn) }
      }, [type])
    },
    __emitRealtime: (type: string) => {
      listeners.get(type)?.forEach((fn) =>
        fn({ type, payload: {}, timestamp: new Date() }),
      )
    },
  }
})

const emitRealtime = (
  realtime as unknown as { __emitRealtime: (type: string) => void }
).__emitRealtime

function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, body: unknown): Response {
  return {
    ok: false,
    status,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function item(overrides: Partial<InboxItem>): InboxItem {
  return {
    id: "i1",
    workspace_id: "ws-1",
    kind: "escalation",
    source_id: "src-1",
    title: "needs attention",
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: "2026-06-01T00:00:00Z",
    updated_at: "2026-06-01T00:00:00Z",
    ...overrides,
  }
}

describe("useInbox", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  it("is disabled without a workspaceId — no fetch, empty items", async () => {
    const { result } = renderHook(() => useInbox(null), {
      wrapper: makeWrapper(qc),
    })
    await act(async () => { await Promise.resolve() })
    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current.items).toEqual([])
    expect(result.current.unreadCount).toBe(0)
  })

  it("fetches the workspace list with a state filter and caches under the canonical key", async () => {
    const rows = [item({ id: "i1" }), item({ id: "i2" })]
    mockFetch.mockResolvedValueOnce(okJSON({ rows, count: 2, unread_count: 2 }))

    const { result } = renderHook(() => useInbox("ws-1", "unread"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current.items).toHaveLength(2))

    expect(mockFetch.mock.calls[0][0]).toBe("/api/v1/inbox?workspace_id=ws-1&state=unread")
    expect(result.current.unreadCount).toBe(2)
    expect(qc.getQueryData(inboxKeys.list("ws-1", "unread"))).toBeTruthy()
  })

  it("omits the state param for the 'all' filter (and when no filter given)", async () => {
    mockFetch.mockResolvedValue(okJSON({ rows: [], count: 0, unread_count: 0 }))

    const { result } = renderHook(() => useInbox("ws-1", "all"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(mockFetch.mock.calls[0][0]).toBe("/api/v1/inbox?workspace_id=ws-1")
  })

  it("surfaces a non-ok list response as the same `inbox: <status>` error string", async () => {
    mockFetch.mockResolvedValueOnce(errJSON(500, {}))

    const { result } = renderHook(() => useInbox("ws-1", "unread"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current.error).toBe("inbox: 500"))
    expect(result.current.items).toEqual([])
  })

  it("refetches when inbox.updated / escalation.created / pipeline.waitpoint.created fire", async () => {
    mockFetch.mockResolvedValue(okJSON({ rows: [], count: 0, unread_count: 0 }))

    const { result } = renderHook(() => useInbox("ws-1", "unread"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(mockFetch).toHaveBeenCalledTimes(1)

    act(() => { emitRealtime("inbox.updated") })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))

    act(() => { emitRealtime("escalation.created") })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(3))

    act(() => { emitRealtime("pipeline.waitpoint.created") })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(4))
  })

  describe("patch", () => {
    it("PATCHes the item and drops it from a filtered list when the new state no longer matches", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ rows: [item({ id: "i1" }), item({ id: "i2" })], count: 2, unread_count: 2 }),
      )
      const { result } = renderHook(() => useInbox("ws-1", "unread"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.items).toHaveLength(2))

      mockFetch.mockResolvedValueOnce(okJSON({ ok: true }))
      await act(async () => { await result.current.patch("i1", "read") })

      const [url, init] = mockFetch.mock.calls[1] as [string, RequestInit]
      expect(url).toBe("/api/v1/inbox/i1?workspace_id=ws-1")
      expect(init.method).toBe("PATCH")
      expect(JSON.parse(init.body as string)).toEqual({ state: "read" })

      // i1 left the unread filter; badge decremented. (The cache write
      // notifies observers in a batched microtask — waitFor flushes it.)
      await waitFor(() =>
        expect(result.current.items.map((it) => it.id)).toEqual(["i2"]),
      )
      expect(result.current.unreadCount).toBe(1)
    })

    it("keeps the row (with resolved_action) when the new state still matches the filter", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ rows: [item({ id: "i1", state: "read" })], count: 1, unread_count: 0 }),
      )
      const { result } = renderHook(() => useInbox("ws-1", "all"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.items).toHaveLength(1))

      mockFetch.mockResolvedValueOnce(okJSON({ ok: true }))
      await act(async () => { await result.current.patch("i1", "resolved", "acknowledged") })

      await waitFor(() => expect(result.current.items[0].state).toBe("resolved"))
      expect(result.current.items[0].resolved_action).toBe("acknowledged")
      expect(result.current.unreadCount).toBe(0)
    })

    it("propagates server errors to the caller AND the error field (409 source-managed guard)", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ rows: [item({ id: "i1" })], count: 1, unread_count: 1 }),
      )
      const { result } = renderHook(() => useInbox("ws-1", "unread"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.items).toHaveLength(1))

      mockFetch.mockResolvedValueOnce(errJSON(409, { error: "waitpoint items are source-managed" }))
      await expect(
        act(async () => { await result.current.patch("i1", "resolved", "acknowledged") }),
      ).rejects.toThrow("waitpoint items are source-managed")

      await waitFor(() =>
        expect(result.current.error).toBe("waitpoint items are source-managed"),
      )
      // List untouched — no optimistic mutation happened before the error.
      expect(result.current.items).toHaveLength(1)
      expect(result.current.unreadCount).toBe(1)
    })
  })
})

describe("useInboxUnreadCount", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  it("returns 0 without a workspaceId and does not fetch", async () => {
    const { result } = renderHook(() => useInboxUnreadCount(null), {
      wrapper: makeWrapper(qc),
    })
    await act(async () => { await Promise.resolve() })
    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current).toBe(0)
  })

  it("fetches the dedicated count endpoint", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ unread_count: 4 }))
    const { result } = renderHook(() => useInboxUnreadCount("ws-1"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current).toBe(4))
    expect(mockFetch.mock.calls[0][0]).toBe("/api/v1/inbox/count?workspace_id=ws-1")
  })

  it("refetches on inbox.updated and keeps the last good value when the refetch fails", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ unread_count: 4 }))
    const { result } = renderHook(() => useInboxUnreadCount("ws-1"), {
      wrapper: makeWrapper(qc),
    })
    await waitFor(() => expect(result.current).toBe(4))

    // Badge updates instantly when the event lands…
    mockFetch.mockResolvedValueOnce(okJSON({ unread_count: 6 }))
    act(() => { emitRealtime("inbox.updated") })
    await waitFor(() => expect(result.current).toBe(6))

    // …and a failed refetch never blanks the badge.
    mockFetch.mockResolvedValueOnce(errJSON(500, {}))
    act(() => { emitRealtime("inbox.updated") })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(3))
    expect(result.current).toBe(6)
  })
})
