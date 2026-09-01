import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { useJournalStream } from "@/hooks/use-journal-stream"

// In-memory EventSource double (happy-dom ships none). Duplicated from
// use-journal-stream.test.ts rather than shared, matching what
// use-journal-stream.cov.test.ts already does — the existing files are not
// modified by this change.
class MockEventSource {
  static instances: MockEventSource[] = []
  url: string
  readyState = 0
  listeners = new Map<string, Set<(ev: MessageEvent) => void>>()
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null

  constructor(url: string) {
    this.url = url
    MockEventSource.instances.push(this)
  }
  addEventListener(type: string, fn: (ev: MessageEvent) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)!.add(fn)
  }
  removeEventListener(type: string, fn: (ev: MessageEvent) => void) {
    this.listeners.get(type)?.delete(fn)
  }
  dispatch(type: string, data: unknown) {
    const ev = {
      data: typeof data === "string" ? data : JSON.stringify(data),
    } as MessageEvent
    if (type === "message" && this.onmessage) this.onmessage(ev)
    this.listeners.get(type)?.forEach((fn) => fn(ev))
  }
  open() {
    this.readyState = 1
    this.onopen?.(new Event("open"))
  }
  fail() {
    this.onerror?.(new Event("error"))
  }
  close() {
    this.readyState = 2
  }
}

function entry(id: string, ts: string) {
  return {
    id,
    workspace_id: "ws_test",
    ts,
    entry_type: "peer.escalation",
    severity: "warn",
    actor_type: "agent",
    summary: "test " + id,
  }
}

function okResponse(entries: unknown[], nextCursor?: string) {
  return {
    ok: true,
    status: 200,
    json: async () => ({ entries, next_cursor: nextCursor ?? null }),
  } as unknown as Response
}

/** The hook's poll page size. A page that comes back exactly this full is
 *  the signal that the backfill was truncated. */
const POLL_LIMIT = 50

let mockFetch: ReturnType<typeof vi.fn>

beforeEach(() => {
  MockEventSource.instances = []
  vi.stubGlobal("EventSource", MockEventSource)
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  // Pin jitter to its maximum so the backoff delay is the deterministic
  // upper bound of its window and a test can advance past it exactly.
  vi.spyOn(Math, "random").mockReturnValue(0.999999)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Fake timers, but with shouldAdvanceTime so React's own scheduling still
 *  runs — the pattern the existing cov suite uses. Every assertion below
 *  that depends on state goes through act()/waitFor rather than reading
 *  straight after an advance. */
function useHybridTimers() {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"))
}

describe("useJournalStream — reconnect", () => {
  it("re-opens the stream after a backoff instead of polling forever", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const onEntry = vi.fn()
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )

    act(() => {
      MockEventSource.instances[0].open()
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))
    // Nothing re-opened yet — the retry is scheduled, not immediate.
    expect(MockEventSource.instances).toHaveLength(1)

    // First backoff window is base 1000 ms with equal jitter -> at most 1000.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100)
    })
    expect(MockEventSource.instances.length).toBeGreaterThanOrEqual(2)

    act(() => {
      MockEventSource.instances[MockEventSource.instances.length - 1].open()
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))
  })

  it("stops polling once the stream is back", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100)
    })
    act(() => {
      MockEventSource.instances[MockEventSource.instances.length - 1].open()
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))

    const callsAtReconnect = mockFetch.mock.calls.length
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20000)
    })
    expect(mockFetch.mock.calls.length).toBe(callsAtReconnect)
  })

  it("backs off exponentially and never exceeds the ceiling", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }))

    const failLatest = () =>
      act(() => {
        MockEventSource.instances[MockEventSource.instances.length - 1].fail()
      })

    failLatest()
    // Attempt 1 lands inside 1000 ms; attempt 2 must NOT land that fast.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1100)
    })
    expect(MockEventSource.instances).toHaveLength(2)

    failLatest()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(900)
    })
    expect(MockEventSource.instances).toHaveLength(2) // 2000 ms window, not yet
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1300)
    })
    expect(MockEventSource.instances).toHaveLength(3)

    // Drive the backoff well past the ceiling: every subsequent attempt must
    // still arrive within one ceiling window, i.e. it stops doubling.
    for (let i = 0; i < 8; i++) {
      failLatest()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30100)
      })
    }
    expect(MockEventSource.instances).toHaveLength(11)
  })

  it("reconnect() retries immediately without waiting for the backoff", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    act(() => {
      result.current.reconnect()
    })
    expect(MockEventSource.instances).toHaveLength(2)
    await waitFor(() => expect(result.current.status).toBe("connecting"))
  })

  it("falls back to Polling when a manual reconnect also fails", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    act(() => {
      result.current.reconnect()
    })
    await waitFor(() => expect(result.current.status).toBe("connecting"))

    // The poll timer is still running from the first failure, so a naive
    // "already polling, nothing to do" guard would leave the badge stuck on
    // Connecting for the rest of the outage.
    act(() => {
      MockEventSource.instances[1].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))
  })

  it("does not start a second poll cycle while one is still in flight", async () => {
    useHybridTimers()
    // A cursor walk that never resolves: the next tick must skip, not
    // launch a competing walk off the same watermark.
    mockFetch.mockImplementation(() => new Promise<Response>(() => {}))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )
    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(20000)
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("reconnects on real timers too (the fake-timer test is not lying)", async () => {
    // No fake timers here on purpose: a backoff implemented with anything
    // the fake clock does not drive would still pass above.
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await waitFor(() => expect(MockEventSource.instances.length).toBe(2), {
      timeout: 3000,
    })
  }, 10000)
})

