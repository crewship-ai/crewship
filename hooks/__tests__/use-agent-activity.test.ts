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
import { useAgentActivity } from "@/hooks/use-agent-activity"

function fireLog(payload: Record<string, unknown>) {
  const cb = realtimeCallbacks["agent.log"]
  if (!cb) throw new Error("agent.log listener not registered")
  cb({ payload })
}

// Advance past the 500 ms flush interval and let React flush the resulting setState.
async function flushTick() {
  await act(async () => {
    vi.advanceTimersByTime(500)
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe("useAgentActivity", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("registers an agent.log listener and starts with an empty map", () => {
    const { result } = renderHook(() => useAgentActivity())
    expect(result.current.size).toBe(0)
    expect(realtimeCallbacks["agent.log"]).toBeTypeOf("function")
  })

  it("ignores events without agent slug or content", async () => {
    const { result } = renderHook(() => useAgentActivity())

    act(() => {
      fireLog({ content: "no agent" })
      fireLog({ agent: "lucie" })
      fireLog({})
    })
    await flushTick()

    expect(result.current.size).toBe(0)
  })

  it("reads the 'agent' field in preference to 'agent_slug'", async () => {
    const { result } = renderHook(() => useAgentActivity())

    act(() => {
      fireLog({ agent: "lucie", agent_slug: "other-slug", content: "hi" })
    })
    await flushTick()

    expect(result.current.get("lucie")).toBe("hi")
    expect(result.current.has("other-slug")).toBe(false)
  })

  it("falls back to 'agent_slug' when 'agent' is absent", async () => {
    const { result } = renderHook(() => useAgentActivity())

    act(() => {
      fireLog({ agent_slug: "viktor", content: "scripting something" })
    })
    await flushTick()

    expect(result.current.get("viktor")).toBe("scripting something")
  })

  it("truncates snippets longer than 80 chars with an ellipsis", async () => {
    const { result } = renderHook(() => useAgentActivity())

    const long = "x".repeat(120)
    act(() => {
      fireLog({ agent: "eva", content: long })
    })
    await flushTick()

    const got = result.current.get("eva")!
    expect(got.length).toBe(80)
    expect(got.endsWith("...")).toBe(true)
    expect(got.startsWith("x".repeat(77))).toBe(true)
  })

  it("leaves snippets exactly 80 chars long untruncated", async () => {
    const { result } = renderHook(() => useAgentActivity())

    const eighty = "y".repeat(80)
    act(() => {
      fireLog({ agent: "eva", content: eighty })
    })
    await flushTick()

    expect(result.current.get("eva")).toBe(eighty)
  })

  it("keeps only the latest snippet per agent across multiple logs", async () => {
    const { result } = renderHook(() => useAgentActivity())

    act(() => {
      fireLog({ agent: "lucie", content: "first" })
      fireLog({ agent: "lucie", content: "second" })
      fireLog({ agent: "lucie", content: "third" })
    })
    await flushTick()

    expect(result.current.size).toBe(1)
    expect(result.current.get("lucie")).toBe("third")
  })

  it("flush is a no-op when nothing has changed since last tick", async () => {
    const { result } = renderHook(() => useAgentActivity())

    act(() => {
      fireLog({ agent: "lucie", content: "hi" })
    })
    await flushTick()
    const first = result.current

    // Nothing new fired; next flush must not replace the map reference needlessly.
    await flushTick()
    expect(result.current).toBe(first)
  })

  it("prunes entries older than 30 s on the next flush after new activity", async () => {
    const { result } = renderHook(() => useAgentActivity())

    // First log at t0
    act(() => {
      fireLog({ agent: "lucie", content: "old" })
    })
    await flushTick()
    expect(result.current.get("lucie")).toBe("old")

    // Advance 30 s — lucie's entry is now stale.
    await act(async () => {
      vi.advanceTimersByTime(30_000)
      await Promise.resolve()
    })

    // A new log from viktor marks dirty; lucie is pruned on this flush.
    act(() => {
      fireLog({ agent: "viktor", content: "fresh" })
    })
    await flushTick()

    expect(result.current.has("lucie")).toBe(false)
    expect(result.current.get("viktor")).toBe("fresh")
  })

  it("clears the flush interval on unmount", () => {
    const { unmount } = renderHook(() => useAgentActivity())

    // setInterval is called once by the flush effect.
    const before = vi.getTimerCount()
    unmount()
    const after = vi.getTimerCount()

    expect(after).toBeLessThan(before)
  })
})
