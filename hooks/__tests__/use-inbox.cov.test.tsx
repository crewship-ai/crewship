import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useInbox, type InboxItem } from "@/hooks/use-inbox"

// Coverage companion for use-inbox.test.tsx — covers the manual
// refresh() helper (invalidate → refetch), the no-workspace guards, the
// unread-count bookkeeping on a read→unread flip, and PATCH error arms.

// In-test event bus mirroring the one in use-inbox.test.tsx.
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

import * as realtime from "@/hooks/use-realtime"
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

describe("useInbox refresh()", () => {
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

  it("invalidates the workspace inbox cache and refetches fresh rows", async () => {
    mockFetch
      .mockResolvedValueOnce(okJSON({ rows: [item({ id: "i1" })], count: 1, unread_count: 1 }))
      .mockResolvedValueOnce(
        okJSON({ rows: [item({ id: "i1" }), item({ id: "i2" })], count: 2, unread_count: 2 }),
      )

    const { result } = renderHook(() => useInbox("ws-1"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))
    expect(result.current.unreadCount).toBe(1)

    await act(async () => {
      await result.current.refresh()
    })

    await waitFor(() => expect(result.current.items).toHaveLength(2))
    expect(result.current.unreadCount).toBe(2)
    expect(mockFetch).toHaveBeenCalledTimes(2)
    // Both fetches hit the same canonical list URL.
    expect(mockFetch.mock.calls[0][0]).toBe("/api/v1/inbox?workspace_id=ws-1")
    expect(mockFetch.mock.calls[1][0]).toBe("/api/v1/inbox?workspace_id=ws-1")
  })

  it("refresh() is a no-op without a workspaceId", async () => {
    const { result } = renderHook(() => useInbox(null), { wrapper: makeWrapper(qc) })
    await act(async () => {
      await result.current.refresh()
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("realtime invalidation is a no-op without a workspaceId", async () => {
    renderHook(() => useInbox(null), { wrapper: makeWrapper(qc) })
    await act(async () => {
      emitRealtime("inbox.updated")
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

describe("useInbox patch()", () => {
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

  it("patch() is a no-op without a workspaceId", async () => {
    const { result } = renderHook(() => useInbox(null), { wrapper: makeWrapper(qc) })
    await act(async () => {
      await result.current.patch("i1", "read")
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("flipping a read item back to unread increments the badge count", async () => {
    mockFetch.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return Promise.resolve(okJSON({}))
      }
      return Promise.resolve(
        okJSON({
          rows: [item({ id: "i1", state: "read" }), item({ id: "i2", state: "unread" })],
          count: 2,
          unread_count: 1,
        }),
      )
    })

    const { result } = renderHook(() => useInbox("ws-1"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(2))
    expect(result.current.unreadCount).toBe(1)

    await act(async () => {
      await result.current.patch("i1", "unread")
    })

    // The query-cache notification lands a microtask after mutateAsync
    // settles — wait for the observer to flush.
    await waitFor(() => expect(result.current.unreadCount).toBe(2))
    expect(result.current.items.find((i) => i.id === "i1")?.state).toBe("unread")
    const patchCall = mockFetch.mock.calls.find(([, init]) => (init as RequestInit)?.method === "PATCH")!
    expect(patchCall[0]).toBe("/api/v1/inbox/i1?workspace_id=ws-1")
    expect(JSON.parse((patchCall[1] as RequestInit).body as string)).toMatchObject({ state: "unread" })
  })

  it("surfaces a PATCH failure with a non-JSON body as 'patch failed (status)'", async () => {
    mockFetch.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return Promise.resolve({
          ok: false,
          status: 500,
          json: async () => {
            throw new SyntaxError("not json")
          },
        } as unknown as Response)
      }
      return Promise.resolve(okJSON({ rows: [item({ id: "i1" })], count: 1, unread_count: 1 }))
    })

    const { result } = renderHook(() => useInbox("ws-1"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    await act(async () => {
      await expect(result.current.patch("i1", "resolved")).rejects.toThrow("patch failed (500)")
    })
    await waitFor(() => expect(result.current.error).toBe("patch failed (500)"))
    // The cached list is untouched by the failed mutation.
    expect(result.current.items[0].state).toBe("unread")
  })

  it("a successful patch with no cached list does not crash the reconciler", async () => {
    mockFetch.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "PATCH") {
        return Promise.resolve(okJSON({}))
      }
      // List fetch never settles — cache entry for the key stays empty.
      return new Promise<Response>(() => {})
    })

    const { result } = renderHook(() => useInbox("ws-1"), { wrapper: makeWrapper(qc) })
    await act(async () => {
      await result.current.patch("i1", "read")
    })
    expect(result.current.items).toEqual([])
    expect(result.current.error).toBeNull()
  })
})
