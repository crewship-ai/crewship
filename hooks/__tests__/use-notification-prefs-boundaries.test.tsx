import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { useNotificationPrefs, type PrefCell } from "@/hooks/use-notification-prefs"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const cell: PrefCell = { category: "approvals", channel_id: "nch_a", state: "immediate" }
const other: PrefCell = { category: "runs", channel_id: "nch_b", state: "off" }
function json(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } as Response }
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((a, b) => { resolve = a; reject = b })
  return { promise, resolve, reject }
}
beforeEach(() => { api.mockReset() })

it("clears loading and prior errors when workspace selection is removed", async () => {
  const pending = deferred<Response>()
  api.mockReturnValue(pending.promise)
  const { result, rerender } = renderHook(({ workspace }: { workspace: string | null }) => useNotificationPrefs(workspace), { initialProps: { workspace: "ws-a" as string | null } })
  expect(result.current.loading).toBe(true)
  const signal = api.mock.calls[0][1].signal as AbortSignal
  rerender({ workspace: null })
  expect(signal.aborted).toBe(true)
  expect(result.current.loading).toBe(false)
  expect(result.current.error).toBeNull()
  await act(async () => { pending.resolve(json({ cells: [cell] })); await pending.promise })
  expect(result.current.cells).toEqual([])
})

it("does not restore the former workspace after its pending update fails", async () => {
  const pending = deferred<Response>()
  api.mockImplementation((url: string, init?: RequestInit) => init?.method === "PUT" ? pending.promise : Promise.resolve(json({ cells: url.includes("ws-a") ? [cell] : [other] })))
  const { result, rerender } = renderHook(({ workspace }) => useNotificationPrefs(workspace), { initialProps: { workspace: "ws-a" } })
  await waitFor(() => expect(result.current.cells).toEqual([cell]))
  let mutation!: Promise<unknown>
  act(() => { mutation = result.current.setCell({ ...cell, state: "off" }).catch(error => error) })
  rerender({ workspace: "ws-b" })
  await waitFor(() => expect(result.current.cells).toEqual([other]))
  await act(async () => { pending.reject(new Error("old workspace denied")); await mutation })
  expect(result.current.cells).toEqual([other])
})

it("rolls back only the failed cell while preserving another completed edit", async () => {
  const pending = deferred<Response>()
  api.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method !== "PUT") return Promise.resolve(json({ cells: [cell] }))
    const edited = JSON.parse(String(init.body)).cells[0] as PrefCell
    return edited.channel_id === cell.channel_id ? pending.promise : Promise.resolve(json({ ok: true }))
  })
  const { result } = renderHook(() => useNotificationPrefs("ws-a"))
  await waitFor(() => expect(result.current.cells).toEqual([cell]))
  let first!: Promise<unknown>
  act(() => { first = result.current.setCell({ ...cell, state: "off" }).catch(error => error) })
  await act(async () => { await result.current.setCell(other) })
  await act(async () => { pending.reject(new Error("first denied")); await first })
  expect(result.current.cells).toEqual([cell, other])
})

