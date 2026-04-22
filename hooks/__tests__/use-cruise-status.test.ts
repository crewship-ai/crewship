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
import { useCruiseStatus } from "@/hooks/use-cruise-status"

async function flushAsync() {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

describe("useCruiseStatus", () => {
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
    const { result } = renderHook(() => useCruiseStatus(null))
    await act(async () => { await flushAsync() })

    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current).toBeNull()
  })

  it("fetches cruise status on mount and surfaces the record", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ total: 10, running: 2, error: 1, idle: 7 }),
    })

    const { result } = renderHook(() => useCruiseStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(mockFetch).toHaveBeenCalledWith("/api/v1/agents/cruise-status?workspace_id=ws-1")
    expect(result.current).toEqual({ total: 10, running: 2, error: 1, idle: 7 })
  })

  it("does not update on a non-OK response", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      json: async () => ({ total: 99 }),
    })

    const { result } = renderHook(() => useCruiseStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBeNull()
  })

  it("swallows fetch errors so the toolbar never crashes", async () => {
    mockFetch.mockRejectedValueOnce(new Error("boom"))

    const { result } = renderHook(() => useCruiseStatus("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBeNull()
  })

  it("debounces a burst of realtime events to a single refresh", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ total: 1, running: 1, error: 0, idle: 0 }),
    })

    renderHook(() => useCruiseStatus("ws-1"))
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

  it("subscribes to all six agent/run lifecycle events", () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => ({}) })

    renderHook(() => useCruiseStatus("ws-1"))

    for (const ev of [
      "agent.status", "agent.created", "agent.deleted",
      "run.started", "run.completed", "run.failed",
    ]) {
      expect(realtimeCallbacks[ev]).toBeTypeOf("function")
    }
  })

  it("unmount cancels a pending debounce timer", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ total: 0, running: 0, error: 0, idle: 0 }),
    })

    const { unmount } = renderHook(() => useCruiseStatus("ws-1"))
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
