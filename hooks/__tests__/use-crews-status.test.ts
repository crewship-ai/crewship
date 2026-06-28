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
