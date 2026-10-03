import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { usePipelineWebhooks } from "@/hooks/use-pipeline-webhooks"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const fetchMock = vi.mocked(apiFetch); const realtime = vi.mocked(useRealtimeEventSafe)
const webhook = { id: "hook", workspace_id: "ws", name: "Deploy", target_pipeline_id: "pipe", token: "fixture-token", signing_secret_set: true, inputs_template: {}, enabled: true, rate_limit_per_min: 10, fire_count: 0, created_at: "now", updated_at: "now" }
function response(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
function emit(payload: Record<string, unknown>) { act(() => realtime.mock.calls.at(-1)![1]({ payload } as never)) }
beforeEach(() => { fetchMock.mockReset(); realtime.mockClear() })
afterEach(() => { vi.useRealTimers() })

describe("pipeline webhook configuration", () => {
  it("loads scoped configuration with a cancellable request", async () => {
    fetchMock.mockResolvedValue(response([webhook]))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.webhooks).toEqual([webhook]))
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipeline-webhooks")
    expect(fetchMock.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
  })
  it("skips reads and mutations without a selected workspace", async () => {
    const { result } = renderHook(() => usePipelineWebhooks(null))
    expect(result.current).toMatchObject({ webhooks: [], loading: false, error: null })
    await act(async () => {
      expect(await result.current.create({ name: "Deploy" })).toBeNull()
      expect(await result.current.update("hook", { enabled: false })).toBeNull()
      await result.current.remove("hook")
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each([null, {}])("accepts legacy empty responses (%j)", async body => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.webhooks).toEqual([])
  })
  it.each([new Error("offline"), "failed"])("surfaces transport failure (%s)", async failure => {
    fetchMock.mockRejectedValue(failure)
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.error).toBe(failure instanceof Error ? failure.message : failure))
  })
  it("recovers from a failed HTTP read", async () => {
    fetchMock.mockResolvedValueOnce(response({}, 503)).mockResolvedValueOnce(response([webhook]))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.error).toBe("pipeline webhooks: 503"))
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ webhooks: [webhook], error: null, loading: false })
  })
  it("returns the one-time signing secret from creation and refreshes the secret-free list", async () => {
    fetchMock.mockResolvedValueOnce(response([])).mockResolvedValueOnce(response({ ...webhook, signing_secret: "fixture-secret" })).mockResolvedValueOnce(response([webhook]))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(async () => { expect(await result.current.create({ name: "Deploy", target_pipeline_slug: "deploy", ingress_profile: "github" })).toMatchObject({ signing_secret: "fixture-secret" }) })
    expect(fetchMock.mock.calls[1]).toEqual(["/api/v1/workspaces/ws/pipeline-webhooks", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ name: "Deploy", target_pipeline_slug: "deploy", ingress_profile: "github" }) }])
    expect(result.current.webhooks).toEqual([webhook])
  })
  it("updates only supplied fields and supports explicit unpin/secret rotation", async () => {
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(response({ ...webhook, enabled: false })).mockResolvedValueOnce(response([{ ...webhook, enabled: false }]))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    const body = { enabled: false, target_pipeline_version: null, rotate_secret: true }
    await act(async () => { await result.current.update("hook", body) })
    expect(fetchMock.mock.calls[1]).toEqual(["/api/v1/workspaces/ws/pipeline-webhooks/hook", { method: "PATCH", headers: { "content-type": "application/json" }, body: JSON.stringify(body) }])
    expect(result.current.webhooks[0]?.enabled).toBe(false)
  })
  it.each(["create", "update"] as const)("surfaces the server's %s refusal without refreshing configuration", async operation => {
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(new Response("permission denied", { status: 403 }))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(async () => {
      const pending = operation === "create" ? result.current.create({ name: "Deploy" }) : result.current.update("hook", { name: "Deploy" })
      await expect(pending).rejects.toThrow(`${operation} webhook: 403 permission denied`)
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.current.webhooks).toEqual([webhook])
  })
  it.each([200, 404])("treats delete %i as removal and refreshes configuration", async status => {
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(response({}, status)).mockResolvedValueOnce(response([]))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(async () => { await result.current.remove("hook") })
    expect(fetchMock.mock.calls[1]).toEqual(["/api/v1/workspaces/ws/pipeline-webhooks/hook", { method: "DELETE" }])
    expect(result.current.webhooks).toEqual([])
  })
  it("reports failed deletion without pretending the webhook disappeared", async () => {
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(response({}, 500))
    const { result } = renderHook(() => usePipelineWebhooks("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    await act(async () => { await expect(result.current.remove("hook")).rejects.toThrow("delete webhook: 500") })
    expect(result.current.webhooks).toEqual([webhook])
  })
  it.each(["webhook", undefined])("coalesces inbound/legacy starts into one refresh (%s)", async via => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(response([{ ...webhook, fire_count: 3 }]))
    const { result, unmount } = renderHook(() => usePipelineWebhooks("ws"))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    emit({ triggered_via: via }); emit({ triggered_via: via }); emit({ triggered_via: via })
    await act(async () => { await vi.advanceTimersByTimeAsync(1499) })
    expect(fetchMock).toHaveBeenCalledOnce()
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(result.current.webhooks[0]?.fire_count).toBe(3)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    emit({ triggered_via: "manual" })
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    unmount()
  })
  it("cancels a scheduled live refresh on unmount", async () => {
    vi.useFakeTimers(); fetchMock.mockResolvedValue(response([]))
    const { unmount } = renderHook(() => usePipelineWebhooks("ws"))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    emit({}); unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
    expect(fetchMock).toHaveBeenCalledOnce()
  })
  it("does not let an old workspace's debounce overwrite the newly selected workspace", async () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValueOnce(response([webhook])).mockResolvedValueOnce(response([{ ...webhook, workspace_id: "new", id: "new" }])).mockResolvedValue(response([webhook]))
    const { result, rerender, unmount } = renderHook(({ ws }) => usePipelineWebhooks(ws), { initialProps: { ws: "ws" } })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    emit({ triggered_via: "webhook" })
    rerender({ ws: "new" })
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(result.current.webhooks[0]?.id).toBe("new")
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
    expect(result.current.webhooks[0]?.id).toBe("new")
    expect(fetchMock).toHaveBeenCalledTimes(2)
    unmount()
  })
  it("does not refresh the old workspace after a pending create completes in another scope", async () => {
    const mutation = deferred<Response>()
    fetchMock.mockResolvedValueOnce(response([webhook])).mockReturnValueOnce(mutation.promise)
      .mockResolvedValueOnce(response([{ ...webhook, workspace_id: "new", id: "new" }])).mockResolvedValue(response([webhook]))
    const { result, rerender } = renderHook(({ ws }) => usePipelineWebhooks(ws), { initialProps: { ws: "ws" } })
    await waitFor(() => expect(result.current.loading).toBe(false))
    let pending!: ReturnType<typeof result.current.create>
    act(() => { pending = result.current.create({ name: "Deploy" }) })
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.webhooks[0]?.id).toBe("new"))
    await act(async () => { mutation.resolve(response(webhook)); await pending })
    expect(result.current.webhooks[0]?.id).toBe("new")
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })
  it("settles loading after workspace selection is cleared", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => usePipelineWebhooks(ws), { initialProps: { ws: "ws" as string | null } })
    rerender({ ws: null })
    expect(result.current).toMatchObject({ webhooks: [], loading: false, error: null })
  })
})


