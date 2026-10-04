import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { usePageProjectHistory } from "@/hooks/use-page-project-history"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const realtime = vi.mocked(useRealtimeEventSafe)
const revision = { revision: 7, digest: "digest7", git_commit: "commit7", actor: "author", created_at: "2026-10-02T00:00:00Z", restorable: true }
const history = { revisions: [revision], next_before: 0 }
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }
function setup(workspace = "ws", slug = "page") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { client, ...renderHook((props) => usePageProjectHistory(props.workspace, props.slug), { initialProps: { workspace, slug }, wrapper }) }
}
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })

describe("usePageProjectHistory", () => {
  it("loads newest revisions and preserves source/actor/restore metadata", async () => {
    fetchMock.mockResolvedValue(response(history))
    const { result } = setup("ws /&", "page/#")
    expect(result.current.query.isPending).toBe(true)
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    expect(result.current.query.data?.pages).toEqual([history])
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/pages/page%2F%23/project/history?workspace_id=ws+%2F%26")
    expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
    expect(result.current.query.hasNextPage).toBe(false)
  })

  it("pages using the server's before cursor and stops when the server returns zero", async () => {
    fetchMock.mockResolvedValueOnce(response({ ...history, next_before: 7 }))
      .mockResolvedValueOnce(response({ revisions: [{ ...revision, revision: 3, restorable: false }], next_before: 0 }))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.hasNextPage).toBe(true))
    await act(async () => { await result.current.query.fetchNextPage() })
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/pages/page/project/history?workspace_id=ws&before=7")
    await waitFor(() => expect(result.current.query.data?.pages.flatMap((p) => p.revisions.map((r) => r.revision))).toEqual([7, 3]))
    await waitFor(() => expect(result.current.query.hasNextPage).toBe(false))
    await act(async () => { await result.current.query.fetchNextPage() })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it.each(["message", "empty", "invalid-json"])("surfaces history errors with a useful fallback (%s)", async (kind) => {
    fetchMock.mockResolvedValue(kind === "invalid-json" ? new Response("invalid", { status: 500 })
      : response(kind === "message" ? { error: "History retention storage unavailable" } : {}, 503))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.error?.message).toBe(kind === "message" ? "History retention storage unavailable" : "Could not load source history."))
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it.each(["page.updated", "page.deleted", "realtime.reconnected"])("refreshes the scoped history on %s", async (kind) => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(response({ revisions: [], next_before: 0 }))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    const callback = realtime.mock.calls.filter(([event]) => event === kind).at(-1)?.[1]
    expect(callback).toBeDefined()
    act(() => callback!({} as never))
    await waitFor(() => expect(result.current.query.data?.pages[0].revisions).toEqual([]))
    expect(invalidation).toHaveBeenCalledWith({ queryKey: ["page-project-history", "ws", "page"] })
  })

  it("restores a chosen revision with a compare-and-swap fence and refreshes history plus preview", async () => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(response({ revision: 8 }))
      .mockResolvedValueOnce(response({ revisions: [{ ...revision, revision: 8 }], next_before: 0 }))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    await act(async () => { await result.current.restore.mutateAsync({ revision: 3, expectedRevision: 7 }) })
    expect(fetchMock.mock.calls[1]).toEqual(["/api/v1/pages/page/project/restore?workspace_id=ws", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ revision: 3, expected_revision: 7 }),
    }])
    expect(invalidation).toHaveBeenCalledWith({ queryKey: ["page-project-history", "ws", "page"] })
    expect(invalidation).toHaveBeenCalledWith({ queryKey: ["page-preview", "ws", "page"] })
    await waitFor(() => expect(result.current.query.data?.pages[0].revisions[0].revision).toBe(8))
  })

  it.each(["message", "empty", "invalid-json"])("refetches authoritative state after a rejected restore (%s)", async (kind) => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(kind === "invalid-json"
      ? new Response("invalid", { status: 500 }) : response(kind === "message" ? { error: "Draft revision changed" } : {}, 409))
      .mockResolvedValueOnce(response(history))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    const message = kind === "message" ? "Draft revision changed" : "Could not restore source revision."
    await act(async () => { await expect(result.current.restore.mutateAsync({ revision: 3, expectedRevision: 7 })).rejects.toThrow(message) })
    expect(invalidation).toHaveBeenCalledWith({ queryKey: ["page-project-history", "ws", "page"] })
    expect(invalidation).toHaveBeenCalledWith({ queryKey: ["page-preview", "ws", "page"] })
  })

  it("aborts the old workspace's history request and ignores its late response", async () => {
    let resolve!: (value: Response) => void
    fetchMock.mockReturnValueOnce(new Promise((r) => { resolve = r })).mockResolvedValueOnce(response({ revisions: [], next_before: 0 }))
    const { result, rerender } = setup()
    const signal = fetchMock.mock.calls[0][1]!.signal!
    rerender({ workspace: "other-ws", slug: "page" })
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    expect(signal.aborted).toBe(true)
    await act(async () => { resolve(response(history)) })
    expect(result.current.query.data?.pages[0].revisions).toEqual([])
  })
})
