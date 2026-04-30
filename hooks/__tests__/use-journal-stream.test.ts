import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { useJournalStream } from "@/hooks/use-journal-stream"

// Minimal in-memory EventSource double. Lets tests trigger open/error/
// custom events deterministically. happy-dom does not ship one.
class MockEventSource {
  static instances: MockEventSource[] = []
  url: string
  readyState = 0
  listeners = new Map<string, Set<(ev: any) => void>>()
  onopen: ((ev: Event) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null

  constructor(url: string) {
    this.url = url
    MockEventSource.instances.push(this)
  }
  addEventListener(type: string, fn: (ev: any) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)!.add(fn)
  }
  removeEventListener(type: string, fn: (ev: any) => void) {
    this.listeners.get(type)?.delete(fn)
  }
  dispatch(type: string, data: unknown) {
    const ev = { data: typeof data === "string" ? data : JSON.stringify(data) } as MessageEvent
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

beforeEach(() => {
  MockEventSource.instances = []
  ;(global as any).EventSource = MockEventSource
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  delete (global as any).EventSource
})

const validEntry = {
  id: "j1",
  workspace_id: "ws_test",
  ts: "2026-04-30T10:00:00Z",
  entry_type: "peer.escalation",
  severity: "warn",
  actor_type: "agent",
  summary: "test",
}

describe("useJournalStream", () => {
  it("opens an EventSource on mount with workspace_id query", async () => {
    const onEntry = vi.fn()
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))

    expect(MockEventSource.instances).toHaveLength(1)
    expect(MockEventSource.instances[0].url).toContain("workspace_id=ws_test")
  })

  it("does not open EventSource when workspaceId is null", () => {
    const onEntry = vi.fn()
    const { result } = renderHook(() => useJournalStream({ workspaceId: null, onEntry }))

    expect(MockEventSource.instances).toHaveLength(0)
    expect(result.current.status).toBe("idle")
  })

  it("does not open EventSource when enabled=false", () => {
    const onEntry = vi.fn()
    renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry, enabled: false }),
    )
    expect(MockEventSource.instances).toHaveLength(0)
  })

  it("transitions to connected on open and fires onEntry for 'entry' events", async () => {
    const onEntry = vi.fn()
    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )

    act(() => {
      MockEventSource.instances[0].open()
    })
    await waitFor(() => expect(result.current.status).toBe("connected"))

    act(() => {
      MockEventSource.instances[0].dispatch("entry", validEntry)
    })
    expect(onEntry).toHaveBeenCalledWith(expect.objectContaining({ id: "j1" }))
  })

  it("falls back to polling on error", async () => {
    const onEntry = vi.fn()
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ entries: [validEntry] }),
    } as unknown as Response)

    const { result } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )

    act(() => {
      MockEventSource.instances[0].fail()
    })
    await waitFor(() => expect(result.current.status).toBe("polling"))
    expect(result.current.lastError).toMatch(/SSE/)
  })

  it("ignores malformed frames (does not crash)", async () => {
    const onEntry = vi.fn()
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))

    act(() => {
      MockEventSource.instances[0].open()
      // Bad JSON — handler should swallow.
      MockEventSource.instances[0].dispatch("entry", "not-json")
    })

    expect(onEntry).not.toHaveBeenCalled()
  })

  it("schema-invalid entries are silently dropped", async () => {
    const onEntry = vi.fn()
    renderHook(() => useJournalStream({ workspaceId: "ws_test", onEntry }))

    act(() => {
      MockEventSource.instances[0].open()
      MockEventSource.instances[0].dispatch("entry", { wrong: "shape" })
    })
    expect(onEntry).not.toHaveBeenCalled()
  })

  it("closes EventSource on unmount", async () => {
    const onEntry = vi.fn()
    const { unmount } = renderHook(() =>
      useJournalStream({ workspaceId: "ws_test", onEntry }),
    )
    const es = MockEventSource.instances[0]
    expect(es.readyState).toBe(0)

    unmount()
    expect(es.readyState).toBe(2)
  })

  it("filter params land in EventSource URL", () => {
    const onEntry = vi.fn()
    renderHook(() =>
      useJournalStream({
        workspaceId: "ws_test",
        params: { entry_type: "summary.generated", crew_id: "crew_a" },
        onEntry,
      }),
    )

    const url = MockEventSource.instances[0].url
    expect(url).toContain("entry_type=summary.generated")
    expect(url).toContain("crew_id=crew_a")
  })
})
