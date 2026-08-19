import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useApiMutation, ApiMutationError, type ApiMutationOutcome } from "@/hooks/use-api-mutation"

function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

function jsonResponse(status: number, body: unknown, headers?: Record<string, string>): Response {
  const h = new Headers(headers)
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: h,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function emptyResponse(status: number, headers?: Record<string, string>): Response {
  const h = new Headers(headers)
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: h,
    text: async () => "",
    json: async () => {
      throw new Error("no body")
    },
  } as unknown as Response
}

describe("useApiMutation", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  // Rule 1 + rule 3 (#1563): a refused write must not report success and
  // must not invalidate anything a retry would need re-fetched.
  it("a refused write does not resolve as ok and does not invalidate", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse(403, { error: "not your crew" }))
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries")
    const onOk = vi.fn()

    const { result } = renderHook(
      () =>
        useApiMutation<void, { id: string }>({
          request: () => ({
            input: "/api/v1/widgets/1",
            init: { method: "PATCH", headers: { "Content-Type": "application/json" } },
          }),
          invalidateKeys: [["widgets"]],
          onOk,
        }),
      { wrapper: makeWrapper(qc) },
    )

    await act(async () => {
      await expect(result.current.mutateAsync(undefined)).rejects.toThrow()
    })

    expect(onOk).not.toHaveBeenCalled()
    expect(invalidateSpy).not.toHaveBeenCalled()
    await waitFor(() => expect(result.current.error).toBeInstanceOf(ApiMutationError))
  })

  // Rule 2: the server's own words, not a generic client-side message.
  it("surfaces the server's own message via readApiError", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse(422, { detail: "widget is archived" }))

    const { result } = renderHook(
      () =>
        useApiMutation({
          request: () => ({ input: "/api/v1/widgets/1", init: { method: "PATCH" } }),
        }),
      { wrapper: makeWrapper(qc) },
    )

    await act(async () => {
      await expect(result.current.mutateAsync(undefined)).rejects.toThrow("widget is archived")
    })

    await waitFor(() => expect(result.current.error).toBeInstanceOf(ApiMutationError))
    const err = result.current.error
    expect((err as ApiMutationError).message).toBe("widget is archived")
    expect((err as ApiMutationError).status).toBe(422)
  })

  // Rule 2's fallback: no server body at all still names the status.
  it("falls back to a (HTTP <status>) message when the body carries none", async () => {
    mockFetch.mockResolvedValueOnce(emptyResponse(500))

    const { result } = renderHook(
      () => useApiMutation({ request: () => ({ input: "/api/v1/widgets/1", init: { method: "POST" } }) }),
      { wrapper: makeWrapper(qc) },
    )

    await act(async () => {
      await expect(result.current.mutateAsync(undefined)).rejects.toThrow()
    })

    await waitFor(() => expect(result.current.error).toBeInstanceOf(ApiMutationError))
    expect((result.current.error as ApiMutationError).message).toContain("(HTTP 500)")
  })

  // 429 is a first-class "already running" outcome, not a generic error —
  // no toast-as-failure, no invalidation, and the Retry-After survives.
  it("reports a 429 as already-running, carrying Retry-After, without invalidating", async () => {
    mockFetch.mockResolvedValueOnce(
      jsonResponse(429, { error: "run already in progress" }, { "Retry-After": "30" }),
    )
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries")
    const onError = vi.fn()
    const onAlreadyRunning = vi.fn()

    const { result } = renderHook(
      () =>
        useApiMutation({
          request: () => ({ input: "/api/v1/pipelines/1/run", init: { method: "POST" } }),
          invalidateKeys: [["pipelines"]],
          onError,
          onAlreadyRunning,
        }),
      { wrapper: makeWrapper(qc) },
    )

    let outcome: ApiMutationOutcome<unknown> | undefined
    await act(async () => {
      outcome = await result.current.mutateAsync(undefined)
    })

    expect(outcome).toEqual({
      kind: "already-running",
      status: 429,
      retryAfterSeconds: 30,
      message: "run already in progress",
    })
    await waitFor(() => expect(result.current.isAlreadyRunning).toBe(true))
    expect(result.current.error).toBeUndefined()
    expect(onError).not.toHaveBeenCalled()
    expect(onAlreadyRunning).toHaveBeenCalledTimes(1)
    expect(invalidateSpy).not.toHaveBeenCalled()
  })

  // 202 is success, not completion: the caller gets the pending/run id back.
  it("resolves a 202 as accepted, invalidates, and hands back the parsed id", async () => {
    mockFetch.mockResolvedValueOnce(jsonResponse(202, { run_id: "run-42" }))
    const onAccepted = vi.fn()

    const { result } = renderHook(
      () =>
        useApiMutation<void, { run_id: string }>({
          request: () => ({ input: "/api/v1/pipelines/1/run", init: { method: "POST" } }),
          invalidateKeys: [["pipelines"]],
          onAccepted,
        }),
      { wrapper: makeWrapper(qc) },
    )

    let outcome: ApiMutationOutcome<{ run_id: string }> | undefined
    await act(async () => {
      outcome = await result.current.mutateAsync(undefined)
    })

    expect(outcome).toEqual({ kind: "accepted", status: 202, data: { run_id: "run-42" } })
    expect(onAccepted).toHaveBeenCalledWith({ run_id: "run-42" }, undefined)
  })

  // Rule 4: a transport failure and a 500 must be distinguishable so a
  // caller can tell "the server said no" from "we don't know what happened".
  it("distinguishes a transport failure from a 500", async () => {
    mockFetch.mockRejectedValueOnce(new TypeError("Failed to fetch"))

    const { result } = renderHook(
      () => useApiMutation({ request: () => ({ input: "/api/v1/widgets/1", init: { method: "POST" } }) }),
      { wrapper: makeWrapper(qc) },
    )

    await act(async () => {
      await expect(result.current.mutateAsync(undefined)).rejects.toThrow("Failed to fetch")
    })

    await waitFor(() => expect(result.current.error).toBeInstanceOf(TypeError))
    expect(result.current.error).not.toBeInstanceOf(ApiMutationError)
  })

  // The Stripe pattern: a retry of the SAME click reuses its idempotency
  // key; a fresh mutate() call mints a new one.
  it("reuses the idempotency key on retry() and mints a fresh one on a new mutate()", async () => {
    mockFetch
      .mockResolvedValueOnce(jsonResponse(500, { error: "boom" })) // first attempt
      .mockResolvedValueOnce(jsonResponse(200, { ok: true })) // retry of same click
      .mockResolvedValueOnce(jsonResponse(200, { ok: true })) // second, independent click

    const { result } = renderHook(
      () =>
        useApiMutation<{ n: number }, { ok: boolean }>({
          request: (variables) => ({
            input: `/api/v1/widgets/${variables.n}`,
            init: { method: "POST" },
          }),
        }),
      { wrapper: makeWrapper(qc) },
    )

    await act(async () => {
      await expect(result.current.mutateAsync({ n: 1 })).rejects.toThrow()
    })
    const firstKey = (mockFetch.mock.calls[0][1] as RequestInit).headers as Headers
    const firstIdempotencyKey = new Headers(firstKey).get("Idempotency-Key")
    expect(firstIdempotencyKey).toBeTruthy()

    await act(async () => {
      await result.current.retryAsync()
    })
    const retryHeaders = new Headers((mockFetch.mock.calls[1][1] as RequestInit).headers)
    expect(retryHeaders.get("Idempotency-Key")).toBe(firstIdempotencyKey)

    await act(async () => {
      await result.current.mutateAsync({ n: 2 })
    })
    const secondClickHeaders = new Headers((mockFetch.mock.calls[2][1] as RequestInit).headers)
    expect(secondClickHeaders.get("Idempotency-Key")).toBeTruthy()
    expect(secondClickHeaders.get("Idempotency-Key")).not.toBe(firstIdempotencyKey)
  })

  it("makes double-submit impossible: a second mutate() while pending reuses the in-flight call", async () => {
    let resolveFetch: (res: Response) => void
    mockFetch.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve
        }),
    )

    const { result } = renderHook(
      () => useApiMutation({ request: () => ({ input: "/api/v1/widgets/1", init: { method: "POST" } }) }),
      { wrapper: makeWrapper(qc) },
    )

    let firstPromise: Promise<unknown>
    let secondPromise: Promise<unknown>
    await act(async () => {
      firstPromise = result.current.mutateAsync(undefined)
      secondPromise = result.current.mutateAsync(undefined)
      await Promise.resolve()
    })

    expect(mockFetch).toHaveBeenCalledTimes(1)

    await act(async () => {
      resolveFetch!(jsonResponse(200, { ok: true }))
      await Promise.all([firstPromise, secondPromise])
    })

    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("exposes isPending so a button can disable itself while a call is in flight", async () => {
    let resolveFetch: (res: Response) => void
    mockFetch.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          resolveFetch = resolve
        }),
    )

    const { result } = renderHook(
      () => useApiMutation({ request: () => ({ input: "/api/v1/widgets/1", init: { method: "POST" } }) }),
      { wrapper: makeWrapper(qc) },
    )

    expect(result.current.isPending).toBe(false)

    let p: Promise<unknown>
    act(() => {
      p = result.current.mutateAsync(undefined)
    })

    await waitFor(() => expect(result.current.isPending).toBe(true))

    await act(async () => {
      resolveFetch!(jsonResponse(200, { ok: true }))
      await p
    })

    await waitFor(() => expect(result.current.isPending).toBe(false))
  })
})
