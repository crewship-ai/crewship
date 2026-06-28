import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

// Capture the latest callback registered per event type so tests can fire it.
const realtimeCallbacks: Record<string, (event: unknown) => void> = {}

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: vi.fn(
    (eventType: string, cb: (event: unknown) => void) => {
      realtimeCallbacks[eventType] = cb
    },
  ),
}))

import { renderHook, act } from "@testing-library/react"
import { usePendingEscalations } from "@/hooks/use-pending-escalations"

// Flush the fetch → json() → setState chain — each await boundary is one
// microtask tick, so drain a handful.
async function flushAsync() {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve()
  }
}

describe("usePendingEscalations", () => {
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

  it("returns 0 for null workspace and does not fetch", async () => {
    const { result } = renderHook(() => usePendingEscalations(null))
    await act(async () => { await flushAsync() })
    expect(result.current).toBe(0)
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("fetches pending count on mount and surfaces it", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ count: 7 }),
    })

    const { result } = renderHook(() => usePendingEscalations("ws-1"))
    await act(async () => { await flushAsync() })

    expect(mockFetch).toHaveBeenCalledWith(
      "/api/v1/escalations/pending-count?workspace_id=ws-1",
      expect.objectContaining({ credentials: "include" }),
    )
    expect(result.current).toBe(7)
  })

  it("missing count field defaults to 0", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({}),
    })

    const { result } = renderHook(() => usePendingEscalations("ws-1"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBe(0)
  })

  it("non-OK response leaves count untouched", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      json: async () => ({ count: 99 }),
    })

    const { result } = renderHook(() => usePendingEscalations("ws-2"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBe(0)
  })

  it("fetch rejection does not crash the toolbar hook", async () => {
    mockFetch.mockRejectedValueOnce(new Error("network down"))

    const { result } = renderHook(() => usePendingEscalations("ws-3"))
    await act(async () => { await flushAsync() })

    expect(result.current).toBe(0)
  })

  it("debounces a burst of realtime events into a single refresh", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ count: 1 }),
    })

    renderHook(() => usePendingEscalations("ws-4"))
    await act(async () => { await flushAsync() })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    // Three rapid events — all inside the 150 ms window.
    act(() => {
      realtimeCallbacks["escalation.created"]?.({})
      realtimeCallbacks["escalation.created"]?.({})
      realtimeCallbacks["escalation.resolved"]?.({})
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    await act(async () => {
      vi.advanceTimersByTime(150)
      await flushAsync()
    })
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("unmount cancels a pending debounce timer", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ count: 0 }),
    })

    const { unmount } = renderHook(() => usePendingEscalations("ws-5"))
    await act(async () => { await flushAsync() })
    expect(mockFetch).toHaveBeenCalledTimes(1)

    act(() => {
      realtimeCallbacks["escalation.created"]?.({})
    })
    unmount()

    await act(async () => {
      vi.advanceTimersByTime(500)
      await flushAsync()
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("subscribes to both escalation.created and escalation.resolved", () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ count: 0 }),
    })

    renderHook(() => usePendingEscalations("ws-6"))

    expect(realtimeCallbacks["escalation.created"]).toBeTypeOf("function")
    expect(realtimeCallbacks["escalation.resolved"]).toBeTypeOf("function")
  })
})
