import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useJournalList } from "@/hooks/use-journal-list"
import type { JournalEntry } from "@/lib/types/journal"

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function entry(id: string, overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id,
    workspace_id: "ws_test",
    ts: "2026-04-30T10:00:00Z",
    entry_type: "peer.escalation",
    severity: "warn",
    actor_type: "agent",
    summary: "summary " + id,
    ...overrides,
  } as JournalEntry
}

// stubGlobal, not `global.fetch = …`: vi.restoreAllMocks() does not undo a
// direct global assignment, so the mock would outlive this file.
let mockFetch: ReturnType<typeof vi.fn>

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Mount the hook with a known head page and wait for the initial load. */
async function mounted(head: JournalEntry[], opts: { maxEntries?: number } = {}) {
  mockFetch.mockResolvedValueOnce(okJSON({ entries: head, next_cursor: null }))
  let renders = 0
  const hook = renderHook(() => {
    renders++
    return useJournalList({ workspaceId: "ws_test", limit: 50, ...opts })
  })
  await waitFor(() => expect(hook.result.current.loading).toBe(false))
  return { ...hook, renderCount: () => renders }
}

describe("useJournalList — batched prependLive", () => {
  it("accepts an array and lands the newest entry at the head", async () => {
    const { result } = await mounted([entry("old")])

    // Arrival order is chronological (oldest first), matching how a
    // 250 ms SSE flush buffers events.
    act(() => {
      result.current.prependLive([entry("a"), entry("b"), entry("c")])
    })

    expect(result.current.entries.map((e) => e.id)).toEqual(["c", "b", "a", "old"])
  })

  it("matches what one-call-per-entry produced", async () => {
    const batch = [entry("a"), entry("b"), entry("c")]

    const oneByOne = await mounted([entry("old")])
    act(() => {
      for (const e of batch) oneByOne.result.current.prependLive(e)
    })

    const asArray = await mounted([entry("old")])
    act(() => {
      asArray.result.current.prependLive(batch)
    })

    expect(asArray.result.current.entries.map((e) => e.id)).toEqual(
      oneByOne.result.current.entries.map((e) => e.id),
    )
  })

  it("still accepts a single entry", async () => {
    const { result } = await mounted([entry("old")])
    act(() => {
      result.current.prependLive(entry("solo"))
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["solo", "old"])
  })

  it("dedupes against the buffer and within the batch itself", async () => {
    const { result } = await mounted([entry("j1")])
    act(() => {
      result.current.prependLive([entry("j1"), entry("new"), entry("new")])
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["new", "j1"])
  })

  it("applies maxEntries to the whole batch", async () => {
    const { result } = await mounted([entry("o1"), entry("o2")], { maxEntries: 3 })
    act(() => {
      result.current.prependLive([entry("a"), entry("b"), entry("c")])
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["c", "b", "a"])
  })

  it("renders once per batch, not once per entry", async () => {
    const { result, renderCount } = await mounted([entry("old")])
    const before = renderCount()

    act(() => {
      result.current.prependLive(
        Array.from({ length: 50 }, (_, i) => entry(`e${i}`)),
      )
    })

    // One state update -> one re-render. The old signature forced 50.
    expect(renderCount() - before).toBe(1)
    expect(result.current.entries).toHaveLength(51)
  })

  it("an all-duplicate batch keeps the same entries reference", async () => {
    // React can still render a component once before bailing out of an
    // identical setState, so render count is not the assertion — array
    // identity is. It is what the page's memo chain (histogram buckets,
    // stats rail, virtuoso rows) actually keys off.
    const { result } = await mounted([entry("j1"), entry("j2")])
    const before = result.current.entries
    act(() => {
      result.current.prependLive([entry("j1"), entry("j2")])
    })
    expect(result.current.entries).toBe(before)
  })

  it("an empty batch does not re-render", async () => {
    const { result, renderCount } = await mounted([entry("j1")])
    const before = renderCount()
    act(() => {
      result.current.prependLive([])
    })
    expect(renderCount()).toBe(before)
  })
})
