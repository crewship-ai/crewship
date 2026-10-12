import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { useChains } from "@/hooks/use-chains"
import { useAutomations } from "@/hooks/use-automations"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
function useLists(workspace: string | null) { return { chains: useChains(workspace), rules: useAutomations(workspace) } }
function json(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } as Response }
function rows(id: string) { return { chains: [{ origin: id }], automations: [{ id }], has_more: false, has_unrecorded_runs: true } }
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void
  const promise = new Promise<T>((a, b) => { resolve = a; reject = b })
  return { promise, resolve, reject }
}
beforeEach(() => { api.mockReset() })

it("finishes both loaders on deselection and ignores pending old responses", async () => {
  const pending = deferred<Response>()
  api.mockReturnValue(pending.promise)
  const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useLists(ws), { initialProps: { ws: "old" as string | null } })
  expect(result.current.chains.loading).toBe(true)
  expect(result.current.rules.loading).toBe(true)
  rerender({ ws: null })
  expect(result.current.chains.loading).toBe(false)
  expect(result.current.rules.loading).toBe(false)
  await act(async () => { pending.resolve(json(rows("old"))); await pending.promise })
  expect(result.current.chains.chains).toEqual([])
  expect(result.current.rules.automations).toEqual([])
  expect(result.current.chains.hasMore).toBe(true)
  expect(result.current.chains.hasUnrecordedRuns).toBe(false)
})

it("clears former workspace rows and summary flags before replacement reads finish", async () => {
  const pending = deferred<Response>()
  api.mockResolvedValue(json(rows("old")))
  const { result, rerender } = renderHook(({ ws }) => useLists(ws), { initialProps: { ws: "old" } })
  await waitFor(() => expect(result.current.chains.chains).toHaveLength(1))
  await waitFor(() => expect(result.current.rules.automations).toHaveLength(1))
  const oldChainRefresh = result.current.chains.refresh, oldRuleRefresh = result.current.rules.refresh
  api.mockReturnValue(pending.promise)
  rerender({ ws: "new" })
  expect(result.current.chains.chains).toEqual([])
  expect(result.current.rules.automations).toEqual([])
  expect(result.current.chains.hasMore).toBe(true)
  expect(result.current.chains.hasUnrecordedRuns).toBe(false)
  await act(async () => { pending.resolve(json(rows("new"))); await pending.promise })
  await waitFor(() => expect(result.current.chains.loading).toBe(false))
  api.mockClear()
  await act(async () => { await oldChainRefresh(); await oldRuleRefresh() })
  expect(api).not.toHaveBeenCalled()
  expect(result.current.chains.chains[0].origin).toBe("new")
})

it.each([new Error("network down"), "network down"])("reports active failures %# and clears them on deselection", async failure => {
  api.mockRejectedValue(failure)
  const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useLists(ws), { initialProps: { ws: "old" as string | null } })
  await waitFor(() => expect(result.current.chains.error).toBe(failure instanceof Error ? "network down" : "could not load workflows"))
  await waitFor(() => expect(result.current.rules.error).toBe("network down"))
  rerender({ ws: null })
  expect(result.current.chains.error).toBeNull()
  expect(result.current.rules.error).toBeNull()
})

it("keeps conservative completeness flags when response fields are missing", async () => {
  api.mockResolvedValue(json({}))
  const { result } = renderHook(() => useLists("space & slash/"))
  await waitFor(() => expect(result.current.chains.loading).toBe(false))
  await waitFor(() => expect(result.current.rules.loading).toBe(false))
  expect(result.current.chains.chains).toEqual([])
  expect(result.current.rules.automations).toEqual([])
  expect(result.current.chains.hasMore).toBe(true)
  expect(api.mock.calls.every(call => String(call[0]).includes("workspace_id=space%20%26%20slash%2F"))).toBe(true)
})

it.each([false, true])("ignores abandoned manual refreshes rejected=%s", async reject => {
  const pending = deferred<Response>()
  api.mockReturnValue(pending.promise)
  const { result } = renderHook(() => useLists("ws"))
  api.mockResolvedValue(json(rows("current")))
  await act(async () => { await Promise.all([result.current.chains.refresh(), result.current.rules.refresh()]) })
  await act(async () => {
    if (reject) pending.reject("abandoned failure")
    else pending.resolve(json(rows("old")))
    await pending.promise.catch(() => {})
  })
  expect(result.current.chains.chains[0].origin).toBe("current")
  expect(result.current.rules.automations[0].id).toBe("current")
  expect(result.current.chains.error).toBeNull()
  expect(result.current.rules.error).toBeNull()
})

it("ignores JSON decoding that completes after the scope changes", async () => {
  const pending = deferred<unknown>()
  api.mockResolvedValue({ ok: true, json: () => pending.promise })
  const { result, rerender } = renderHook(({ ws }) => useLists(ws), { initialProps: { ws: "old" } })
  await act(async () => { await Promise.resolve() })
  api.mockResolvedValue(json(rows("new")))
  rerender({ ws: "new" })
  await waitFor(() => expect(result.current.chains.chains[0]?.origin).toBe("new"))
  await act(async () => { pending.resolve(rows("old")); await pending.promise })
  expect(result.current.chains.chains[0].origin).toBe("new")
  expect(result.current.rules.automations[0].id).toBe("new")
})

it("does not start captured refresh callbacks after unmount", async () => {
  api.mockResolvedValue(json(rows("ws")))
  const { result, unmount } = renderHook(() => useLists("ws"))
  await waitFor(() => expect(result.current.rules.loading).toBe(false))
  const refreshChains = result.current.chains.refresh, refreshRules = result.current.rules.refresh
  unmount()
  api.mockClear()
  await refreshChains()
  await refreshRules()
  expect(api).not.toHaveBeenCalled()
})

it("does not let a captured old window size replace a newly selected window", async () => {
  api.mockResolvedValue(json(rows("ws")))
  const { result, rerender } = renderHook(({ limit }) => useChains("ws", limit), { initialProps: { limit: 25 } })
  await waitFor(() => expect(result.current.loading).toBe(false))
  const oldRefresh = result.current.refresh
  rerender({ limit: 50 })
  await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
  await act(async () => { await oldRefresh() })
  expect(api).toHaveBeenCalledTimes(2)
  expect(api.mock.calls[1][0]).toContain("limit=50")
})
