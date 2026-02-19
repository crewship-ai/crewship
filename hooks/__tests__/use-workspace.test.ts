import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

const mockFetch = vi.fn()
vi.stubGlobal("fetch", mockFetch)

import { useWorkspace } from "@/hooks/use-workspace"

describe("useWorkspace", () => {
  beforeEach(() => {
    mockFetch.mockReset()
  })

  it("starts in loading state", () => {
    mockFetch.mockReturnValue(new Promise(() => {})) // never resolves
    const { result } = renderHook(() => useWorkspace())
    expect(result.current.loading).toBe(true)
    expect(result.current.workspaceId).toBeNull()
    expect(result.current.role).toBeNull()
  })

  it("fetches first workspace and sets workspaceId + role", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => [
        { id: "ws-1", name: "Test Workspace", slug: "test", currentUserRole: "ADMIN" },
        { id: "ws-2", name: "Other", slug: "other", currentUserRole: "MEMBER" },
      ],
    })

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(result.current.loading).toBe(false)
    })

    expect(result.current.workspaceId).toBe("ws-1")
    expect(result.current.role).toBe("ADMIN")
  })

  it("handles empty workspaces array", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => [],
    })

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(result.current.loading).toBe(false)
    })

    expect(result.current.workspaceId).toBeNull()
    expect(result.current.role).toBeNull()
  })

  it("handles API error response", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 500 })

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(result.current.loading).toBe(false)
    })

    expect(result.current.workspaceId).toBeNull()
    expect(result.current.role).toBeNull()
  })

  it("handles network error", async () => {
    mockFetch.mockRejectedValue(new Error("Network error"))

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(result.current.loading).toBe(false)
    })

    expect(result.current.workspaceId).toBeNull()
    expect(result.current.role).toBeNull()
  })

  it("calls /api/v1/workspaces endpoint", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => [],
    })

    renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith("/api/v1/workspaces")
    })
  })

  it("handles workspace with null role", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => [
        { id: "ws-1", name: "Test", slug: "test", currentUserRole: null },
      ],
    })

    const { result } = renderHook(() => useWorkspace())

    await waitFor(() => {
      expect(result.current.loading).toBe(false)
    })

    expect(result.current.workspaceId).toBe("ws-1")
    expect(result.current.role).toBeNull()
  })
})
