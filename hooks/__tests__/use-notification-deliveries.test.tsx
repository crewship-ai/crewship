import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useNotificationDeliveries } from "@/hooks/use-notification-deliveries"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const row = { id: "delivery", workspace_id: "ws", status: "sent" }
function response(body: unknown = { deliveries: [row] }, status = 200) {
  return new Response(JSON.stringify(body), { status })
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
beforeEach(() => { fetchMock.mockReset() })

describe("notification delivery log", () => {
  it("encodes scope and all filters", async () => {
    fetchMock.mockResolvedValue(response())
    const { result } = renderHook(() => useNotificationDeliveries("ws &/", {
      status: "failed", channelId: "channel &", category: "run", limit: 25,
    }))
    await waitFor(() => expect(result.current.deliveries).toEqual([row]))
    const url = new URL(String(fetchMock.mock.calls[0][0]), "http://localhost")
    expect(Object.fromEntries(url.searchParams)).toEqual({ workspace_id: "ws &/", status: "failed", channel_id: "channel &", category: "run", limit: "25" })
    expect(result.current).toMatchObject({ loading: false, error: null, forbidden: false })
  })
  it.each([null, undefined, ""])("does not fetch for absent scope %s", ws => {
    const { result } = renderHook(() => useNotificationDeliveries(ws))
    expect(result.current).toMatchObject({ deliveries: [], loading: false, error: null, forbidden: false })
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each([{}, null, { deliveries: null }, { deliveries: {} }])("treats missing or malformed rows as empty: %j", async body => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => useNotificationDeliveries("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.deliveries).toEqual([])
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/v1/notification-deliveries?workspace_id=ws")
  })
  it("distinguishes forbidden access and recovers on refresh", async () => {
    fetchMock.mockResolvedValueOnce(response({}, 403)).mockResolvedValueOnce(response())
    const { result } = renderHook(() => useNotificationDeliveries("ws"))
    await waitFor(() => expect(result.current.forbidden).toBe(true))
    expect(result.current).toMatchObject({ deliveries: [], error: null, loading: false })
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ deliveries: [row], forbidden: false, error: null })
  })
  it.each([new Error("offline"), "offline"])("reports transport failure %s", async failure => {
    fetchMock.mockRejectedValue(failure)
    const { result } = renderHook(() => useNotificationDeliveries("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe(failure instanceof Error ? "offline" : "failed to load deliveries")
    expect(result.current.deliveries).toEqual([])
  })
  it("reports HTTP failure then clears it on success", async () => {
    fetchMock.mockResolvedValueOnce(response({}, 503)).mockResolvedValueOnce(response())
    const { result } = renderHook(() => useNotificationDeliveries("ws"))
    await waitFor(() => expect(result.current.error).toBe("load deliveries: 503"))
    await act(async () => { await result.current.refresh() })
    expect(result.current).toMatchObject({ deliveries: [row], error: null, loading: false })
  })
  it.each(["response", "body", "rejection"])("ignores obsolete %s after a workspace change", async stage => {
    const pending = deferred<Response>(); const body = deferred<unknown>()
    const json = vi.fn(() => body.promise)
    fetchMock.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, json } as unknown as Response) : pending.promise)
      .mockResolvedValueOnce(response({ deliveries: [{ ...row, id: "new" }] }))
    const { result, rerender } = renderHook(({ ws }) => useNotificationDeliveries(ws), { initialProps: { ws: "old" } })
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    const signal = fetchMock.mock.calls[0][1]?.signal
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.deliveries[0]?.id).toBe("new"))
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      if (stage === "rejection") pending.reject(new Error("old error"))
      else { pending.resolve(response()); body.resolve({ deliveries: [row] }) }
    })
    expect(result.current).toMatchObject({ deliveries: [{ ...row, id: "new" }], error: null, loading: false })
  })
  it.each([403, 503])("clears prior %s state when workspace selection is removed", async status => {
    fetchMock.mockResolvedValue(response({}, status))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useNotificationDeliveries(ws), { initialProps: { ws: "ws" as string | null } })
    await waitFor(() => expect(result.current.loading).toBe(false))
    rerender({ ws: null })
    expect(result.current).toMatchObject({ deliveries: [], error: null, forbidden: false, loading: false })
  })
  it("aborts pending delivery loading on unmount", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { unmount } = renderHook(() => useNotificationDeliveries("ws"))
    const signal = fetchMock.mock.calls[0][1]?.signal
    unmount()
    expect(signal?.aborted).toBe(true)
  })
})
