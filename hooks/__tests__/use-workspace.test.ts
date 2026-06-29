import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const mockFetch = vi.fn()
vi.stubGlobal("fetch", mockFetch)

import { useWorkspace, _resetWorkspaceStoreForTests } from "@/hooks/use-workspace"

const WS_A = { id: "ws-a", name: "Acme", slug: "acme", currentUserRole: "OWNER" }
const WS_B = { id: "ws-b", name: "Beta", slug: "beta", currentUserRole: "MEMBER" }

describe("useWorkspace", () => {
  // The shared vitest setup installs a stubbed localStorage whose getItem
  // always returns null. Swap in a Map-backed impl so persistence tests
  // observe real reads/writes.
  let storage: Map<string, string>
  beforeEach(() => {
    mockFetch.mockReset()
    storage = new Map<string, string>()
    vi.spyOn(window.localStorage, "getItem").mockImplementation((k) => storage.get(k) ?? null)
    vi.spyOn(window.localStorage, "setItem").mockImplementation((k, v) => {
      storage.set(k, String(v))
    })
    vi.spyOn(window.localStorage, "removeItem").mockImplementation((k) => {
      storage.delete(k)
    })
    vi.spyOn(window.localStorage, "clear").mockImplementation(() => {
      storage.clear()
    })
    _resetWorkspaceStoreForTests()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("starts in loading state", () => {
    mockFetch.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useWorkspace())
    expect(result.current.loading).toBe(true)
    expect(result.current.workspaceId).toBeNull()
    expect(result.current.workspaces).toEqual([])
    expect(result.current.workspace).toBeNull()
    expect(result.current.role).toBeNull()
  })

  it("fetches workspaces and selects the first as default", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.workspaceId).toBe("ws-a")
    expect(result.current.role).toBe("OWNER")
    expect(result.current.workspace).toEqual(WS_A)
    expect(result.current.workspaces).toEqual([WS_A, WS_B])
  })

  it("handles empty workspaces array", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [] })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaceId).toBeNull()
    expect(result.current.role).toBeNull()
    expect(result.current.workspaces).toEqual([])
  })

  it("handles API error response", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 500 })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaceId).toBeNull()
    expect(result.current.workspaces).toEqual([])
  })

  it("handles network error", async () => {
    mockFetch.mockRejectedValue(new Error("Network error"))
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaceId).toBeNull()
  })

  it("calls /api/v1/workspaces endpoint", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [] })
    renderHook(() => useWorkspace())
    await waitFor(() => expect(mockFetch).toHaveBeenCalledWith("/api/v1/workspaces", expect.objectContaining({ credentials: "include" })))
  })

  it("handles workspace with null role", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => [{ ...WS_A, currentUserRole: null }],
    })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaceId).toBe("ws-a")
    expect(result.current.role).toBeNull()
  })

  it("setWorkspaceId switches the active workspace and updates role", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => result.current.setWorkspaceId("ws-b"))

    expect(result.current.workspaceId).toBe("ws-b")
    expect(result.current.role).toBe("MEMBER")
    expect(result.current.workspace).toEqual(WS_B)
  })

  it("setWorkspaceId is a no-op for an unknown id", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => result.current.setWorkspaceId("does-not-exist"))

    expect(result.current.workspaceId).toBe("ws-a")
  })

  it("persists the selected workspace id to localStorage", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => result.current.setWorkspaceId("ws-b"))

    expect(window.localStorage.getItem("crewship.workspaceId")).toBe("ws-b")
  })

  it("restores the persisted workspace id on next mount", async () => {
    window.localStorage.setItem("crewship.workspaceId", "ws-b")
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })

    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.workspaceId).toBe("ws-b")
    expect(result.current.role).toBe("MEMBER")
  })

  it("falls back to the first workspace when persisted id is stale", async () => {
    window.localStorage.setItem("crewship.workspaceId", "ws-gone")
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })

    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.workspaceId).toBe("ws-a")
  })

  it("shares state across hook instances (module-level store)", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: async () => [WS_A, WS_B] })

    const { result: a } = renderHook(() => useWorkspace())
    const { result: b } = renderHook(() => useWorkspace())
    await waitFor(() => expect(a.current.loading).toBe(false))

    act(() => a.current.setWorkspaceId("ws-b"))

    expect(b.current.workspaceId).toBe("ws-b")
    expect(b.current.workspace).toEqual(WS_B)
  })

  it("refresh re-fetches and picks up new workspaces", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => [WS_A] })
    const { result } = renderHook(() => useWorkspace())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.workspaces).toEqual([WS_A])

    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => [WS_A, WS_B] })
    await act(async () => {
      await result.current.refresh()
    })

    expect(result.current.workspaces).toEqual([WS_A, WS_B])
  })
})
