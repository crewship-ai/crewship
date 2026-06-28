import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"
import { z } from "zod"

import { useApiResource } from "@/hooks/use-api-resource"

// useApiResource is built on apiFetch, which for any non-401 response
// returns the raw fetch Response. Mocking global.fetch therefore drives
// the hook end-to-end, matching how the bespoke copies were tested.
function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}
function notFound(): Response {
  return { ok: false, status: 404, json: async () => ({}) } as unknown as Response
}
function httpError(status: number): Response {
  return { ok: false, status, json: async () => ({}) } as unknown as Response
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

const rowSchema = z.object({ rows: z.array(z.object({ id: z.string() })) })
type Rows = z.infer<typeof rowSchema>

beforeEach(() => {
  global.fetch = vi.fn()
})
afterEach(() => {
  vi.restoreAllMocks()
})

describe("useApiResource", () => {
  it("loads and parses a successful response", async () => {
    const body = { rows: [{ id: "a" }, { id: "b" }] }
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON(body))

    const { result } = renderHook(() => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema }))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.data).toEqual(body)
    expect(result.current.error).toBeNull()
    expect(result.current.notConfigured).toBe(false)
  })

  it("casts the body when no schema is supplied", async () => {
    const body = [{ id: "x" }]
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON(body))

    const { result } = renderHook(() => useApiResource<{ id: string }[]>("/api/v1/list"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.data).toEqual(body)
  })

  it("maps 404 to notConfigured by default", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(notFound())

    const { result } = renderHook(() => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.notConfigured).toBe(true)
    expect(result.current.error).toBeNull()
    expect(result.current.data).toBeNull()
  })

  it("maps 404 to an HTTP error when on404 is 'error'", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(notFound())

    const { result } = renderHook(() =>
      useApiResource<Rows>("/api/v1/thing", { schema: rowSchema, on404: "error" }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.notConfigured).toBe(false)
    expect(result.current.error).toBe("HTTP 404")
  })

  it("reports `HTTP <status>` for a non-ok response", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(httpError(503))

    const { result } = renderHook(() => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("HTTP 503")
    expect(result.current.notConfigured).toBe(false)
    expect(result.current.data).toBeNull()
  })

  it("reports 'Network error' when the fetch rejects", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error("offline"))

    const { result } = renderHook(() => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema }))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("Network error")
  })

  it("falls back to `fallback` on schema parse failure", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ rows: "not-an-array" }))

    const fallback: Rows = { rows: [] }
    const { result } = renderHook(() =>
      useApiResource<Rows>("/api/v1/thing", { schema: rowSchema, fallback }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.data).toEqual({ rows: [] })
    expect(result.current.error).toBeNull()
  })

  it("keeps prior data on parse failure when no fallback is given", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ id: "a" }] }))

    const { result, rerender } = renderHook(
      ({ key }: { key: number }) => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema, reloadKey: key }),
      { initialProps: { key: 0 } },
    )
    await waitFor(() => expect(result.current.data).toEqual({ rows: [{ id: "a" }] }))

    fetchMock.mockResolvedValueOnce(okJSON({ rows: "bad" }))
    rerender({ key: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

    // Previous good data is preserved (not blanked) on a parse regression.
    expect(result.current.data).toEqual({ rows: [{ id: "a" }] })
  })

  it("keeps prior data on transient error when keepDataOnError is set", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON([{ id: "a" }]))

    const { result, rerender } = renderHook(
      ({ key }: { key: number }) =>
        useApiResource<{ id: string }[]>("/api/v1/list", { keepDataOnError: true, reloadKey: key }),
      { initialProps: { key: 0 } },
    )
    await waitFor(() => expect(result.current.data).toEqual([{ id: "a" }]))

    fetchMock.mockResolvedValueOnce(httpError(500))
    rerender({ key: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

    // List survives the backend hiccup instead of flashing to empty.
    expect(result.current.data).toEqual([{ id: "a" }])
    expect(result.current.error).toBe("HTTP 500")
  })

  it("ignores a stale response that resolves after a newer one (race guard)", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const first = deferred<Response>()
    const second = deferred<Response>()
    fetchMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const { result, rerender } = renderHook(
      ({ url }: { url: string }) => useApiResource<Rows>(url, { schema: rowSchema }),
      { initialProps: { url: "/api/v1/a" } },
    )
    // Switch url before the first request resolves -> a second request fires.
    rerender({ url: "/api/v1/b" })

    // Resolve the NEWER request first.
    await act(async () => {
      second.resolve(okJSON({ rows: [{ id: "new" }] }))
    })
    await waitFor(() => expect(result.current.data).toEqual({ rows: [{ id: "new" }] }))

    // Now the stale (older) request resolves — it must NOT overwrite.
    await act(async () => {
      first.resolve(okJSON({ rows: [{ id: "stale" }] }))
    })
    expect(result.current.data).toEqual({ rows: [{ id: "new" }] })
  })

  it("reload() refetches and updates data", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ id: "first" }] }))

    const { result } = renderHook(() => useApiResource<Rows>("/api/v1/thing", { schema: rowSchema }))
    await waitFor(() => expect(result.current.data).toEqual({ rows: [{ id: "first" }] }))

    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ id: "second" }] }))
    await act(async () => {
      await result.current.reload()
    })
    await waitFor(() => expect(result.current.data).toEqual({ rows: [{ id: "second" }] }))
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("does not fetch when disabled (url null) and resets when resetOnDisable", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() =>
      useApiResource<Rows>(null, { schema: rowSchema, resetOnDisable: true }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current.data).toBeNull()
  })

  it("does not fetch when enabled=false", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() =>
      useApiResource<Rows>("/api/v1/thing", { schema: rowSchema, enabled: false }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).not.toHaveBeenCalled()
  })
})
