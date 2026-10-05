import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useNotificationChannels } from "@/hooks/use-notification-channels"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const row = { id: "channel", type: "email", enabled: true }
const response = (body: unknown = { channels: [row] }, status = 200) => new Response(JSON.stringify(body), { status })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
type Channels = ReturnType<typeof useNotificationChannels>
const mutations = [
  { name: "create", fallback: "create channel: 503", call: (h: Channels) => h.create({ type: "email", to: "test@example.test" }) },
  { name: "remove", fallback: "delete channel: 503", call: (h: Channels) => h.remove("id /&") },
  { name: "patch", fallback: "update channel: 503", call: (h: Channels) => h.patch("id /&", { enabled: false }) },
  { name: "sendTest", fallback: "test send: 503", call: (h: Channels) => h.sendTest("id /&") },
  { name: "sendDraftTest", fallback: "test send: 503", call: (h: Channels) => h.sendDraftTest({ type: "email", to: "test@example.test" }) },
]
beforeEach(() => { fetchMock.mockReset() })

describe("notification channel boundaries", () => {
  it("requests everyone's channels only when selected", async () => {
    fetchMock.mockImplementation(async () => response())
    const { result } = renderHook(() => useNotificationChannels("ws &", { includeEveryone: true }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/v1/notification-channels?workspace_id=ws+%26&scope=all")
  })
  it.each([null, {}, { channels: null }])("handles absent lists %j", async body => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => useNotificationChannels("ws"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.channels).toEqual([])
  })
  it.each([new Error("offline"), "offline"])("surfaces transport failure %s", async failure => {
    fetchMock.mockRejectedValue(failure)
    const { result } = renderHook(() => useNotificationChannels("ws"))
    await waitFor(() => expect(result.current.error).toBe("offline"))
    expect(result.current.loading).toBe(false)
  })
  it.each(["response", "body", "rejection"])("ignores obsolete %s", async stage => {
    const pending = deferred<Response>(); const body = deferred<unknown>(); const json = vi.fn(() => body.promise)
    fetchMock.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, json } as unknown as Response) : pending.promise).mockResolvedValueOnce(response())
    const { result, rerender } = renderHook(({ ws }) => useNotificationChannels(ws), { initialProps: { ws: "old" } })
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.channels).toEqual([row]))
    await act(async () => {
      if (stage === "rejection") pending.reject(new Error("old error"))
      else { pending.resolve(response({ channels: [] })); body.resolve({ channels: [] }) }
    })
    expect(result.current).toMatchObject({ channels: [row], error: null, loading: false })
  })
  it.each(["pending", "failed"])("clears %s state when workspace is removed", async state => {
    fetchMock.mockImplementation(() => state === "pending" ? new Promise(() => {}) : Promise.resolve(response({}, 503)))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useNotificationChannels(ws), { initialProps: { ws: "old" as string | null } })
    if (state === "failed") await waitFor(() => expect(result.current.error).not.toBeNull())
    rerender({ ws: null })
    expect(result.current).toMatchObject({ channels: [], loading: false, error: null })
  })
  for (const mutation of mutations) {
    it(`${mutation.name} is inert without a workspace`, async () => {
      const { result } = renderHook(() => useNotificationChannels(null))
      await act(async () => { await mutation.call(result.current) })
      expect(fetchMock).not.toHaveBeenCalled()
    })
    it.each(["error", "detail", "empty", "invalid"])(`${mutation.name} handles %s failure bodies`, async kind => {
      fetchMock.mockResolvedValueOnce(response()).mockResolvedValueOnce(kind === "invalid" ? new Response("bad JSON", { status: 503 }) : response(kind === "empty" ? {} : { [kind]: "server refusal" }, 503))
      const { result } = renderHook(() => useNotificationChannels("ws"))
      await waitFor(() => expect(result.current.loading).toBe(false))
      await act(async () => { await expect(mutation.call(result.current)).rejects.toThrow(kind === "error" || kind === "detail" ? "server refusal" : mutation.fallback) })
      expect(fetchMock).toHaveBeenCalledTimes(2)
    })
    it(`${mutation.name} sends to the encoded workspace and returns success`, async () => {
      fetchMock.mockImplementation(async () => response())
      const { result } = renderHook(() => useNotificationChannels("ws /&"))
      await waitFor(() => expect(result.current.loading).toBe(false))
      await act(async () => { await mutation.call(result.current) })
      expect(String(fetchMock.mock.calls[1][0])).toContain("workspace_id=ws%20%2F%26")
      if (["remove", "patch", "sendTest"].includes(mutation.name)) expect(String(fetchMock.mock.calls[1][0])).toContain("id%20%2F%26")
      expect(fetchMock).toHaveBeenCalledTimes(mutation.name.startsWith("send") ? 2 : 3)
    })
  }
  it.each(mutations.slice(0, 3))("does not refresh an obsolete workspace after $name settles", async mutation => {
    const pending = deferred<Response>()
    fetchMock.mockResolvedValueOnce(response()).mockReturnValueOnce(pending.promise).mockResolvedValue(response({ channels: [{ ...row, id: "new" }] }))
    const { result, rerender } = renderHook(({ ws }) => useNotificationChannels(ws), { initialProps: { ws: "old" } })
    await waitFor(() => expect(result.current.loading).toBe(false))
    let work!: Promise<unknown>
    act(() => { work = mutation.call(result.current) })
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.channels[0]?.id).toBe("new"))
    await act(async () => { pending.resolve(response()); await work })
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(result.current.channels[0]?.id).toBe("new")
  })
})
