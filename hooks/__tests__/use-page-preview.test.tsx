import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider, focusManager } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { pagePreviewKeys, usePagePreview, type PagePreview } from "@/hooks/use-page-preview"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const realtime = vi.mocked(useRealtimeEventSafe)
const preview: PagePreview = {
  revision: 7, runtime_url: "https://runtime.example", build: { id: "build7", source_revision: 7, state: "ready" },
  artifact: { format: "crewship-page-preview/v1", javascript: "compiled", css: "styles", toolchain: "v1" },
}
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }
function setup(workspace = "ws", slug = "page") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { client, ...renderHook((props) => usePagePreview(props.workspace, props.slug), { initialProps: { workspace, slug }, wrapper }) }
}
function emit(kind: string) {
  const callback = realtime.mock.calls.filter(([event]) => event === kind).at(-1)?.[1]
  expect(callback).toBeDefined()
  act(() => callback!({} as never))
}
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })
afterEach(() => { vi.useRealTimers(); focusManager.setFocused(undefined) })

describe("usePagePreview", () => {
  it("loads the scoped draft preview and forwards a cancellable signal", async () => {
    fetchMock.mockResolvedValue(response(preview))
    const { result } = setup("ws /&", "page/#")
    expect(result.current.query.isPending).toBe(true)
    await waitFor(() => expect(result.current.query.data).toEqual(preview))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("/api/v1/pages/page%2F%23/project/preview?workspace_id=ws+%2F%26")
    expect(init?.signal).toBeInstanceOf(AbortSignal)
    expect(pagePreviewKeys.detail("ws", "page")).toEqual(["page-preview", "ws", "page"])
  })

  it.each([
    [404, "This Page has no application draft yet. Ask an agent to create one."],
    [503, "Application previews are not enabled on this installation."],
    [403, "You need permission to edit this Page to open its application preview."],
    [500, "Could not load the application preview."],
  ])("explains preview failure %i without automatic retries", async (status, message) => {
    fetchMock.mockResolvedValue(response({}, status))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.error?.message).toBe(message))
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it.each(["page.updated", "page.deleted", "realtime.reconnected"])("refreshes only the matching Page on %s", async (kind) => {
    fetchMock.mockResolvedValueOnce(response(preview)).mockResolvedValueOnce(response({ ...preview, revision: 8 }))
    const { client, result } = setup()
    client.setQueryDefaults(pagePreviewKeys.detail("other-ws", "page"), { gcTime: Infinity })
    client.setQueryDefaults(pagePreviewKeys.detail("ws", "other-page"), { gcTime: Infinity })
    client.setQueryData(pagePreviewKeys.detail("other-ws", "page"), preview)
    client.setQueryData(pagePreviewKeys.detail("ws", "other-page"), preview)
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    emit(kind)
    await waitFor(() => expect(result.current.query.data?.revision).toBe(8))
    expect(client.getQueryState(pagePreviewKeys.detail("other-ws", "page"))?.isInvalidated).toBe(false)
    expect(client.getQueryState(pagePreviewKeys.detail("ws", "other-page"))?.isInvalidated).toBe(false)
  })

  it("starts a fenced build and clears the previous executable artifact before refetch completes", async () => {
    const job = { id: "build8", source_revision: 8, state: "running" as const }
    fetchMock.mockResolvedValueOnce(response(preview)).mockResolvedValueOnce(response(job))
      .mockReturnValueOnce(new Promise(() => {}))
    const { result, client } = setup()
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    await act(async () => { await result.current.build.mutateAsync(8) })
    const [url, init] = fetchMock.mock.calls[1]
    expect(url).toBe("/api/v1/pages/page/project/build?workspace_id=ws")
    expect(init).toMatchObject({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ expected_revision: 8 }) })
    expect(client.getQueryData(pagePreviewKeys.detail("ws", "page"))).toEqual({ revision: 7, runtime_url: preview.runtime_url, build: job })
    await waitFor(() => expect(result.current.query.data?.artifact).toBeUndefined())
  })

  it("uses the new build revision when building before the first preview resolves", async () => {
    fetchMock.mockImplementation(async (_url, init) => init?.method === "POST"
      ? response({ id: "build9", source_revision: 9, state: "running" }) : new Promise(() => {}))
    const { result, client } = setup()
    await act(async () => { await result.current.build.mutateAsync(9) })
    expect(client.getQueryData(pagePreviewKeys.detail("ws", "page"))).toMatchObject({ revision: 9, runtime_url: "", build: { id: "build9" } })
  })

  it.each(["json", "invalid-json"])("surfaces build failures (%s) and retains the previous artifact when no build started", async (kind) => {
    fetchMock.mockResolvedValueOnce(response(preview)).mockResolvedValueOnce(kind === "json"
      ? response({ error: "The draft changed. Refresh before building." }, 409) : new Response("invalid", { status: 500 }))
    const { result } = setup()
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
    const message = kind === "json" ? "The draft changed. Refresh before building." : "Could not start the preview build."
    await act(async () => { await expect(result.current.build.mutateAsync(8)).rejects.toThrow(message) })
    expect(result.current.query.data).toEqual(preview)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("cancels obsolete previews and isolates results after a workspace switch", async () => {
    let resolve!: (value: Response) => void
    fetchMock.mockReturnValueOnce(new Promise((r) => { resolve = r })).mockResolvedValueOnce(response({ ...preview, revision: 20 }))
    const { result, rerender } = setup()
    const signal = fetchMock.mock.calls[0][1]!.signal!
    rerender({ workspace: "new-ws", slug: "page" })
    await waitFor(() => expect(result.current.query.data?.revision).toBe(20))
    expect(signal.aborted).toBe(true)
    await act(async () => { resolve(response(preview)) })
    expect(result.current.query.data?.revision).toBe(20)
  })

  it("polls running builds as a foreground backstop and stops once the build is ready", async () => {
    vi.useFakeTimers()
    focusManager.setFocused(true)
    fetchMock.mockResolvedValueOnce(response({ ...preview, build: { ...preview.build!, state: "running" } }))
      .mockResolvedValueOnce(response(preview))
    const { result, unmount } = setup()
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(result.current.query.data?.build?.state).toBe("running")
    await act(async () => { await vi.advanceTimersByTimeAsync(60_001) })
    expect(result.current.query.data?.build?.state).toBe("ready")
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    unmount()
  })
})