it.each(["response", "body"])("ignores the obsolete webhook %s after switching workspace", async stage => {
  const pending = deferred<Response>(); const body = deferred<unknown>(); const json = vi.fn(() => body.promise)
  fetchMock.mockReturnValueOnce(stage === "response" ? pending.promise : Promise.resolve({ ok: true, json } as unknown as Response)).mockResolvedValueOnce(response([]))
  const { result, rerender } = renderHook(({ ws }) => usePipelineWebhooks(ws), { initialProps: { ws: "old" } })
  if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
  rerender({ ws: "new" })
  await waitFor(() => expect(result.current.loading).toBe(false))
  await act(async () => { pending.resolve(response([webhook])); body.resolve([webhook]) })
  expect(result.current).toMatchObject({ webhooks: [], loading: false, error: null })
})

it("ignores a rejected old webhook request after workspace selection is cleared", async () => {
  let reject!: (reason: Error) => void
  fetchMock.mockReturnValue(new Promise((_, r) => { reject = r }))
  const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => usePipelineWebhooks(ws), { initialProps: { ws: "old" as string | null } })
  rerender({ ws: null })
  await act(async () => { reject(new DOMException("aborted", "AbortError")) })
  expect(result.current).toMatchObject({ webhooks: [], loading: false, error: null })
})
