import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

const realtimeCallbacks: Record<string, (event: unknown) => void> = {}

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: vi.fn(
    (eventType: string, cb: (event: unknown) => void) => {
      realtimeCallbacks[eventType] = cb
    },
  ),
}))

import { renderHook, act } from "@testing-library/react"
import { useCrewsStatus } from "@/hooks/use-crews-status"

async function flushAsync() {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

describe("useCrewsStatus", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it("returns null and does not fetch when workspaceId is null", async () => {
    const { result } = renderHook(() => useCrewsStatus(null))
    await act(async () => { await flushAsync() })

    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current).toBeNull()
  })

  it("fetches crews status on mount and surfaces the record", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ total: 10, running: 2, error: 1, idle: 7, queued: 0 }),
    })

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(mockFetch).toHaveBeenCalledWith("/api/v1/agents/crews-status?workspace_id=ws-1", expect.objectContaining({ credentials: "include" }))
    expect(result.current).toEqual({ total: 10, running: 2, error: 1, idle: 7, queued: 0 })
  })

  it("surfaces queued assignment count from the server", async () => {
    // Phase 1B (PR #396) lets the server report queued assignments
    // separately from agents-in-error. With 3 RUNNING and 9 QUEUED
    // the operator now sees the real shape of in-flight work
    // instead of the legacy "12 errors" lie.
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ total: 12, running: 3, error: 0, idle: 0, queued: 9 }),
    })

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toEqual({ total: 12, running: 3, error: 0, idle: 0, queued: 9 })
  })

  it("normalises a missing queued field to 0 for legacy servers", async () => {
    // The toolbar must keep working when pointed at a pre-#396
    // server that has no notion of QUEUED. Coercing to 0 keeps
    // string formatters from rendering "NaN queued" while leaving
    // the rest of the payload intact.
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ total: 5, running: 1, error: 0, idle: 4 }),
    })

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toEqual({ total: 5, running: 1, error: 0, idle: 4, queued: 0 })
  })

  it("does not update on a non-OK response", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      json: async () => ({ total: 99 }),
    })

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBeNull()
  })

  it("swallows fetch errors so the toolbar never crashes", async () => {
    mockFetch.mockRejectedValueOnce(new Error("boom"))

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBeNull()
  })

  it("debounces a burst of realtime events to a single refresh", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ total: 1, running: 1, error: 0, idle: 0, queued: 0 }),
    })

    renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    act(() => {
      realtimeCallbacks["agent.status"]?.({})
      realtimeCallbacks["agent.created"]?.({})
      realtimeCallbacks["run.completed"]?.({})
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    await act(async () => {
      vi.advanceTimersByTime(150)
      await flushAsync()
    })
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("subscribes to all eight agent/run/queue lifecycle events", () => {
    // Originally six events (agent.* + run.*). PR #396 adds the
    // queue lifecycle pair (assignment_queued / assignment_unqueued)
    // so the toolbar's queued count refreshes the moment the
    // dispatcher parks a job or the pump promotes it to RUNNING,
    // without waiting for the next agent.status nudge.
    mockFetch.mockResolvedValue({ ok: true, json: async () => ({}) })

    renderHook(() => useCrewsStatus("ws-1"))

    for (const ev of [
      "agent.status", "agent.created", "agent.deleted",
      "run.started", "run.completed", "run.failed",
      "assignment_queued", "assignment_unqueued",
    ]) {
      expect(realtimeCallbacks[ev]).toBeTypeOf("function")
    }
  })

  it("refreshes on an assignment_queued event", async () => {
    // First poll: queue empty. After an assignment_queued event the
    // hook should re-fetch (after the 150ms debounce) and pick up
    // the new count. This is the live-update path that keeps the
    // toolbar honest between agent.status events during a queue
    // burst.
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ total: 3, running: 3, error: 0, idle: 0, queued: 0 }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ total: 3, running: 3, error: 0, idle: 0, queued: 5 }),
      })

    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })
    expect(result.current?.queued).toBe(0)

    act(() => {
      realtimeCallbacks["assignment_queued"]?.({})
    })

    await act(async () => {
      vi.advanceTimersByTime(150)
      await flushAsync()
    })

    expect(mockFetch).toHaveBeenCalledTimes(2)
    expect(result.current?.queued).toBe(5)
  })

  it("unmount cancels a pending debounce timer", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ total: 0, running: 0, error: 0, idle: 0, queued: 0 }),
    })

    const { unmount } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    act(() => {
      realtimeCallbacks["agent.status"]?.({})
    })
    unmount()

    await act(async () => {
      vi.advanceTimersByTime(500)
      await flushAsync()
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })
})

