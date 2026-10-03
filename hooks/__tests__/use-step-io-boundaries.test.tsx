import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { useStepIO } from "@/hooks/use-step-io"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
function json(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } as Response }
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void
  const promise = new Promise<T>((a, b) => { resolve = a; reject = b })
  return { promise, resolve, reject }
}
beforeEach(() => { api.mockReset() })

it("clears old step I/O immediately when another step is selected", async () => {
  const pending = deferred<Response>()
  api.mockResolvedValueOnce(json({ sub_spans: { first: [{ output: "former step" }] } })).mockReturnValueOnce(pending.promise)
  const { result, rerender } = renderHook(({ step }) => useStepIO("ws", "run", step), { initialProps: { step: "first" } })
  await waitFor(() => expect(result.current.spans).toEqual([{ output: "former step" }]))
  rerender({ step: "second" })
  expect(result.current.spans).toBeUndefined()
  expect(result.current.loading).toBe(true)
  await act(async () => { pending.resolve(json({ sub_spans: { second: [{ output: "current step" }] } })); await pending.promise })
  await waitFor(() => expect(result.current.spans).toEqual([{ output: "current step" }]))
})

it.each([null, {}, { sub_spans: null }, { sub_spans: { step: "not a list" } }, { sub_spans: { other: [] } }])("uses no detailed spans for incomplete payload %#", async body => {
  api.mockResolvedValue(json(body))
  const { result } = renderHook(() => useStepIO("ws", "run", "step"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.spans).toBeUndefined()
})

it.each(["status", "transport", "json"])("finishes loading after %s failure", async failure => {
  if (failure === "status") api.mockResolvedValue(json({}, 403))
  if (failure === "transport") api.mockRejectedValue(new Error("offline"))
  if (failure === "json") api.mockResolvedValue(new Response("not JSON", { status: 200 }))
  const { result } = renderHook(() => useStepIO("ws", "run", "step"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.spans).toBeUndefined()
})

it("does not expose an abandoned JSON decode after the run changes", async () => {
  const body = deferred<unknown>()
  api.mockResolvedValueOnce({ ok: true, json: () => body.promise }).mockResolvedValueOnce(json({ sub_spans: { step: [{ id: "current" }] } }))
  const { result, rerender } = renderHook(({ run }) => useStepIO("ws", run, "step"), { initialProps: { run: "old" } })
  await act(async () => { await Promise.resolve() })
  rerender({ run: "new" })
  await waitFor(() => expect(result.current.spans).toEqual([{ id: "current" }]))
  await act(async () => { body.resolve({ sub_spans: { step: [{ id: "abandoned" }] } }); await body.promise })
  expect(result.current.spans).toEqual([{ id: "current" }])
})

it.each([false, true])("ignores abandoned fetch completion rejected=%s", async reject => {
  const pending = deferred<Response>()
  api.mockReturnValueOnce(pending.promise)
  const { result, rerender } = renderHook(({ step }: { step: string | null }) => useStepIO("ws", "run", step), { initialProps: { step: "old" as string | null } })
  const signal = api.mock.calls[0][1].signal as AbortSignal
  rerender({ step: null })
  expect(signal.aborted).toBe(true)
  await act(async () => {
    if (reject) pending.reject(new Error("old error"))
    else pending.resolve(json({ sub_spans: { old: [{ output: "old" }] } }))
    await pending.promise.catch(() => {})
  })
  expect(result.current.spans).toBeUndefined()
  expect(result.current.loading).toBe(false)
})
