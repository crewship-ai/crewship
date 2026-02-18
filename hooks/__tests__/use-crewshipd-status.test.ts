import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

const mockFetch = vi.fn()
vi.stubGlobal("fetch", mockFetch)

import { useCrewshipdStatus } from "@/hooks/use-crewshipd-status"

describe("useCrewshipdStatus", () => {
  beforeEach(() => {
    mockFetch.mockReset()
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("starts in checking state", () => {
    mockFetch.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useCrewshipdStatus("ws-1"))
    expect(result.current.status).toBe("checking")
    expect(result.current.uptime).toBeNull()
  })

  it("does nothing when workspaceId is null", () => {
    const { result } = renderHook(() => useCrewshipdStatus(null))
    expect(result.current.status).toBe("checking")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("sets connected when API responds OK", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok", uptime: "2h 15m" }),
    })

    const { result } = renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("connected")
    })

    expect(result.current.uptime).toBe("2h 15m")
  })

  it("sets disconnected when API responds with error", async () => {
    mockFetch.mockResolvedValue({ ok: false, status: 502 })

    const { result } = renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("disconnected")
    })

    expect(result.current.uptime).toBeNull()
  })

  it("sets disconnected on network error", async () => {
    mockFetch.mockRejectedValue(new Error("Network error"))

    const { result } = renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("disconnected")
    })

    expect(result.current.uptime).toBeNull()
  })

  it("calls correct API endpoint with workspace_id", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    })

    renderHook(() => useCrewshipdStatus("ws-123"))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith(
        "/api/v1/crewshipd?workspace_id=ws-123",
        expect.objectContaining({ cache: "no-store" }),
      )
    })
  })

  it("handles response without uptime field", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    })

    const { result } = renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(result.current.status).toBe("connected")
    })

    expect(result.current.uptime).toBeNull()
  })

  it("polls at 10 second intervals", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok", uptime: "1m" }),
    })

    renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledTimes(1)
    })

    await act(async () => {
      vi.advanceTimersByTime(10_000)
    })

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledTimes(2)
    })
  })

  it("cleans up interval on unmount", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ status: "ok" }),
    })

    const { unmount } = renderHook(() => useCrewshipdStatus("ws-1"))

    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledTimes(1)
    })

    unmount()

    await act(async () => {
      vi.advanceTimersByTime(30_000)
    })

    expect(mockFetch).toHaveBeenCalledTimes(1)
  })
})
