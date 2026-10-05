import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useBudgetSummary, useRoutineBudget } from "../use-routine-budget"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetch = vi.mocked(apiFetch)
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function useSingle(ws: string | null) {
  const result = useRoutineBudget(ws, "routine /")
  return { ...result, data: result.budget }
}
function useSummary(ws: string | null) {
  const result = useBudgetSummary(ws)
  return { ...result, data: result.summary }
}
beforeEach(() => { fetch.mockReset() })
afterEach(() => cleanup())

for (const [kind, useSubject] of [["budget", useSingle], ["summary", useSummary]] as const) {
  describe(kind, () => {
    it("encodes the workspace and preserves known data on HTTP failure", async () => {
      fetch.mockResolvedValueOnce(reply({ month: "known" })).mockResolvedValueOnce(reply({}, 500))
      const { result } = renderHook(() => useSubject("ws /"))
      await waitFor(() => expect(result.current.data?.month).toBe("known"))
      expect(fetch.mock.calls[0][0]).toContain("/workspaces/ws%20%2F/")
      await act(async () => result.current.refresh())
      expect(result.current.data?.month).toBe("known")
      expect(result.current.error).toContain("500")
    })
    it("treats an unavailable history store as absent data", async () => {
      fetch.mockResolvedValue(reply({}, 503))
      const { result } = renderHook(() => useSubject("ws"))
      await waitFor(() => expect(result.current.loading).toBe(false))
      expect(result.current).toMatchObject({ data: null, error: null })
    })
    it.each([new Error("offline"), "offline"])("reports and recovers from %s", async (error) => {
      fetch.mockRejectedValueOnce(error).mockResolvedValueOnce(reply({ month: "recovered" }))
      const { result } = renderHook(() => useSubject("ws"))
      await waitFor(() => expect(result.current.error).toBe("offline"))
      await act(async () => result.current.refresh())
      expect(result.current).toMatchObject({ data: { month: "recovered" }, error: null, loading: false })
    })
    it.each(["response", "body", "error"])("ignores an obsolete %s after switching workspace", async (stage) => {
      const pending = deferred<Response>()
      const decoding = deferred<unknown>()
      const json = vi.fn(() => decoding.promise)
      fetch.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, json } as unknown as Response) : pending.promise)
        .mockResolvedValueOnce(reply({ month: "current" }))
      const { result, rerender } = renderHook(({ ws }) => useSubject(ws), { initialProps: { ws: "old" } })
      if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
      rerender({ ws: "current" })
      await waitFor(() => expect(result.current.data?.month).toBe("current"))
      await act(async () => {
        if (stage === "error") pending.reject(new Error("old"))
        else { pending.resolve(reply({ month: "old" })); decoding.resolve({ month: "old" }) }
      })
      expect(result.current).toMatchObject({ data: { month: "current" }, error: null })
    })
    it("settles loading when its workspace is cleared", async () => {
      const pending = deferred<Response>()
      fetch.mockReturnValue(pending.promise)
      const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useSubject(ws), { initialProps: { ws: "ws" as string | null } })
      expect(result.current.loading).toBe(true)
      rerender({ ws: null })
      expect(result.current).toMatchObject({ data: null, error: null, loading: false })
      await act(async () => pending.resolve(reply({ month: "old" })))
      expect(result.current.data).toBeNull()
    })
    it("removes the old workspace's data while the new request is pending", async () => {
      const pending = deferred<Response>()
      fetch.mockResolvedValueOnce(reply({ month: "old" })).mockReturnValueOnce(pending.promise)
      const { result, rerender } = renderHook(({ ws }) => useSubject(ws), { initialProps: { ws: "old" } })
      await waitFor(() => expect(result.current.data?.month).toBe("old"))
      rerender({ ws: "new" })
      expect(result.current.data).toBeNull()
      await act(async () => pending.resolve(reply({ month: "new" })))
      expect(result.current.data?.month).toBe("new")
    })
  })
}

it.each(["workspace", "routine", "unmount"])("does not publish a completed budget mutation after changing %s", async (change) => {
  const pending = deferred<Response>()
  fetch.mockResolvedValueOnce(reply({ monthly_budget_usd: 10 })).mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(reply({ monthly_budget_usd: 20 }))
  const { result, rerender, unmount } = renderHook(({ ws, slug }) => useRoutineBudget(ws, slug), { initialProps: { ws: "old", slug: "old" } })
  await waitFor(() => expect(result.current.budget?.monthly_budget_usd).toBe(10))
  let mutation!: ReturnType<typeof result.current.setBudget>
  act(() => { mutation = result.current.setBudget(100) })
  if (change === "unmount") unmount()
  else {
    rerender({ ws: change === "workspace" ? "new" : "old", slug: change === "routine" ? "new" : "old" })
    await waitFor(() => expect(result.current.budget?.monthly_budget_usd).toBe(20))
  }
  await act(async () => {
    pending.resolve(reply({ monthly_budget_usd: 100 }))
    expect(await mutation).toMatchObject({ monthly_budget_usd: 100 })
  })
  if (change !== "unmount") expect(result.current.budget?.monthly_budget_usd).toBe(20)
  expect(fetch).toHaveBeenCalledTimes(change === "unmount" ? 2 : 3)
})

it("does not let a pending read overwrite a successful budget edit", async () => {
  const pending = deferred<Response>()
  fetch.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(reply({ monthly_budget_usd: 100 }))
  const { result } = renderHook(() => useRoutineBudget("ws", "routine"))
  await act(async () => result.current.setBudget(100))
  await act(async () => pending.resolve(reply({ monthly_budget_usd: 10 })))
  expect(result.current).toMatchObject({ budget: { monthly_budget_usd: 100 }, loading: false })
})

it("does not send an unscoped mutation and preserves server refusals", async () => {
  const view = renderHook(() => useRoutineBudget(null, null))
  expect(await view.result.current.setBudget(100)).toBeNull()
  expect(fetch).not.toHaveBeenCalled()
  view.unmount()
  fetch.mockResolvedValueOnce(reply({ monthly_budget_usd: 10 })).mockResolvedValueOnce(new Response("forbidden", { status: 403 }))
  const { result } = renderHook(() => useRoutineBudget("ws", "routine"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  await expect(result.current.setBudget(100)).rejects.toThrow("set budget failed: 403 forbidden")
  expect(result.current.budget?.monthly_budget_usd).toBe(10)
})
