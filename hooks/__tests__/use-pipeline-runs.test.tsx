import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: vi.fn() }))
const fetchMock = vi.mocked(apiFetch); const realtime = vi.mocked(useRealtimeEvent)
const row = { id: "run", pipeline_slug: "daily-etl", pipeline_name: "Daily ETL", status: "completed" }
function response(rows: unknown = [row], status = 200) { return new Response(JSON.stringify({ rows, count: 1 }), { status }) }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })
afterEach(() => { vi.useRealTimers() })

describe("workspace run feed", () => {
  it("encodes workspace scope and sends status plus row budget", async () => {
    fetchMock.mockResolvedValue(response())
    const { result } = renderHook(() => usePipelineRuns("ws /&", "active", 200))
    await waitFor(() => expect(result.current.runs).toEqual([row]))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws%20%2F%26/pipeline-runs?status=active&limit=200")
    expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
  })
  it("defaults to all statuses and the historical row budget", async () => {
    fetchMock.mockResolvedValue(response(null))
    const { result } = renderHook(() => usePipelineRuns("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.runs).toEqual([])
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipeline-runs?limit=100")
  })
  it("does not query without a workspace", () => {
    const { result } = renderHook(() => usePipelineRuns(null))
    expect(result.current).toMatchObject({ runs: [], loading: false, error: null })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each(["pipeline.run.started", "pipeline.run.completed", "pipeline.run.failed"])("refreshes on %s even while polling is idle", async kind => {
    fetchMock.mockResolvedValueOnce(response()).mockResolvedValueOnce(response([]))
    const { result } = renderHook(() => usePipelineRuns("ws"))
    await waitFor(() => expect(result.current.runs).toHaveLength(1))
    await act(async () => { await realtime.mock.calls.filter(([event]) => event === kind).at(-1)![1]({} as never) })
    expect(result.current.runs).toEqual([])
  })
  it.each([new Error("offline"), "unavailable"])("surfaces fetch failures (%s)", async failure => {
    fetchMock.mockRejectedValue(failure)
    const { result } = renderHook(() => usePipelineRuns("ws"))
    await waitFor(() => expect(result.current.error).toBe(failure instanceof Error ? failure.message : failure))
  })
  it("recovers after HTTP failure", async () => {
    fetchMock.mockResolvedValueOnce(response([], 503)).mockResolvedValueOnce(response())
    const { result } = renderHook(() => usePipelineRuns("ws"))
    await waitFor(() => expect(result.current.error).toBe("runs: 503"))
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ runs: [row], error: null, loading: false })
  })
  it.each(["running", "queued", "paused", "waiting"])("polls %s runs and stops once complete", async status => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValueOnce(response([{ ...row, status }])).mockResolvedValueOnce(response())
    const { result, unmount } = renderHook(() => usePipelineRuns("ws"))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(result.current.runs[0]?.status).toBe(status)
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(result.current.runs[0]?.status).toBe("completed")
    await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    unmount()
  })
  it("does not overlap a slow refresh with another manual refresh", async () => {
    const pending = deferred<Response>()
    fetchMock.mockReturnValue(pending.promise)
    const { result } = renderHook(() => usePipelineRuns("ws"))
    await act(async () => { await result.current.refresh() })
    expect(fetchMock).toHaveBeenCalledOnce()
    await act(async () => { pending.resolve(response()) })
  })
  it.each(["response", "body"])("ignores stale %s after scope/filter change", async stage => {
    const pending = deferred<Response>(); const body = deferred<unknown>(); const json = vi.fn(() => body.promise)
    fetchMock.mockReturnValueOnce(stage === "response" ? pending.promise : Promise.resolve({ ok: true, json } as unknown as Response)).mockResolvedValueOnce(response([{ ...row, id: "new" }]))
    const { result, rerender } = renderHook(({ ws }) => usePipelineRuns(ws), { initialProps: { ws: "old" } })
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.runs[0]?.id).toBe("new"))
    await act(async () => { pending.resolve(response()); body.resolve({ rows: [row] }) })
    expect(result.current.runs[0]?.id).toBe("new")
  })
  it("keeps the newer request protected when an aborted older response settles", async () => {
    const old = deferred<Response>(); const current = deferred<Response>()
    fetchMock.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise).mockResolvedValue(response())
    const { result, rerender } = renderHook(({ ws }) => usePipelineRuns(ws), { initialProps: { ws: "old" } })
    rerender({ ws: "new" })
    await act(async () => { old.resolve(response()) })
    await act(async () => { await result.current.refresh() })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await act(async () => { current.resolve(response([{ ...row, id: "new" }])) })
    expect(result.current.runs[0]?.id).toBe("new")
  })
  it("settles loading when workspace selection is cleared", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => usePipelineRuns(ws), { initialProps: { ws: "ws" as string | null } })
    rerender({ ws: null })
    expect(result.current).toMatchObject({ runs: [], loading: false, error: null })
  })
})


it("ignores a rejected obsolete request after workspace selection is cleared", async () => {
  let reject!: (reason: Error) => void
  fetchMock.mockReturnValue(new Promise((_, r) => { reject = r }))
  const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => usePipelineRuns(ws), { initialProps: { ws: "old" as string | null } })
  rerender({ ws: null })
  await act(async () => { reject(new DOMException("aborted", "AbortError")) })
  expect(result.current).toMatchObject({ runs: [], loading: false, error: null })
})
