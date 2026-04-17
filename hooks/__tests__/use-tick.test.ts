import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"
import { useTick } from "@/hooks/use-tick"

describe("useTick", () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("starts at 0", () => {
    const { result } = renderHook(() => useTick())
    expect(result.current).toBe(0)
  })

  it("increments once per interval", () => {
    const { result } = renderHook(() => useTick(1000))
    expect(result.current).toBe(0)

    act(() => { vi.advanceTimersByTime(1000) })
    expect(result.current).toBe(1)

    act(() => { vi.advanceTimersByTime(3000) })
    expect(result.current).toBe(4)
  })

  it("honours a custom interval", () => {
    const { result } = renderHook(() => useTick(250))
    act(() => { vi.advanceTimersByTime(1000) })
    expect(result.current).toBe(4)
  })

  it("does not schedule a timer when interval is zero or negative", () => {
    // Zero interval: the early-return branch in the effect prevents any setInterval.
    const before = vi.getTimerCount()
    const { result: zero } = renderHook(() => useTick(0))
    const { result: neg } = renderHook(() => useTick(-5))

    expect(vi.getTimerCount()).toBe(before)
    // Advancing time cannot tick the counters.
    act(() => { vi.advanceTimersByTime(10_000) })
    expect(zero.current).toBe(0)
    expect(neg.current).toBe(0)
  })

  it("clears the interval on unmount", () => {
    const { unmount } = renderHook(() => useTick(100))
    const before = vi.getTimerCount()
    unmount()
    expect(vi.getTimerCount()).toBeLessThan(before)
  })

  it("resets the timer when intervalMs changes", () => {
    const { result, rerender } = renderHook(
      ({ interval }: { interval: number }) => useTick(interval),
      { initialProps: { interval: 1000 } },
    )

    act(() => { vi.advanceTimersByTime(1000) })
    expect(result.current).toBe(1)

    rerender({ interval: 500 })
    act(() => { vi.advanceTimersByTime(500) })
    // One tick under the new interval.
    expect(result.current).toBeGreaterThanOrEqual(2)
  })
})