it.each([null, {}, { cells: null }, { cells: "invalid" }])("normalizes missing preference lists %#", async body => {
  api.mockResolvedValue(json(body))
  const { result } = renderHook(() => useNotificationPrefs("space & slash/"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.cells).toEqual([])
  expect(api.mock.calls[0][0]).toContain("workspace_id=space%20%26%20slash%2F")
})

it.each([new Error("transport failed"), "transport failed"])("reports active transport failures %# and clears on deselection", async failure => {
  api.mockRejectedValue(failure)
  const { result, rerender } = renderHook(({ workspace }: { workspace: string | undefined }) => useNotificationPrefs(workspace), { initialProps: { workspace: "ws-a" as string | undefined } })
  await waitFor(() => expect(result.current.error).toBe("transport failed"))
  rerender({ workspace: undefined })
  expect(result.current.error).toBeNull()
  expect(result.current.loading).toBe(false)
  await act(async () => { await result.current.setCell(cell); await result.current.refresh() })
  expect(api).toHaveBeenCalledTimes(1)
})

it("does not apply an abandoned JSON decode or expose its error", async () => {
  const body = deferred<unknown>()
  api.mockResolvedValueOnce({ ok: true, json: () => body.promise }).mockResolvedValue(json({ cells: [other] }))
  const { result, rerender } = renderHook(({ workspace }) => useNotificationPrefs(workspace), { initialProps: { workspace: "ws-a" } })
  await act(async () => { await Promise.resolve() })
  const oldRefresh = result.current.refresh
  const oldSetCell = result.current.setCell
  rerender({ workspace: "ws-b" })
  await waitFor(() => expect(result.current.cells).toEqual([other]))
  await act(async () => { body.resolve({ cells: [cell] }); await body.promise; await oldRefresh(); await oldSetCell(cell) })
  expect(result.current.cells).toEqual([other])
  expect(result.current.error).toBeNull()
  expect(api).toHaveBeenCalledTimes(2)
})

it("ignores a rejected request once its workspace is abandoned", async () => {
  const pending = deferred<Response>()
  api.mockReturnValueOnce(pending.promise).mockResolvedValue(json({ cells: [other] }))
  const { result, rerender } = renderHook(({ workspace }) => useNotificationPrefs(workspace), { initialProps: { workspace: "ws-a" } })
  rerender({ workspace: "ws-b" })
  await waitFor(() => expect(result.current.cells).toEqual([other]))
  await act(async () => { pending.reject("old transport error"); await pending.promise.catch(() => {}) })
  expect(result.current.error).toBeNull()
  expect(result.current.loading).toBe(false)
})

it.each([
  [json({ detail: "policy detail" }, 403), "policy detail"],
  [json(null, 503), "set preference: 503"],
  [new Response("bad gateway", { status: 502 }), "set preference: 502"],
])("removes a failed new cell and preserves actionable errors %#", async (response, message) => {
  api.mockResolvedValueOnce(json({ cells: [other] })).mockResolvedValueOnce(response)
  const { result } = renderHook(() => useNotificationPrefs("ws-a"))
  await waitFor(() => expect(result.current.cells).toEqual([other]))
  await act(async () => { await expect(result.current.setCell(cell)).rejects.toThrow(message) })
  expect(result.current.cells).toEqual([other])
})

it("keeps a later successful edit when an older edit of the same cell fails", async () => {
  const first = deferred<Response>()
  api.mockResolvedValueOnce(json({ cells: [cell] })).mockReturnValueOnce(first.promise).mockResolvedValueOnce(json({ ok: true }))
  const { result } = renderHook(() => useNotificationPrefs("ws-a"))
  await waitFor(() => expect(result.current.cells).toEqual([cell]))
  let older!: Promise<unknown>
  act(() => { older = result.current.setCell({ ...cell, state: "off" }).catch(error => error) })
  await act(async () => { await result.current.setCell({ ...cell, state: "immediate" }) })
  await act(async () => { first.reject(new Error("stale refusal")); await older })
  expect(result.current.cells).toEqual([cell])
})

it("manual refresh cancels its previous read", async () => {
  const first = deferred<Response>()
  api.mockReturnValueOnce(first.promise).mockResolvedValueOnce(json({ cells: [other] }))
  const { result } = renderHook(() => useNotificationPrefs("ws-a"))
  const signal = api.mock.calls[0][1].signal as AbortSignal
  await act(async () => { await result.current.refresh() })
  expect(signal.aborted).toBe(true)
  await act(async () => { first.resolve(json({ cells: [cell] })); await first.promise })
  expect(result.current.cells).toEqual([other])
})

it("restores the saved cell when both overlapping edits are rejected", async () => {
  const first = deferred<Response>(), second = deferred<Response>()
  api.mockResolvedValueOnce(json({ cells: [cell] })).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  const { result } = renderHook(() => useNotificationPrefs("ws-a"))
  await waitFor(() => expect(result.current.cells).toEqual([cell]))
  let older!: Promise<unknown>, newer!: Promise<unknown>
  act(() => { older = result.current.setCell({ ...cell, state: "off" }).catch(error => error) })
  act(() => { newer = result.current.setCell({ ...cell, state: "immediate" }).catch(error => error) })
  await act(async () => { first.reject(new Error("older denied")); await older })
  await act(async () => { second.reject(new Error("newer denied")); await newer })
  expect(result.current.cells).toEqual([cell])
})
