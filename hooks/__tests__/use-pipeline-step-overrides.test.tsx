import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { usePipelineStepOverrides } from "@/hooks/use-pipeline-step-overrides"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const overrides = [{ step_id: "review", prompt: "Review carefully", model_override: "strong" }]
function response(body: unknown) { return new Response(JSON.stringify(body)) }
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}
beforeEach(() => { fetchMock.mockReset() })

describe("usePipelineStepOverrides", () => {
  it.each([[null, "routine"], ["ws", null]])("does not fetch without scope (%s, %s)", (workspaceId, slug) => {
    const { result } = renderHook(() => usePipelineStepOverrides(workspaceId, slug))
    expect(result.current).toMatchObject({ overrides: [], loading: false })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("loads overrides using escaped scope and replaces them on refresh", async () => {
    fetchMock.mockResolvedValueOnce(response({ overrides })).mockResolvedValueOnce(response({ overrides: [] }))
    const { result } = renderHook(() => usePipelineStepOverrides("ws /?", "routine/#"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.overrides).toEqual(overrides)
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws%20%2F%3F/pipelines/routine%2F%23/overrides")
    await act(async () => { await result.current.refresh() })
    expect(result.current.overrides).toEqual([])
  })

  it.each([null, {}, { overrides: "invalid" }])("treats an absent override list as empty (%j)", async (body) => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => usePipelineStepOverrides("ws", "routine"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.overrides).toEqual([])
  })

  it("clears old overrides when refresh fails without blocking routine detail", async () => {
    fetchMock.mockResolvedValueOnce(response({ overrides })).mockResolvedValueOnce(new Response("", { status: 503 }))
      .mockResolvedValueOnce(new Response("invalid json")).mockRejectedValueOnce(new Error("network"))
    const { result } = renderHook(() => usePipelineStepOverrides("ws", "routine"))
    await waitFor(() => expect(result.current.overrides).toEqual(overrides))
    for (let i = 0; i < 3; i++) {
      await act(async () => { await result.current.refresh() })
      expect(result.current).toMatchObject({ overrides: [], loading: false })
    }
  })

  it.each(["response", "body"])("ignores an obsolete %s after changing routine", async (stage) => {
    const pendingResponse = deferred<Response>()
    const pendingBody = deferred<unknown>()
    const json = vi.fn(() => pendingBody.promise)
    fetchMock.mockReturnValueOnce(stage === "response" ? pendingResponse.promise : Promise.resolve({
      ok: true, json,
    } as unknown as Response)).mockResolvedValueOnce(response({ overrides: [{ step_id: "new" }] }))
    const { result, rerender } = renderHook(({ slug }) => usePipelineStepOverrides("ws", slug), { initialProps: { slug: "old" } })
    const oldSignal = fetchMock.mock.calls[0][1]!.signal!
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    rerender({ slug: "new" })
    await waitFor(() => expect(result.current.overrides).toEqual([{ step_id: "new" }]))
    expect(oldSignal.aborted).toBe(true)
    await act(async () => {
      pendingResponse.resolve(response({ overrides }))
      pendingBody.resolve({ overrides })
    })
    expect(result.current).toMatchObject({ overrides: [{ step_id: "new" }], loading: false })
  })

  it("stops loading when workspace disappears during an in-flight request", async () => {
    const pending = deferred<Response>()
    fetchMock.mockReturnValue(pending.promise)
    const { result, rerender } = renderHook(({ workspaceId }: { workspaceId: string | null }) => usePipelineStepOverrides(workspaceId, "routine"), {
      initialProps: { workspaceId: "ws" as string | null },
    })
    expect(result.current.loading).toBe(true)
    rerender({ workspaceId: null })
    expect(result.current).toMatchObject({ overrides: [], loading: false })
    await act(async () => { pending.resolve(response({ overrides })) })
    expect(result.current).toMatchObject({ overrides: [], loading: false })
  })

  it("ignores a rejected obsolete request while its replacement succeeds", async () => {
    let reject!: (reason: Error) => void
    const pending = new Promise<Response>((_, r) => { reject = r })
    fetchMock.mockReturnValueOnce(pending).mockResolvedValueOnce(response({ overrides }))
    const { result, rerender } = renderHook(({ slug }) => usePipelineStepOverrides("ws", slug), { initialProps: { slug: "old" } })
    rerender({ slug: "new" })
    await waitFor(() => expect(result.current.overrides).toEqual(overrides))
    await act(async () => { reject(new DOMException("aborted", "AbortError")) })
    expect(result.current).toMatchObject({ overrides, loading: false })
  })

  it("aborts an active request when unmounted", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { unmount } = renderHook(() => usePipelineStepOverrides("ws", "routine"))
    const signal = fetchMock.mock.calls[0][1]!.signal!
    unmount()
    expect(signal.aborted).toBe(true)
  })
})