// ── Self-healing ───────────────────────────────────────────────────────────
//
// The toolbar pill used to fetch once on mount and then rely ENTIRELY on
// WebSocket events. Miss one — a reconnect, a dropped frame, a tab that was
// backgrounded while a run started and finished — and it reported "Crews
// idle" through an agent actually working, indefinitely, until something else
// happened to nudge it.
//
// That was observable next to the Activity panel, which polls every 6s on top
// of the same events and so corrects itself. Verified against a dev server:
// while an agent ran, crews-status reported running=1 and the runs feed
// reported one running run — the SERVER agreed with itself the whole time, so
// the disagreement on screen was the pill going stale in the browser.
describe("useCrewsStatus — staying true without an event", () => {
  // Own setup: the fake timers and fetch stub above belong to the sibling
  // describe, and this block advances the clock.
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  const ok = (payload: Record<string, number>) => ({ ok: true, json: async () => payload })

  it("re-fetches on an interval, so a missed event self-corrects", async () => {
    fetchMock.mockResolvedValue(ok({ total: 7, running: 0, error: 0, idle: 7, queued: 0 }))
    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })
    expect(result.current?.running).toBe(0)

    // An agent starts, and the event never arrives.
    fetchMock.mockResolvedValue(ok({ total: 7, running: 1, error: 0, idle: 6, queued: 0 }))
    await act(async () => {
      vi.advanceTimersByTime(6000)
      await flushAsync()
    })
    expect(result.current?.running).toBe(1)
  })

  it("stops polling once unmounted", async () => {
    fetchMock.mockResolvedValue(ok({ total: 1, running: 0, error: 0, idle: 1, queued: 0 }))
    const { unmount } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })
    const afterMount = fetchMock.mock.calls.length

    unmount()
    await act(async () => {
      vi.advanceTimersByTime(30_000)
      await flushAsync()
    })
    expect(fetchMock.mock.calls.length).toBe(afterMount)
  })

  it("never polls without a workspace", async () => {
    renderHook(() => useCrewsStatus(null))
    await act(async () => {
      vi.advanceTimersByTime(30_000)
      await flushAsync()
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

// ── Overlapping responses ──────────────────────────────────────────────────
//
// refresh() fires and returns; the 6s interval and the realtime debouncer can
// each start another before the first lands. Nothing deduplicated them and
// nothing checked which workspace a response belonged to, so a slow reply
// could overwrite a newer one — or paint the previous workspace's counts into
// the bar after a switch. Adding the interval made the overlap likelier, which
// is what put this in scope.
describe("useCrewsStatus — overlapping responses", () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  const ok = (payload: Record<string, number>) => ({ ok: true, json: async () => payload })

  it("keeps the newest answer when an older one lands late", async () => {
    let resolveFirst!: (v: unknown) => void
    fetchMock.mockImplementationOnce(() => new Promise((r) => { resolveFirst = r }))
    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    // A second request starts and finishes while the first is still open.
    fetchMock.mockResolvedValue(ok({ total: 7, running: 5, error: 0, idle: 2, queued: 0 }))
    await act(async () => {
      vi.advanceTimersByTime(6000)
      await flushAsync()
    })
    expect(result.current?.running).toBe(5)

    // The stale first reply arrives last. It must not win.
    await act(async () => {
      resolveFirst(ok({ total: 7, running: 0, error: 0, idle: 7, queued: 0 }))
      await flushAsync()
    })
    expect(result.current?.running).toBe(5)
  })

  it("drops a reply for the workspace the user has left", async () => {
    let resolveOld!: (v: unknown) => void
    fetchMock.mockImplementationOnce(() => new Promise((r) => { resolveOld = r }))
    const { result, rerender } = renderHook(
      ({ ws }: { ws: string }) => useCrewsStatus(ws),
      { initialProps: { ws: "ws-1" } },
    )
    await act(async () => { await flushAsync() })

    fetchMock.mockResolvedValue(ok({ total: 2, running: 0, error: 0, idle: 2, queued: 0 }))
    rerender({ ws: "ws-2" })
    await act(async () => { await flushAsync() })
    expect(result.current?.total).toBe(2)

    await act(async () => {
      resolveOld(ok({ total: 99, running: 9, error: 0, idle: 90, queued: 0 }))
      await flushAsync()
    })
    // ws-1's counts must never appear while ws-2 is selected.
    expect(result.current?.total).toBe(2)
  })
})

// ── Whose counts are these? ────────────────────────────────────────────────
//
// The request generation decides who may WRITE, but the value already written
// belongs to whoever asked for it. After a workspace switch the bar kept
// showing the previous workspace's numbers until the new fetch landed — a
// short window, and a confident lie for the whole of it.
//
// And the freshness check ran BEFORE `await res.json()`, so a request that
// lost the race could still win by parsing slowly.
describe("useCrewsStatus — the answer has to be about the right workspace", () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    fetchMock = vi.fn()
    vi.stubGlobal("fetch", fetchMock)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it("reports nothing for a workspace whose counts have not arrived", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ total: 7, running: 0, error: 0, idle: 7, queued: 0 }) })
    const { result, rerender } = renderHook(({ ws }: { ws: string }) => useCrewsStatus(ws), {
      initialProps: { ws: "ws-1" },
    })
    await act(async () => { await flushAsync() })
    expect(result.current?.total).toBe(7)

    // Switch, and hold the new request open.
    fetchMock.mockImplementationOnce(() => new Promise(() => {}))
    rerender({ ws: "ws-2" })
    // ws-1's seven agents must not be attributed to ws-2 for even a frame.
    expect(result.current).toBeNull()
  })

  it("does not let a slow body beat a newer request", async () => {
    let resolveBody!: (v: unknown) => void
    fetchMock.mockImplementationOnce(async () => ({
      ok: true,
      json: () => new Promise((r) => { resolveBody = r }),
    }))
    const { result } = renderHook(() => useCrewsStatus("ws-1"))
    await act(async () => { await flushAsync() })

    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ total: 7, running: 5, error: 0, idle: 2, queued: 0 }) })
    await act(async () => {
      vi.advanceTimersByTime(6000)
      await flushAsync()
    })
    expect(result.current?.running).toBe(5)

    // The first request's BODY finally parses. It lost the race before the
    // fetch resolved; parsing slowly must not hand it the win back.
    await act(async () => {
      resolveBody({ total: 7, running: 0, error: 0, idle: 7, queued: 0 })
      await flushAsync()
    })
    expect(result.current?.running).toBe(5)
  })
})
