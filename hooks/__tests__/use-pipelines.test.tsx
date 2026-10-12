import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { usePipelines, usePipelineRuns } from "@/hooks/use-pipelines"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const realtime = vi.mocked(useRealtimeEvent)
const pipeline = { id: "pipe", slug: "daily-etl", name: "Daily ETL", dsl_version: "v1", definition_hash: "hash", ephemeral: false, workspace_visible: true, invocation_count: 2, authored_via: "user_api", created_at: "now", updated_at: "now" }
const entry = { id: "journal", ts: "now", entry_type: "pipeline.step.completed", severity: "info", summary: "Finished", run_id: "run" }
function response(body: unknown, status = 200, headers?: HeadersInit) { return new Response(JSON.stringify(body), { status, headers }) }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })

describe("routine catalog", () => {
  it("loads every advertised page and retains routine metadata", async () => {
    fetchMock.mockResolvedValueOnce(response([pipeline], 200, { "X-Next-Offset": "1" })).mockResolvedValueOnce(response([{ ...pipeline, id: "second" }]))
    const { result } = renderHook(() => usePipelines("ws"))
    await waitFor(() => expect(result.current.pipelines).toHaveLength(2))
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/v1/workspaces/ws/pipelines?limit=200&offset=0", "/api/v1/workspaces/ws/pipelines?limit=200&offset=1"])
    expect(fetchMock.mock.calls.every(([, init]) => init?.signal instanceof AbortSignal)).toBe(true)
    expect(result.current).toMatchObject({ loading: false, error: null })
  })
  it("does not fetch without a workspace", () => {
    const { result } = renderHook(() => usePipelines(null))
    expect(result.current).toMatchObject({ pipelines: [], loading: false, error: null })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each(["pipeline.saved", "pipeline.run.completed", "pipeline.run.failed"])("refreshes on %s", async kind => {
    fetchMock.mockResolvedValueOnce(response([pipeline])).mockResolvedValueOnce(response([]))
    const { result } = renderHook(() => usePipelines("ws"))
    await waitFor(() => expect(result.current.pipelines).toHaveLength(1))
    const cb = realtime.mock.calls.filter(([event]) => event === kind).at(-1)![1]
    await act(async () => { await cb({} as never) })
    expect(result.current.pipelines).toEqual([])
  })
  it.each([new Error("offline"), "transport unavailable"])("reports fetch errors and recovers on refresh (%s)", async failure => {
    fetchMock.mockRejectedValueOnce(failure).mockResolvedValueOnce(response([pipeline]))
    const { result } = renderHook(() => usePipelines("ws"))
    await waitFor(() => expect(result.current.error).toBe(failure instanceof Error ? failure.message : failure))
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ pipelines: [pipeline], loading: false, error: null })
  })
  it("labels HTTP failures as catalog errors", async () => {
    fetchMock.mockResolvedValue(response({}, 503))
    const { result } = renderHook(() => usePipelines("ws"))
    await waitFor(() => expect(result.current.error).toBe("pipelines list: 503"))
  })
  it("ignores late catalog data after a workspace switch", async () => {
    const pending = deferred<Response>()
    fetchMock.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(response([{ ...pipeline, id: "new" }]))
    const { result, rerender } = renderHook(({ ws }) => usePipelines(ws), { initialProps: { ws: "old" } })
    const signal = fetchMock.mock.calls[0][1]!.signal!
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.pipelines[0]?.id).toBe("new"))
    expect(signal.aborted).toBe(true)
    await act(async () => { pending.resolve(response([pipeline])) })
    expect(result.current.pipelines[0]?.id).toBe("new")
  })
  it("settles loading and errors when the workspace is cleared during a request", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => usePipelines(ws), { initialProps: { ws: "ws" as string | null } })
    expect(result.current.loading).toBe(true)
    rerender({ ws: null })
    expect(result.current).toMatchObject({ pipelines: [], loading: false, error: null })
  })
})

describe("routine journal history", () => {
  it("includes bounded step events in the selected routine's journal history", async () => {
    fetchMock.mockResolvedValue(response([entry]))
    const { result } = renderHook(() => usePipelineRuns("ws", "daily-etl"))
    await waitFor(() => expect(result.current.runs).toEqual([entry]))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipelines/daily-etl/runs?limit=50&include_steps=1")
    expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
  })
  it.each([null, {}])("tolerates legacy empty history (%j)", async body => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => usePipelineRuns("ws", "daily-etl"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.runs).toEqual([])
  })
  it.each([[null, "routine"], ["ws", null]])("avoids requests without complete scope (%s,%s)", (ws, slug) => {
    const { result } = renderHook(() => usePipelineRuns(ws, slug))
    expect(result.current).toMatchObject({ runs: [], loading: false })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each([new Error("offline"), "failed"])("surfaces transport failure (%s)", async failure => {
    fetchMock.mockRejectedValue(failure)
    const { result } = renderHook(() => usePipelineRuns("ws", "daily-etl"))
    await waitFor(() => expect(result.current.error).toBe(failure instanceof Error ? failure.message : failure))
  })
  it("reports HTTP failure and recovers on refresh", async () => {
    fetchMock.mockResolvedValueOnce(response({}, 500)).mockResolvedValueOnce(response([entry]))
    const { result } = renderHook(() => usePipelineRuns("ws", "daily-etl"))
    await waitFor(() => expect(result.current.error).toBe("pipeline runs: 500"))
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ runs: [entry], error: null, loading: false })
  })
  it.each(["response", "body"])("ignores obsolete %s after selecting a different routine", async stage => {
    const pending = deferred<Response>(); const body = deferred<unknown>(); const json = vi.fn(() => body.promise)
    fetchMock.mockReturnValueOnce(stage === "response" ? pending.promise : Promise.resolve({ ok: true, json } as unknown as Response)).mockResolvedValueOnce(response([{ ...entry, id: "new" }]))
    const { result, rerender } = renderHook(({ slug }) => usePipelineRuns("ws", slug), { initialProps: { slug: "old" } })
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    rerender({ slug: "new" })
    await waitFor(() => expect(result.current.runs[0]?.id).toBe("new"))
    await act(async () => { pending.resolve(response([entry])); body.resolve([entry]) })
    expect(result.current.runs[0]?.id).toBe("new")
  })
  it("settles loading after the selected routine disappears", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result, rerender } = renderHook(({ slug }: { slug: string | null }) => usePipelineRuns("ws", slug), { initialProps: { slug: "old" as string | null } })
    rerender({ slug: null })
    expect(result.current).toMatchObject({ runs: [], loading: false, error: null })
  })
})


it.each(["catalog", "history"])("ignores an obsolete %s transport rejection after the new workspace loaded", async kind => {
  let reject!: (reason: Error) => void
  fetchMock.mockReturnValueOnce(new Promise((_, r) => { reject = r })).mockResolvedValueOnce(response(kind === "catalog" ? [pipeline] : [entry]))
  const { result, rerender } = renderHook(({ ws }) => kind === "catalog" ? usePipelines(ws) : usePipelineRuns(ws, "daily-etl"), { initialProps: { ws: "old" } })
  rerender({ ws: "new" })
  await waitFor(() => expect(result.current.loading).toBe(false))
  await act(async () => { reject(new DOMException("aborted", "AbortError")) })
  expect(result.current).toMatchObject({ loading: false, error: null })
})
