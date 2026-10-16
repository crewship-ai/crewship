import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { useNativePageSearch } from "@/hooks/use-native-page-search"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetch = vi.mocked(apiFetch)
const match = (id: string) => ({ page_id: id, slug: id, name: id, content_type: "slides", schema_version: 2, source_revision: 1, source_generation: 0, snippet: "needle" })
const response = (id: string, cursor?: string) => new Response(JSON.stringify({ scope: "native-docs-slides", matches: [match(id)], has_more: Boolean(cursor), ...(cursor ? { cursor } : {}) }), { status: 200 })
const deferred = () => { let resolve!: (value: Response) => void; const promise = new Promise<Response>(done => { resolve = done }); return { promise, resolve } }
const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (name: string, callback: () => void) => { events.set(name, callback) } }))
beforeEach(() => { fetch.mockReset(); events.clear() })

describe("native Page content search authority lifecycle", () => {
  it("masks old viewer results synchronously and ignores late response across workspace changes", async () => {
    fetch.mockResolvedValueOnce(response("first-page"))
    const { result, rerender } = renderHook(({ workspace, viewer }) => useNativePageSearch(workspace, viewer, true), { initialProps: { workspace: "workspace-a", viewer: "viewer-a" } })
    await act(async () => { await result.current.search("needle") })
    expect(result.current.matches[0].page_id).toBe("first-page")
    const pending = deferred(); fetch.mockReturnValueOnce(pending.promise)
    let work!: Promise<void>
    act(() => { work = result.current.search("needle") })
    const signal = fetch.mock.calls[1][1]?.signal
    rerender({ workspace: "workspace-b", viewer: "viewer-b" })
    expect(result.current.matches).toEqual([])
    expect(signal?.aborted).toBe(true)
    await act(async () => { pending.resolve(response("late-private-page")); await work })
    expect(result.current.matches).toEqual([])
    fetch.mockResolvedValueOnce(response("new-viewer-page"))
    await act(async () => { await result.current.search("needle") })
    expect(result.current.matches[0].page_id).toBe("new-viewer-page")
    expect(String(fetch.mock.calls[2][0])).toContain("workspace_id=workspace-b")
  })

  it("uses opaque continuation and drops previous metadata before denied/new batch", async () => {
    const cursor = "a".repeat(43)
    fetch.mockResolvedValueOnce(response("previous-page", cursor))
    const { result } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
    await act(async () => { await result.current.search("needle & second") })
    const pending = deferred(); fetch.mockReturnValueOnce(pending.promise)
    let work!: Promise<void>
    act(() => { work = result.current.search("needle & second", true) })
    expect(result.current.matches).toEqual([])
    expect(String(fetch.mock.calls[1][0])).toContain("cursor=" + cursor)
    expect(new URL(String(fetch.mock.calls[1][0]), "http://localhost").searchParams.get("q")).toBe("needle & second")
    await act(async () => { pending.resolve(new Response("{}", { status: 403 })); await work })
    expect(result.current.matches).toEqual([])
    expect(result.current.cursor).toBeUndefined()
    expect(result.current.complete).toBe(false)
    expect(result.current.error).toContain("access")
  })

  it("clear/unmount abort late promises and unsupported/incomplete responses never mean no results", async () => {
    const pending = deferred(); fetch.mockReturnValueOnce(pending.promise)
    const { result, unmount } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
    let work!: Promise<void>
    act(() => { work = result.current.search("needle") })
    act(() => result.current.clear())
    expect(fetch.mock.calls[0][1]?.signal?.aborted).toBe(true)
    await act(async () => { pending.resolve(response("obsolete")); await work })
    expect(result.current.matches).toEqual([])
    fetch.mockResolvedValueOnce(new Response("{}", { status: 503 }))
    await act(async () => { await result.current.search("needle") })
    expect(result.current.complete).toBe(false)
    expect(result.current.error).toContain("not ready")
    fetch.mockResolvedValueOnce(new Response(JSON.stringify({ scope: "all-pages", matches: [], has_more: false }), { status: 200 }))
    await act(async () => { await result.current.search("needle") })
    expect(result.current.complete).toBe(false)
    expect(result.current.error).toContain("unsupported response")
    const last = deferred(); fetch.mockReturnValueOnce(last.promise)
    act(() => { work = result.current.search("needle") })
    unmount()
    expect(fetch.mock.calls.at(-1)?.[1]?.signal?.aborted).toBe(true)
    last.resolve(response("unmounted")); await work
  })

  it("invalidates existing visible snippets on access/content events and focus without retrying", async () => {
    fetch.mockResolvedValue(response("private-page"))
    const { result } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
    await act(async () => { await result.current.search("needle") })
    expect(result.current.matches[0].page_id).toBe("private-page")
    act(() => events.get("page.folder.updated")?.())
    expect(result.current.matches).toEqual([])
    expect(result.current.complete).toBe(false)
    expect(result.current.error).toContain("Search again")
    expect(fetch).toHaveBeenCalledTimes(1)
    await act(async () => { await result.current.search("needle") })
    act(() => window.dispatchEvent(new Event("focus")))
    expect(result.current.matches).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(2)
    await act(async () => { await result.current.search("needle") })
    act(() => events.get("realtime.reconnected")?.())
    expect(result.current.matches).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(3)
  })

  it("expires idle results after bounded lifetime without an automatic 503 retry loop", async () => {
    vi.useFakeTimers()
    try {
      fetch.mockResolvedValueOnce(response("private-page"))
      const { result, unmount } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
      await act(async () => { await result.current.search("needle") })
      expect(result.current.matches).toHaveLength(1)
      act(() => vi.advanceTimersByTime(15000))
      expect(result.current.matches).toEqual([])
      expect(result.current.error).toContain("Search again")
      expect(fetch).toHaveBeenCalledTimes(1)
      unmount()
    } finally { vi.useRealTimers() }
  })

  it("an older batch expiry cannot clear a newer query result", async () => {
    vi.useFakeTimers()
    try {
      fetch.mockResolvedValueOnce(response("old-page")).mockResolvedValueOnce(response("new-page"))
      const { result, unmount } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
      await act(async () => { await result.current.search("old needle") })
      act(() => vi.advanceTimersByTime(14000))
      await act(async () => { await result.current.search("new needle") })
      act(() => vi.advanceTimersByTime(1000))
      expect(result.current.matches[0].page_id).toBe("new-page")
      act(() => vi.advanceTimersByTime(14000))
      expect(result.current.matches).toEqual([])
      expect(fetch).toHaveBeenCalledTimes(2)
      unmount()
    } finally { vi.useRealTimers() }
  })

  it("accepts matched rich Doc3 responses but rejects unknown Slides3", async () => {
    const wire = (type: string) => new Response(JSON.stringify({ scope: "native-docs-slides", matches: [{ ...match("rich-page"), content_type: type, schema_version: 3 }], has_more: false }), { status: 200 })
    fetch.mockResolvedValueOnce(wire("document")).mockResolvedValueOnce(wire("slides"))
    const { result } = renderHook(() => useNativePageSearch("workspace", "viewer", true))
    await act(async () => { await result.current.search("needle") })
    expect(result.current.matches[0].schema_version).toBe(3)
    await act(async () => { await result.current.search("needle") })
    expect(result.current.matches).toEqual([])
    expect(result.current.complete).toBe(false)
    expect(result.current.error).toContain("unsupported response")
  })

  it("disabled or signed-out contexts never send a request", async () => {
    const { result, rerender } = renderHook(({ enabled, viewer }) => useNativePageSearch("workspace", viewer, enabled), { initialProps: { enabled: false, viewer: "viewer" as string | null } })
    await act(async () => { await result.current.search("needle") })
    rerender({ enabled: true, viewer: null })
    await act(async () => { await result.current.search("needle") })
    expect(fetch).not.toHaveBeenCalled()
    await waitFor(() => expect(result.current.matches).toEqual([]))
  })
})