describe("useJournalStream — backfill gap", () => {
  it("advances the poll watermark from SSE entries, not just polled ones", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    act(() => {
      MockEventSource.instances[0].open()
      MockEventSource.instances[0].dispatch("entry", entry("live1", "2026-01-01T00:20:00.000Z"))
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    const url = mockFetch.mock.calls[0][0] as string
    // Must be the newest SSE ts, not the mount time — re-requesting from
    // mount silently discards everything the stream already delivered.
    expect(url).toContain(`since=${encodeURIComponent("2026-01-01T00:20:00.000Z")}`)
  })

  it("compares watermarks by instant, not by string", async () => {
    useHybridTimers()
    mockFetch.mockResolvedValue(okResponse([]))
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )

    // The API serialises ts as RFC3339Nano, which strips trailing zeros, so
    // an entry landing exactly on a second has no fractional part at all.
    // Lexicographically "…01.500Z" sorts BELOW "…01Z" ('.' < 'Z'), so a
    // string compare refuses to advance past a whole second — and the poll
    // that follows re-requests from before entries it already delivered.
    act(() => {
      MockEventSource.instances[0].open()
      MockEventSource.instances[0].dispatch("entry", entry("a", "2026-01-01T00:10:01Z"))
      MockEventSource.instances[0].dispatch("entry", entry("b", "2026-01-01T00:10:01.500Z"))
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })

    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain(`since=${encodeURIComponent("2026-01-01T00:10:01.500Z")}`)
  })

  it("walks the cursor to backfill a gap larger than one page", async () => {
    useHybridTimers()
    // Newest first, the order docs/api-reference/journal.mdx defines and
    // queries.go emits (`ORDER BY ts DESC, id DESC`). Building the page
    // oldest-first would test the hook against a shape no server sends, and
    // an implementation that reverses each page instead of the whole walk
    // would pass anyway.
    const page1 = Array.from({ length: POLL_LIMIT }, (_, i) =>
      entry(
        `p1_${POLL_LIMIT - 1 - i}`,
        `2026-01-01T00:01:${String(POLL_LIMIT - 1 - i).padStart(2, "0")}.000Z`,
      ),
    )
    mockFetch
      .mockResolvedValueOnce(okResponse(page1, "cursor-1"))
      .mockResolvedValueOnce(okResponse([entry("p2_0", "2026-01-01T00:00:30.000Z")]))
      .mockResolvedValue(okResponse([]))

    const onEntry = vi.fn()
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )
    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })

    expect(mockFetch.mock.calls.length).toBeGreaterThanOrEqual(2)
    expect(mockFetch.mock.calls[1][0] as string).toContain("cursor=cursor-1")
    expect(onEntry).toHaveBeenCalledTimes(POLL_LIMIT + 1)
    // Oldest first across the WHOLE catch-up, not per page: the second page
    // is older than the first, so it must be delivered before it. Asserting
    // the complete sequence rather than just its head — checking only the
    // first id passes for an implementation that reverses each page
    // separately and hands back p1 newest-first behind it.
    expect(onEntry.mock.calls.map(([e]: [{ id: string }]) => e.id)).toEqual([
      "p2_0",
      ...Array.from({ length: POLL_LIMIT }, (_, i) => `p1_${i}`),
    ])
    // A gap that the cursor walk closed is not reported as lost.
    expect(result.current.gapDetected).toBe(false)
  })

  it("reports a gap when the catch-up walk hits its page ceiling", async () => {
    useHybridTimers()
    // Every page comes back full and hands out another cursor: the backlog
    // is deeper than the hook is willing to walk in one tick.
    let n = 0
    mockFetch.mockImplementation(async () => {
      const page = Array.from({ length: POLL_LIMIT }, (_, i) =>
        entry(`g${n}_${i}`, "2026-01-01T00:10:00.000Z"),
      )
      n++
      return okResponse(page, `cursor-${n}`)
    })

    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )
    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    await waitFor(() => expect(result.current.gapDetected).toBe(true))
    expect(result.current.lastError).toMatch(/missing/i)
    // Bounded: the walk stops rather than paging the whole workspace.
    expect(mockFetch.mock.calls.length).toBeLessThanOrEqual(8)
  })

  it("keeps the gap warning when the stream comes back on its own", async () => {
    useHybridTimers()
    mockFetch.mockImplementation(async () =>
      okResponse(
        Array.from({ length: POLL_LIMIT }, (_, i) => entry(`x${i}`, "2026-01-01T00:10:00.000Z")),
        "c",
      ),
    )
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )
    act(() => {
      MockEventSource.instances[0].fail()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    await waitFor(() => expect(result.current.gapDetected).toBe(true))

    // The backoff reconnects and succeeds. That restores the live tail but
    // does NOT fill the hole, so the warning and its explanation have to
    // survive — only re-reading the head can clear it.
    act(() => {
      MockEventSource.instances[MockEventSource.instances.length - 1].open()
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))
    expect(result.current.gapDetected).toBe(true)
    expect(result.current.lastError).toMatch(/missing/i)
  })

  it("reconnect() clears the gap warning", async () => {
    useHybridTimers()
    mockFetch.mockImplementation(async () =>
      okResponse(
        Array.from({ length: POLL_LIMIT }, (_, i) => entry(`x${i}`, "2026-01-01T00:10:00.000Z")),
        "c",
      ),
    )
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry: vi.fn() }),
    )
    act(() => {
      MockEventSource.instances[0].fail()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    await waitFor(() => expect(result.current.gapDetected).toBe(true))

    act(() => {
      result.current.reconnect()
    })
    await waitFor(() => expect(result.current.gapDetected).toBe(false))
  })
})
