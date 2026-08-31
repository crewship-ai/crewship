import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

import { useBatchedPrepend, PREPEND_FLUSH_MS } from "@/hooks/use-batched-prepend"
import type { JournalEntry } from "@/lib/types/journal"

function entry(id: string): JournalEntry {
  return {
    id,
    workspace_id: "ws_test",
    ts: "2026-04-30T10:00:00Z",
    entry_type: "container.metrics",
    severity: "info",
    actor_type: "system",
    summary: "metrics " + id,
  } as JournalEntry
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("useBatchedPrepend", () => {
  it("coalesces a burst into a single array call", async () => {
    const prepend = vi.fn()
    const { result } = renderHook(() => useBatchedPrepend(prepend))

    act(() => {
      for (let i = 0; i < 50; i++) result.current(entry(`e${i}`))
    })
    expect(prepend).not.toHaveBeenCalled()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS)
    })
    expect(prepend).toHaveBeenCalledTimes(1)
    const batch = prepend.mock.calls[0][0] as JournalEntry[]
    expect(batch).toHaveLength(50)
    // Arrival order preserved — the consumer reverses it, not this hook.
    expect(batch[0].id).toBe("e0")
    expect(batch[49].id).toBe("e49")
  })

  it("starts a fresh window after each flush", async () => {
    const prepend = vi.fn()
    const { result } = renderHook(() => useBatchedPrepend(prepend))

    act(() => {
      result.current(entry("a"))
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS)
    })
    act(() => {
      result.current(entry("b"))
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS)
    })

    expect(prepend).toHaveBeenCalledTimes(2)
    expect((prepend.mock.calls[1][0] as JournalEntry[]).map((e) => e.id)).toEqual(["b"])
  })

  it("does not fire when nothing arrived", async () => {
    const prepend = vi.fn()
    renderHook(() => useBatchedPrepend(prepend))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS * 4)
    })
    expect(prepend).not.toHaveBeenCalled()
  })

  it("keeps the same callback identity across renders", () => {
    const { result, rerender } = renderHook(
      ({ fn }: { fn: (e: JournalEntry | JournalEntry[]) => void }) => useBatchedPrepend(fn),
      { initialProps: { fn: vi.fn() } },
    )
    const first = result.current
    // A caller that rebuilds prependLive every render must not churn the
    // subscription this callback is handed to.
    rerender({ fn: vi.fn() })
    expect(result.current).toBe(first)
  })

  it("uses the latest prependLive when the window closes", async () => {
    const first = vi.fn()
    const second = vi.fn()
    const { result, rerender } = renderHook(
      ({ fn }: { fn: (e: JournalEntry | JournalEntry[]) => void }) => useBatchedPrepend(fn),
      { initialProps: { fn: first } },
    )
    act(() => {
      result.current(entry("a"))
    })
    rerender({ fn: second })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS)
    })
    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledTimes(1)
  })

  it("drops the pending window on unmount instead of writing to a dead list", async () => {
    const prepend = vi.fn()
    const { result, unmount } = renderHook(() => useBatchedPrepend(prepend))
    act(() => {
      result.current(entry("a"))
    })
    unmount()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(PREPEND_FLUSH_MS * 2)
    })
    expect(prepend).not.toHaveBeenCalled()
  })
})
