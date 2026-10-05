import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { useAgentFetch } from "@/hooks/use-agent-fetch"

afterEach(() => { cleanup(); vi.restoreAllMocks() })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

it("ignores an old request failure after a newer selection succeeds", async () => {
  const old = deferred<string | null>()
  const fetcher = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValueOnce("new selection")
  const log = vi.spyOn(console, "error").mockImplementation(() => {})
  const { result, rerender } = renderHook(({ scope }) => useAgentFetch(fetcher, [scope], { logLabel: "Panel" }), { initialProps: { scope: "old" } })
  const signal = fetcher.mock.calls[0][0] as AbortSignal
  rerender({ scope: "new" })
  await waitFor(() => expect(result.current.data).toBe("new selection"))
  expect(signal.aborted).toBe(true)
  await act(async () => { old.reject(new Error("late network failure")) })
  expect(result.current).toEqual({ data: "new selection", loading: false, error: null })
  expect(log).not.toHaveBeenCalled()
})

it("responds to enable changes even when the caller's dependencies stay fixed", async () => {
  const fetcher = vi.fn().mockResolvedValue("loaded")
  const { result, rerender } = renderHook(({ enabled }) => useAgentFetch(fetcher, [], { enabled }), { initialProps: { enabled: false } })
  expect(result.current).toEqual({ data: null, loading: false, error: null })
  expect(fetcher).not.toHaveBeenCalled()
  rerender({ enabled: true })
  await waitFor(() => expect(result.current.data).toBe("loaded"))
  rerender({ enabled: false })
  expect(result.current).toEqual({ data: null, loading: false, error: null })
  expect((fetcher.mock.calls[0][0] as AbortSignal).aborted).toBe(true)
})

it("ignores a late body after disable", async () => {
  const pending = deferred<string | null>()
  const fetcher = vi.fn(() => pending.promise)
  const { result, rerender } = renderHook(({ enabled }) => useAgentFetch(fetcher, [enabled], { enabled }), { initialProps: { enabled: true } })
  rerender({ enabled: false })
  await act(async () => { pending.resolve("obsolete") })
  expect(result.current).toEqual({ data: null, loading: false, error: null })
})

it.each([undefined, "Panel"])("reports current failures with optional log label %s", async (logLabel) => {
  const failure = new Error("offline")
  const log = vi.spyOn(console, "error").mockImplementation(() => {})
  const { result } = renderHook(() => useAgentFetch(() => Promise.reject(failure), [], { logLabel }))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current).toEqual({ data: null, loading: false, error: failure })
  if (logLabel) expect(log).toHaveBeenCalledWith("Panel: fetch failed", failure)
  else expect(log).not.toHaveBeenCalled()
})

it("treats an AbortError as cancellation", async () => {
  const { result } = renderHook(() => useAgentFetch(() => Promise.reject(new DOMException("cancelled", "AbortError")), []))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.error).toBeNull()
})

it("accepts an empty response and aborts on unmount", async () => {
  let signal: AbortSignal | undefined
  const { result, unmount } = renderHook(() => useAgentFetch((s) => { signal = s; return Promise.resolve(null) }, []))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.data).toBeNull()
  unmount()
  expect(signal?.aborted).toBe(true)
})
