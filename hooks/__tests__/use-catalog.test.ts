import { describe, it, expect, vi, beforeEach, afterAll } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

const mockFetch = vi.fn()
vi.stubGlobal("fetch", mockFetch)

import { useCatalog } from "@/hooks/use-catalog"

interface Item {
  id: string
}

const extract = (j: unknown): Item[] => {
  const items = (j as { items?: unknown })?.items
  return Array.isArray(items) ? (items as Item[]) : []
}

describe("useCatalog", () => {
  beforeEach(() => {
    mockFetch.mockReset()
  })

  afterAll(() => {
    vi.unstubAllGlobals()
  })

  it("starts in loading state when enabled", () => {
    mockFetch.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    expect(result.current.loading).toBe(true)
    expect(result.current.data).toBeNull()
    expect(result.current.error).toBeNull()
  })

  it("does not fetch when disabled", () => {
    const { result } = renderHook(() =>
      useCatalog<Item>("/api/x", extract, false),
    )
    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current.loading).toBe(false)
  })

  it("populates data on success", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ items: [{ id: "a" }, { id: "b" }] }),
    })
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data).toEqual([{ id: "a" }, { id: "b" }])
    expect(result.current.error).toBeNull()
  })

  it("sets error on non-OK response", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 500 })
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBeInstanceOf(Error)
    expect(result.current.data).toEqual([])
  })

  it("sets error on network failure", async () => {
    mockFetch.mockRejectedValue(new Error("offline"))
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error?.message).toBe("offline")
    expect(result.current.data).toEqual([])
  })

  it("refetch triggers another request", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ items: [] }),
    })
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    act(() => result.current.refetch())
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))
  })

  it("re-fetches when url changes", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ items: [] }),
    })
    const { rerender } = renderHook(
      ({ url }) => useCatalog<Item>(url, extract),
      { initialProps: { url: "/api/a" } },
    )
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    rerender({ url: "/api/b" })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))
    expect(mockFetch.mock.calls[1][0]).toBe("/api/b")
  })

  it("ignores AbortError", async () => {
    const abortErr = new DOMException("aborted", "AbortError")
    mockFetch.mockRejectedValue(abortErr)
    const { result } = renderHook(() => useCatalog<Item>("/api/x", extract))
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))
    expect(result.current.error).toBeNull()
  })

it.each([
  [new DOMException("blocked by browser policy", "SecurityError"), "blocked by browser policy"],
  ["transport refused", "transport refused"],
])("reports non-abort failures without leaving catalog loading", async (failure, message) => {
  mockFetch.mockRejectedValue(failure)
  const { result, unmount } = renderHook(() => useCatalog<Item>("/api/catalog-errors", extract))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.error?.message).toBe(message)
  expect(result.current.data).toEqual([])
  unmount()
})


it.each(["success", "failure"])("ignores obsolete catalog %s after the URL changes", async outcome => {
  let resolve!: (value: unknown) => void
  let reject!: (reason: Error) => void
  const pending = new Promise((yes, no) => { resolve = yes; reject = no })
  mockFetch.mockReturnValueOnce(pending).mockResolvedValueOnce({ ok: true, json: async () => ({ items: [{ id: "current" }] }) })
  const view = renderHook(({ url }) => useCatalog<Item>(url, extract), { initialProps: { url: "/api/old" } })
  const oldSignal = mockFetch.mock.calls[0][1].signal as AbortSignal
  view.rerender({ url: "/api/current" })
  await waitFor(() => expect(view.result.current.data).toEqual([{ id: "current" }]))
  expect(oldSignal.aborted).toBe(true)
  await act(async () => {
    if (outcome === "success") resolve({ ok: true, json: async () => ({ items: [{ id: "obsolete" }] }) })
    else reject(new Error("obsolete failure"))
  })
  expect(view.result.current).toMatchObject({ data: [{ id: "current" }], error: null, loading: false })
  view.unmount()
})
})
