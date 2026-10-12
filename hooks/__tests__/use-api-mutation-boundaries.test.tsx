import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { useApiMutation } from "@/hooks/use-api-mutation"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

let client: QueryClient
function Wrapper({ children }: { children: React.ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
const request = () => ({ input: "/api/v1/widgets" })

beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("mutation response and retry boundaries", () => {
  it.each([
    [null, null], ["", null], ["-1", null], ["1.5", null], ["0x10", null],
    ["1e3", null], ["Infinity", null], ["Wed, 21 Oct 2015 07:28:00 GMT", null],
    ["9007199254740993", null], ["0", 0], ["05", 5], ["120", 120],
  ])("only exposes integer retry delays for header %s", async (header, expected) => {
    const headers = header === null ? undefined : { "Retry-After": header }
    vi.mocked(apiFetch).mockResolvedValue(new Response("", { status: 429, headers }))
    const invalidation = vi.spyOn(client, "invalidateQueries")
    const { result } = renderHook(() => useApiMutation({ request, invalidateKeys: [["widgets"]] }), { wrapper: Wrapper })
    await act(async () => {
      expect(await result.current.mutateAsync(undefined)).toEqual({ kind: "already-running", status: 429, message: "Already running (HTTP 429)", retryAfterSeconds: expected })
    })
    expect(invalidation).not.toHaveBeenCalled()
  })

  it.each([204, 202, 200])("accepts an empty successful %i response", async (status) => {
    vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status }))
    const { result } = renderHook(() => useApiMutation({ request }), { wrapper: Wrapper })
    await act(async () => {
      expect(await result.current.mutateAsync(undefined)).toEqual({ kind: status === 202 ? "accepted" : "ok", status, data: undefined })
    })
  })

  it("tolerates non-JSON success and honors a custom parser", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => new Response("created"))
    const { result, rerender } = renderHook(({ parse }) => useApiMutation<void, unknown>({ request, parse }), {
      wrapper: Wrapper, initialProps: { parse: undefined as ((res: Response) => Promise<unknown>) | undefined },
    })
    await act(async () => { expect(await result.current.mutateAsync(undefined)).toMatchObject({ kind: "ok", data: undefined }) })
    rerender({ parse: async (response) => response.text() })
    await act(async () => { expect(await result.current.mutateAsync(undefined)).toMatchObject({ kind: "ok", data: "created" }) })
  })

  it("does not invalidate or report success when a custom parser fails", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response("invalid result"))
    const onOk = vi.fn()
    const onError = vi.fn()
    const invalidation = vi.spyOn(client, "invalidateQueries")
    const failure = new Error("unreadable result")
    const { result } = renderHook(() => useApiMutation({ request, parse: async () => { throw failure }, onOk, onError, invalidateKeys: [["widgets"]] }), { wrapper: Wrapper })
    await act(async () => { await expect(result.current.mutateAsync(undefined)).rejects.toBe(failure) })
    expect(onError).toHaveBeenCalledWith(failure, undefined)
    expect(onOk).not.toHaveBeenCalled()
    expect(invalidation).not.toHaveBeenCalled()
  })

  it("makes retry before a click and after reset a no-op", async () => {
    vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status: 204 }))
    const { result } = renderHook(() => useApiMutation({ request }), { wrapper: Wrapper })
    await act(async () => { result.current.retry(); expect(await result.current.retryAsync()).toBeUndefined() })
    expect(apiFetch).not.toHaveBeenCalled()
    await act(async () => { await result.current.mutateAsync(undefined) })
    act(() => { result.current.reset() })
    await waitFor(() => { expect(result.current.data).toBeUndefined(); expect(result.current.error).toBeUndefined() })
    await act(async () => { expect(await result.current.retryAsync()).toBeUndefined() })
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })

  it("reports fire-and-forget failures and retries with the same key", async () => {
    vi.mocked(apiFetch).mockImplementation(async () => new Response('{"error":"retry refused"}', { status: 503 }))
    const onError = vi.fn()
    const { result } = renderHook(() => useApiMutation({ request, onError }), { wrapper: Wrapper })
    act(() => { result.current.mutate(undefined) })
    await waitFor(() => expect(onError).toHaveBeenCalledTimes(1))
    act(() => { result.current.retry() })
    await waitFor(() => expect(onError).toHaveBeenCalledTimes(2))
    const keys = vi.mocked(apiFetch).mock.calls.map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"))
    expect(keys[0]).toBeTruthy()
    expect(keys[1]).toBe(keys[0])
  })

  it.each([undefined, {}])("keeps non-secure origins usable without randomUUID (%s)", async (cryptoValue) => {
    vi.stubGlobal("crypto", cryptoValue)
    vi.mocked(apiFetch).mockImplementation(async () => new Response(null, { status: 204 }))
    const { result } = renderHook(() => useApiMutation({ request }), { wrapper: Wrapper })
    await act(async () => { await result.current.mutateAsync(undefined); await result.current.retryAsync(); await result.current.mutateAsync(undefined) })
    const keys = vi.mocked(apiFetch).mock.calls.map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"))
    expect(keys[0]).toMatch(/^[\da-f]{8}-[\da-f]{4}-4[\da-f]{3}-[89ab][\da-f]{3}-[\da-f]{12}$/)
    expect(keys[1]).toBe(keys[0])
    expect(keys[2]).not.toBe(keys[0])
  })

  it("collapses an ambiguous unserializable double click while its request is pending", async () => {
    const pending = deferred<Response>()
    vi.mocked(apiFetch).mockReturnValue(pending.promise)
    const { result } = renderHook(() => useApiMutation<{ id: bigint }>({ request }), { wrapper: Wrapper })
    let first!: Promise<unknown>
    let second!: Promise<unknown>
    await act(async () => {
      first = result.current.mutateAsync({ id: 1n })
      second = result.current.mutateAsync({ id: 1n })
    })
    expect(second).toBe(first)
    expect(apiFetch).toHaveBeenCalledTimes(1)
    await act(async () => { pending.resolve(new Response(null, { status: 204 })); await first })
  })
})
