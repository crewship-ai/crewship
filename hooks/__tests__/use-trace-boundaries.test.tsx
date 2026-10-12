import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useTrace } from "../use-trace"

const events = vi.hoisted(() => new Map<string, (event: { payload?: Record<string, unknown> }) => void>())
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (name: string, callback: (event: { payload?: Record<string, unknown> }) => void) => events.set(name, callback),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetch = vi.mocked(apiFetch)
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
beforeEach(() => { fetch.mockReset(); events.clear() })
afterEach(() => { cleanup(); vi.useRealTimers() })

it.each(["workspace", "run"])("settles a pending trace when clearing %s", async (which) => {
  const pending = deferred<Response>()
  fetch.mockReturnValue(pending.promise)
  const { result, rerender } = renderHook(({ ws, run }: { ws: string | null; run: string | null }) => useTrace(ws, run), {
    initialProps: { ws: "ws" as string | null, run: "run" as string | null },
  })
  expect(result.current.loading).toBe(true)
  const signal = fetch.mock.calls[0][1]?.signal
  rerender({ ws: which === "workspace" ? null : "ws", run: which === "run" ? null : "run" })
  expect(result.current).toMatchObject({ loading: false, error: null, run: null, dsl: null })
  expect(signal?.aborted).toBe(true)
  await act(async () => pending.resolve(reply({ id: "old", status: "completed" })))
  expect(result.current.run).toBeNull()
  await act(async () => events.get("pipeline.run.started")!({ payload: { run_id: "run" } }))
  expect(fetch).toHaveBeenCalledOnce()
})

it.each(["response", "body", "error"])("ignores an old %s after switching runs", async (stage) => {
  const pending = deferred<Response>()
  const decoding = deferred<unknown>()
  const json = vi.fn(() => decoding.promise)
  fetch.mockReturnValueOnce(stage === "body" ? Promise.resolve({ ok: true, json } as unknown as Response) : pending.promise)
    .mockResolvedValueOnce(reply({ id: "new", status: "completed" }))
  const { result, rerender } = renderHook(({ run }) => useTrace("ws", run), { initialProps: { run: "old" } })
  if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
  rerender({ run: "new" })
  await waitFor(() => expect(result.current.run?.id).toBe("new"))
  await act(async () => {
    if (stage === "error") pending.reject(new Error("obsolete"))
    else { pending.resolve(reply({ id: "old" })); decoding.resolve({ id: "old" }) }
  })
  expect(result.current).toMatchObject({ run: { id: "new" }, loading: false, error: null })
})

it.each([404, 503])("handles HTTP %i without substituting another run", async (status) => {
  const known = { id: "known", status: "completed", definition: { steps: [{ id: "saved" }] } }
  fetch.mockResolvedValueOnce(reply(known)).mockResolvedValueOnce(reply({}, status))
  const { result } = renderHook(() => useTrace("ws /", "run /"))
  await waitFor(() => expect(result.current.run?.id).toBe("known"))
  expect(fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws%20%2F/pipeline-runs/run%20%2F")
  await act(async () => result.current.refresh())
  expect(result.current.error).toBe(`run: ${status}`)
  expect(result.current.run).toEqual(status === 404 ? null : known)
  expect(result.current.dsl).toEqual(status === 404 ? null : known.definition)
})

it.each([new Error("offline"), "offline"])("reports a transport failure and recovers (%s)", async (error) => {
  fetch.mockRejectedValueOnce(error).mockResolvedValueOnce(reply({ id: "recovered", status: "completed" }))
  const { result } = renderHook(() => useTrace("ws", "run"))
  await waitFor(() => expect(result.current.error).toBe("offline"))
  await act(async () => result.current.refresh())
  expect(result.current).toMatchObject({ error: null, loading: false, run: { id: "recovered" } })
})

it.each(["running", "queued", "paused", "waiting"])("polls a %s run and stops at completion", async (status) => {
  vi.useFakeTimers()
  fetch.mockResolvedValueOnce(reply({ id: "run", status })).mockResolvedValueOnce(reply({ id: "run", status: "completed" }))
  const { result } = renderHook(() => useTrace("ws", "run"))
  await act(async () => {})
  expect(result.current.run?.status).toBe(status)
  await act(async () => vi.advanceTimersByTimeAsync(3000))
  expect(result.current.run?.status).toBe("completed")
  await act(async () => vi.advanceTimersByTimeAsync(9000))
  expect(fetch).toHaveBeenCalledTimes(2)
})

it("refreshes only events identifying this run, including the legacy identifier", async () => {
  fetch.mockImplementation(async () => reply({ id: "run", status: "completed" }))
  const { result, unmount } = renderHook(() => useTrace("ws", "run"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  for (const payload of [undefined, {}, { run_id: "other" }]) {
    await act(async () => events.get("pipeline.run.completed")!({ payload }))
  }
  expect(fetch).toHaveBeenCalledOnce()
  for (const [name, callback] of events) {
    await act(async () => callback({ payload: { run_id: "run" } }))
    expect(fetch.mock.calls.at(-1)?.[0], name).toContain("/pipeline-runs/run")
  }
  await act(async () => events.get("pipeline.step.completed")!({ payload: { pipeline_run_id: "run" } }))
  expect(fetch).toHaveBeenCalledTimes(9)
  const signal = fetch.mock.calls.at(-1)?.[1]?.signal
  unmount()
  expect(signal?.aborted).toBe(true)
})
