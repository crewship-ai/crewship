import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

import { useJournalStream } from "@/hooks/use-journal-stream"

// In-memory EventSource double (happy-dom ships none). Mirrors the one in
// use-journal-stream.test.ts; duplicated here because existing test files
// must not be modified.
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

/** EventSource stand-in whose constructor throws — covers the
 *  "browser refuses to even open the stream" fallback path. */
class ThrowingEventSource {
  constructor() {
    throw new Error("EventSource unavailable")
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

let mockFetch: ReturnType<typeof vi.fn>

beforeEach(() => {
  MockEventSource.instances = []
  vi.useFakeTimers({ shouldAdvanceTime: true })
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"))
  vi.stubGlobal("EventSource", MockEventSource)
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("useJournalStream — polling fallback", () => {
  async function startInPollingMode() {
    const onEntry = vi.fn()
    const hook = renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))
    act(() => {
      MockEventSource.instances[0].fail()
    })
    expect(hook.result.current.status).toBe("polling")
    return { onEntry, ...hook }
  }

  it("polls /api/v1/journal every 5s with since watermark + limit=50", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [] }),
    } as unknown as Response)
    await startInPollingMode()

    expect(mockFetch).not.toHaveBeenCalled()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(mockFetch).toHaveBeenCalledTimes(1)
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain("/api/v1/journal?")
    expect(url).toContain("workspace_id=ws_test")
    expect(url).toContain("limit=50")
    // Watermark starts at the (faked) time the effect mounted.
    expect(url).toContain(`since=${encodeURIComponent("2026-01-01T00:00:00.000Z")}`)
  })

  it("delivers polled entries oldest-first and advances the watermark", async () => {
    // Backend returns newest-first; hook must re-emit oldest-first.
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          entries: [
            entry("j2", "2026-01-01T00:00:02.000Z"),
            entry("j1", "2026-01-01T00:00:01.000Z"),
          ],
        }),
      } as unknown as Response)
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ entries: [] }),
      } as unknown as Response)

    const { onEntry } = await startInPollingMode()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).toHaveBeenCalledTimes(2)
    expect(onEntry.mock.calls[0][0].id).toBe("j1")
    expect(onEntry.mock.calls[1][0].id).toBe("j2")

    // Second tick must use the newest seen ts as the watermark.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    const secondUrl = mockFetch.mock.calls[1][0] as string
    expect(secondUrl).toContain(
      `since=${encodeURIComponent("2026-01-01T00:00:02.000Z")}`,
    )
  })

  it("non-ok poll response delivers nothing and the next tick retries", async () => {
    mockFetch
      .mockResolvedValueOnce({
        ok: false,
        status: 503,
        json: async () => ({}),
      } as unknown as Response)
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ entries: [entry("j9", "2026-01-01T00:00:09.000Z")] }),
      } as unknown as Response)

    const { onEntry } = await startInPollingMode()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).not.toHaveBeenCalled()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).toHaveBeenCalledTimes(1)
    expect(onEntry.mock.calls[0][0].id).toBe("j9")
  })

  it("tolerates a rejected poll fetch (next tick retries)", async () => {
    mockFetch
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ entries: [entry("j5", "2026-01-01T00:00:05.000Z")] }),
      } as unknown as Response)

    const { onEntry } = await startInPollingMode()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).not.toHaveBeenCalled()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).toHaveBeenCalledTimes(1)
  })

  it("non-array entries payload is treated as empty", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: "not-an-array" }),
    } as unknown as Response)

    const { onEntry } = await startInPollingMode()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).not.toHaveBeenCalled()
  })

  it("schema-invalid polled entries are skipped, valid ones still delivered", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        entries: [
          entry("ok1", "2026-01-01T00:00:01.000Z"),
          { wrong: "shape" },
        ],
      }),
    } as unknown as Response)

    const { onEntry } = await startInPollingMode()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).toHaveBeenCalledTimes(1)
    expect(onEntry.mock.calls[0][0].id).toBe("ok1")
  })

  it("unmount stops the poll loop", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [] }),
    } as unknown as Response)
    const { unmount } = await startInPollingMode()
    unmount()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000)
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

describe("useJournalStream — EventSource constructor failure", () => {
  it("falls straight back to polling with lastError set", async () => {
    vi.stubGlobal("EventSource", ThrowingEventSource)
    const onEntry = vi.fn()
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )
    expect(result.current.status).toBe("polling")
    expect(result.current.lastError).toBe("Failed to open stream")

    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({ entries: [entry("p1", "2026-01-01T00:00:01.000Z")] }),
    } as unknown as Response)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(onEntry).toHaveBeenCalledWith(expect.objectContaining({ id: "p1" }))
  })
})

describe("useJournalStream — default onmessage handler", () => {
  it("treats messages without an explicit event type as entries", async () => {
    const onEntry = vi.fn()
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))

    act(() => {
      MockEventSource.instances[0].open()
      MockEventSource.instances[0].dispatch(
        "message",
        entry("m1", "2026-01-01T00:00:01.000Z"),
      )
    })
    expect(onEntry).toHaveBeenCalledTimes(1)
    expect(onEntry.mock.calls[0][0].id).toBe("m1")
  })

  it("ignores empty-data and malformed-JSON default messages", async () => {
    const onEntry = vi.fn()
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))

    act(() => {
      MockEventSource.instances[0].open()
      MockEventSource.instances[0].dispatch("message", "")
      MockEventSource.instances[0].dispatch("message", "{not json")
    })
    expect(onEntry).not.toHaveBeenCalled()
  })
})

describe("useJournalStream — cancellation guards", () => {
  it("an error event after unmount does not start polling", async () => {
    const onEntry = vi.fn()
    const { unmount } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )
    const es = MockEventSource.instances[0]
    unmount()

    // Late error from the (already closed) source must be a no-op.
    act(() => {
      es.fail()
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(20_000)
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("a late open event after unmount does not flip status", async () => {
    const onEntry = vi.fn()
    const { result, unmount } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )
    const es = MockEventSource.instances[0]
    expect(result.current.status).toBe("connecting")
    unmount()

    act(() => {
      es.open()
    })
    // Status frozen at the last rendered value — no crash, no transition.
    expect(result.current.status).toBe("connecting")
  })
})

describe("useJournalStream — param serialization edge", () => {
  it("drops a malformed fragment produced by an '&' inside a param value", () => {
    const onEntry = vi.fn()
    renderHook(() =>
      useJournalStream({
        workspaceId: "ws_test",
        params: { entry_type: "a&orphan" },
        onEntry,
      }),
    )
    const url = MockEventSource.instances[0].url
    // "a&orphan" splits into "entry_type=a" + bare "orphan"; the bare
    // fragment has no '=' and must be skipped rather than crash the query.
    expect(url).toContain("entry_type=a")
    expect(url).not.toContain("orphan")
  })
})
