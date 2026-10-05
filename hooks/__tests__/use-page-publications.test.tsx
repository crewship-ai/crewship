import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { usePagePublications, type PublicationReceipt } from "@/hooks/use-page-publications"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const realtime = vi.mocked(useRealtimeEventSafe)
const receipt: PublicationReceipt = {
  version: 5, build_id: "build7", source_revision: 7, source_digest: "source7", artifact_digest: "artifact7",
  git_commit: "commit7", actor: "author", created_at: "2026-10-02T00:00:00Z", rollback_of: 0, withdrawn_at: "",
}
const history = { publications: [receipt], publication_version: 5, published: true, can_publish: true, next_before: 0 }
const source = { revision: 7, git_commit: "commit7", project: { files: [{ path: "src/App.tsx", encoding: "utf8", content: "archived" }] } }
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }
function setup(workspace = "ws", slug = "page", revision?: number) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { client, ...renderHook((props) => usePagePublications(props.workspace, props.slug, props.revision), { initialProps: { workspace, slug, revision }, wrapper }) }
}
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })

describe("usePagePublications", () => {
  it("loads publication receipts with scoped escaped transport and preserves current/withdrawn metadata", async () => {
    fetchMock.mockResolvedValue(response(history))
    const { result } = setup("ws /&", "page/#")
    expect(result.current.query.isPending).toBe(true)
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    expect(result.current.query.data?.pages).toEqual([history])
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/pages/page%2F%23/project/publications?workspace_id=ws+%2F%26")
    expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
    expect(result.current.query.hasNextPage).toBe(false)
  })

  it("fetches older receipts with the server's publication cursor", async () => {
    fetchMock.mockResolvedValueOnce(response({ ...history, next_before: 5 })).mockResolvedValueOnce(response({ ...history, publications: [{ ...receipt, version: 2 }], next_before: 0 }))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.hasNextPage).toBe(true))
    await act(async () => { await result.current.query.fetchNextPage() })
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/pages/page/project/publications?workspace_id=ws&before=5")
    await waitFor(() => expect(result.current.query.hasNextPage).toBe(false))
    expect(result.current.query.data?.pages.flatMap((p) => p.publications.map((r) => r.version))).toEqual([5, 2])
  })

  it.each([undefined, 0])("does not request archived source without a selected revision (%s)", async (revision) => {
    fetchMock.mockResolvedValue(response(history))
    const { result } = setup("ws", "page", revision)
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    expect(result.current.source.fetchStatus).toBe("idle")
    expect(result.current.source.data).toBeUndefined()
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it("loads the explicitly selected archived source rather than substituting current draft bytes", async () => {
    fetchMock.mockImplementation(async (url) => response(String(url).includes("/history/") ? source : history))
    const { result, rerender } = setup()
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    rerender({ workspace: "ws", slug: "page", revision: 7 })
    await waitFor(() => expect(result.current.source.data).toEqual(source))
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/pages/page/project/history/7?workspace_id=ws")
    expect(fetchMock.mock.calls[1][1]?.signal).toBeInstanceOf(AbortSignal)
  })

  it.each(["message", "empty"])("surfaces publication history errors (%s) without retries", async (kind) => {
    fetchMock.mockResolvedValue(response(kind === "message" ? { error: "Publication storage unavailable" } : {}, 503))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.error?.message).toBe(kind === "message" ? "Publication storage unavailable" : "Could not load application history."))
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it("reports pruned archived source without replacing it with the current draft", async () => {
    fetchMock.mockImplementation(async (url) => String(url).includes("/history/") ? response({ error: "Source revision no longer retained" }, 404) : response(history))
    const { result } = setup("ws", "page", 7)
    await waitFor(() => expect(result.current.source.error?.message).toBe("Source revision no longer retained"))
    expect(result.current.source.data).toBeUndefined()
    expect(result.current.query.data?.pages).toEqual([history])
  })

  it.each(["page.updated", "page.deleted", "realtime.reconnected"])("refreshes live publication state on %s and invalidates only this workspace", async (kind) => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(response({ ...history, published: false }))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    const callback = realtime.mock.calls.filter(([event]) => event === kind).at(-1)?.[1]
    expect(callback).toBeDefined()
    act(() => callback!({} as never))
    await waitFor(() => expect(result.current.query.data?.pages[0].published).toBe(false))
    expect(invalidation.mock.calls.map(([args]) => args?.queryKey)).toEqual([["page-publications", "ws"], ["page-application", "ws"], ["pages", "ws"]])
  })

  it("withdraws exactly the expected live publication and refreshes readers' cached state", async () => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(response({ published: false, publication_version: 5 }))
      .mockResolvedValueOnce(response({ ...history, published: false }))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    await act(async () => { await result.current.withdraw.mutateAsync(5) })
    expect(fetchMock.mock.calls[1]).toEqual(["/api/v1/pages/page/project/unpublish?workspace_id=ws", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expected_publication: 5 }),
    }])
    expect(invalidation.mock.calls.map(([args]) => args?.queryKey)).toEqual([["page-publications", "ws"], ["page-application", "ws"], ["pages", "ws"]])
    await waitFor(() => expect(result.current.query.data?.pages[0].published).toBe(false))
  })

  it("refreshes authoritative live state even when withdrawal fails its version fence", async () => {
    fetchMock.mockResolvedValueOnce(response(history)).mockResolvedValueOnce(response({ error: "Publication version changed" }, 409))
      .mockResolvedValueOnce(response({ ...history, publication_version: 6 }))
    const { result, client } = setup()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    await act(async () => { await expect(result.current.withdraw.mutateAsync(5)).rejects.toThrow("Publication version changed") })
    expect(invalidation).toHaveBeenCalledTimes(3)
    await waitFor(() => expect(result.current.query.data?.pages[0].publication_version).toBe(6))
  })

  it("aborts the old archived source request when selecting another revision", async () => {
    let resolve!: (value: Response) => void
    fetchMock.mockImplementation((url) => {
      if (String(url).includes("/history/7?")) return new Promise((r) => { resolve = r })
      return Promise.resolve(response(String(url).includes("/history/") ? { ...source, revision: 8 } : history))
    })
    const { result, rerender } = setup("ws", "page", 7)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const signal = fetchMock.mock.calls.find(([url]) => String(url).includes("/history/7?"))![1]!.signal!
    rerender({ workspace: "ws", slug: "page", revision: 8 })
    await waitFor(() => expect(result.current.source.data?.revision).toBe(8))
    expect(signal.aborted).toBe(true)
    await act(async () => { resolve(response(source)) })
    expect(result.current.source.data?.revision).toBe(8)
  })
})
