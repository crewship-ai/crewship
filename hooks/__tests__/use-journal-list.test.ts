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

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useJournalList", () => {
  it("loads first page on mount with workspace_id + limit in query", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(
      okJSON({ entries: [entry("j1"), entry("j2")], next_cursor: "cursor_2" }),
    )

    const { result } = renderHook(() =>
      useJournalList({ workspaceId: "ws_test", limit: 50 }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.entries).toHaveLength(2)
    expect(result.current.nextCursor).toBe("cursor_2")

    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain("workspace_id=ws_test")
    expect(url).toContain("limit=50")
  })

  it("does not fetch when workspaceId is null", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() => useJournalList({ workspaceId: null }))

    // Wait a tick — the effect runs but should bail early.
    await act(async () => {
      await Promise.resolve()
    })
    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current.entries).toEqual([])
  })

  it("does not fetch when enabled=false", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    renderHook(() => useJournalList({ workspaceId: "ws_test", enabled: false }))

    await act(async () => {
      await Promise.resolve()
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("404 surfaces as empty result, NOT as error", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(err(404))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.entries).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("5xx sets error message", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(err(500))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toMatch(/Failed.*500/)
  })

  it("malformed response → empty entries (graceful)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ wrong: 1 }))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.entries).toEqual([])
  })

  it("loadMore appends next page and dedupes by id", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(
        okJSON({ entries: [entry("j1"), entry("j2")], next_cursor: "cur_2" }),
      )
      .mockResolvedValueOnce(
        // page 2 includes a duplicate id (j2) which must be dropped
        okJSON({ entries: [entry("j2"), entry("j3")], next_cursor: null }),
      )

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await act(async () => {
      await result.current.loadMore()
    })
    expect(result.current.entries.map((e) => e.id)).toEqual(["j1", "j2", "j3"])
    expect(result.current.nextCursor).toBeNull()
  })

  it("loadMore is a no-op when nextCursor is null", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ entries: [entry("j1")], next_cursor: null }))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await act(async () => {
      await result.current.loadMore()
    })
    // Only the initial fetch.
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("prependLive prepends a new entry to the head", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ entries: [entry("j1")], next_cursor: null }),
    )

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      result.current.prependLive(entry("j_live"))
    })
    expect(result.current.entries[0].id).toBe("j_live")
    expect(result.current.entries[1].id).toBe("j1")
  })

  it("prependLive dedupes — same id is not re-added", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ entries: [entry("j1")], next_cursor: null }),
    )

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      result.current.prependLive(entry("j1"))
    })
    expect(result.current.entries).toHaveLength(1)
  })

  it("filter param changes trigger a refetch", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ entries: [], next_cursor: null }))

    const { rerender } = renderHook(
      ({ filter }: { filter: { entry_type?: string } }) =>
        useJournalList({ workspaceId: "ws_test", params: filter }),
      { initialProps: { filter: { entry_type: "peer.escalation" } } },
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect((fetchMock.mock.calls[0][0] as string)).toContain("entry_type=peer.escalation")

    rerender({ filter: { entry_type: "summary.generated" } })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect((fetchMock.mock.calls[1][0] as string)).toContain("entry_type=summary.generated")
  })

  it("undefined / empty filter values are dropped from query", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ entries: [], next_cursor: null }))

    renderHook(() =>
      useJournalList({
        workspaceId: "ws_test",
        params: { entry_type: undefined, search: "" },
      }),
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())

    const url = fetchMock.mock.calls[0][0] as string
    expect(url).not.toContain("entry_type=")
    expect(url).not.toContain("search=")
  })

  it("network rejection → empty entries (no crash)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error("offline"))

    const { result } = renderHook(() => useJournalList({ workspaceId: "ws_test" }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.entries).toEqual([])
  })
})
