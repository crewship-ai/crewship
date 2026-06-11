import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useJournalList } from "@/hooks/use-journal-list"
import type { JournalEntry } from "@/lib/types/journal"

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function err(status: number): Response {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

function entry(id: string): JournalEntry {
  return {
    id,
    workspace_id: "ws_test",
    ts: "2026-04-30T10:00:00Z",
    entry_type: "peer.escalation",
    severity: "warn",
    actor_type: "agent",
    summary: "summary " + id,
  } as JournalEntry
}

let mockFetch: ReturnType<typeof vi.fn>

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("useJournalList — maxEntries cap", () => {
  async function mountWithEntries(maxEntries: number | undefined, seed: string[]) {
    mockFetch.mockResolvedValueOnce(
      okJSON({ entries: seed.map(entry), next_cursor: null }),
    )
    const hook = renderHook(() =>
      useJournalList({ workspaceId: "ws_test", maxEntries }),
    )
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    return hook
  }

  it("prependLive trims the tail past maxEntries", async () => {
    const { result } = await mountWithEntries(2, ["j1", "j2"])
    act(() => {
      result.current.prependLive(entry("live1"))
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["live1", "j1"])
  })

  it("maxEntries=0 is a hard cap — nothing is retained", async () => {
    const { result } = await mountWithEntries(0, ["j1"])
    act(() => {
      result.current.prependLive(entry("live1"))
    })
    expect(result.current.entries).toEqual([])
  })

  it("fractional maxEntries is floored", async () => {
    const { result } = await mountWithEntries(2.9, ["j1", "j2"])
    act(() => {
      result.current.prependLive(entry("live1"))
    })
    expect(result.current.entries).toHaveLength(2)
  })

  it("negative maxEntries disables the cap", async () => {
    const { result } = await mountWithEntries(-5, ["j1", "j2"])
    act(() => {
      result.current.prependLive(entry("live1"))
    })
    expect(result.current.entries).toHaveLength(3)
  })

  it("NaN maxEntries disables the cap", async () => {
    const { result } = await mountWithEntries(Number.NaN, ["j1", "j2"])
    act(() => {
      result.current.prependLive(entry("live1"))
    })
    expect(result.current.entries).toHaveLength(3)
  })
})

describe("useJournalList — loadMore failure paths", () => {
  async function mountWithCursor() {
    mockFetch.mockResolvedValueOnce(
      okJSON({ entries: [entry("j1")], next_cursor: "cur_2" }),
    )
    const hook = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(hook.result.current.loading).toBe(false))
    expect(hook.result.current.nextCursor).toBe("cur_2")
    return hook
  }

  it("non-ok loadMore response leaves entries and cursor untouched", async () => {
    const { result } = await mountWithCursor()
    mockFetch.mockResolvedValueOnce(err(500))
    await act(async () => {
      await result.current.loadMore()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["j1"])
    expect(result.current.nextCursor).toBe("cur_2")
    expect(result.current.loadingMore).toBe(false)
  })

  it("schema-invalid loadMore response is ignored", async () => {
    const { result } = await mountWithCursor()
    mockFetch.mockResolvedValueOnce(okJSON({ totally: "wrong" }))
    await act(async () => {
      await result.current.loadMore()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["j1"])
    expect(result.current.nextCursor).toBe("cur_2")
  })

  it("loadMore network rejection resets loadingMore (user can retry)", async () => {
    const { result } = await mountWithCursor()
    mockFetch.mockRejectedValueOnce(new Error("offline"))
    await act(async () => {
      await result.current.loadMore()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["j1"])
    expect(result.current.loadingMore).toBe(false)
  })

  it("loadMore passes the cursor in the query", async () => {
    const { result } = await mountWithCursor()
    mockFetch.mockResolvedValueOnce(
      okJSON({ entries: [entry("j2")], next_cursor: null }),
    )
    await act(async () => {
      await result.current.loadMore()
    })
    expect(mockFetch.mock.calls[1][0] as string).toContain("cursor=cur_2")
  })
})

describe("useJournalList — manual refresh guard", () => {
  it("refresh() is a no-op when enabled=false", async () => {
    const { result } = renderHook(() =>
      useJournalList({ workspaceId: "ws_test", enabled: false }),
    )
    await act(async () => {
      await result.current.refresh()
    })
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

describe("useJournalList — stale-response guards", () => {
  it("a late OK response from a superseded refresh is discarded", async () => {
    let resolveFirst!: (r: Response) => void
    const firstResponse = new Promise<Response>((res) => {
      resolveFirst = res
    })
    mockFetch
      .mockReturnValueOnce(firstResponse) // mount refresh — stays in flight
      .mockResolvedValueOnce(okJSON({ entries: [entry("fresh")], next_cursor: "fresh_cur" }))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await act(async () => {
      await Promise.resolve()
    })

    // Second refresh supersedes the first and completes.
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["fresh"])

    // Old response lands last — must not clobber the fresh page.
    await act(async () => {
      resolveFirst(okJSON({ entries: [entry("stale")], next_cursor: null }))
      await Promise.resolve()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["fresh"])
    expect(result.current.nextCursor).toBe("fresh_cur")
  })

  it("a late ERROR response from a superseded refresh sets no error", async () => {
    let resolveFirst!: (r: Response) => void
    const firstResponse = new Promise<Response>((res) => {
      resolveFirst = res
    })
    mockFetch
      .mockReturnValueOnce(firstResponse)
      .mockResolvedValueOnce(okJSON({ entries: [entry("fresh")], next_cursor: null }))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await act(async () => {
      await Promise.resolve()
    })
    await act(async () => {
      await result.current.refresh()
    })

    await act(async () => {
      resolveFirst(err(500))
      await Promise.resolve()
    })
    expect(result.current.error).toBeNull()
    expect(result.current.entries.map((e) => e.id)).toEqual(["fresh"])
    expect(result.current.loading).toBe(false)
  })
})

describe("useJournalList — param serialization edges", () => {
  it("multiple filter params are sorted into a stable key and all land in the URL", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ entries: [], next_cursor: null }))
    renderHook(() =>
      useJournalList({
        workspaceId: "ws_test",
        params: { severity: "warn", crew_id: "c1" },
      }),
    )
    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain("crew_id=c1")
    expect(url).toContain("severity=warn")
  })

  it("an '&' inside a param value drops only the orphaned fragment", async () => {
    mockFetch.mockResolvedValueOnce(okJSON({ entries: [], next_cursor: null }))
    renderHook(() =>
      useJournalList({
        workspaceId: "ws_test",
        params: { search: "a&orphan" },
      }),
    )
    await waitFor(() => expect(mockFetch).toHaveBeenCalled())
    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain("search=a")
    expect(url).not.toContain("orphan")
  })
})
