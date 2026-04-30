import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useApprovals, decideApproval } from "@/hooks/use-approvals"

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function errJSON(status: number, body: unknown): Response {
  return { ok: false, status, json: async () => body } as unknown as Response
}

const sampleRow = {
  id: "appr_1",
  kind: "destructive_op",
  reason: "rm -rf /important",
  status: "pending",
  created_at: "2026-04-30T10:00:00Z",
}

beforeEach(() => {
  vi.useFakeTimers()
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("useApprovals", () => {
  it("loads pending rows on mount", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ rows: [sampleRow], status: "pending", count: 1 }),
    )

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.rows).toHaveLength(1)
    expect(result.current.rows[0].id).toBe("appr_1")
    expect(result.current.error).toBeNull()
  })

  it("reports notConfigured on 404 (Harbormaster not enabled)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(errJSON(404, {}))

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.notConfigured).toBe(true)
    expect(result.current.rows).toEqual([])
  })

  it("surfaces backend error message instead of bare HTTP code", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      errJSON(403, { error: "approval decisions require OWNER or ADMIN role" }),
    )

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("approval decisions require OWNER or ADMIN role")
  })

  it("falls back to HTTP code when backend error JSON is malformed", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => {
        throw new Error("not json")
      },
    } as unknown as Response)

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("HTTP 500")
  })

  it("surfaces malformed response error rather than silent empty", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ wrong: "shape" }))

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toMatch(/Malformed/i)
    expect(result.current.rows).toEqual([])
  })

  it("does not poll when status != pending", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))

    renderHook(() => useApprovals({ status: "approved", pollMs: 1000 }))
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    // Advance fake clock 5 seconds; pending-only polling means no extra
    // fetches for status=approved.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000)
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("polls every pollMs when status=pending", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))

    renderHook(() => useApprovals({ status: "pending", pollMs: 1000 }))
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3500)
    })
    // initial + 3 polls
    expect(fetchMock).toHaveBeenCalledTimes(4)
  })

  it("does not fetch when enabled=false but still drops loading", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0, enabled: false }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("patchRow optimistically updates a row by id", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ rows: [sampleRow] }))

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      result.current.patchRow("appr_1", { status: "approved", decided_by: "alice" })
    })
    expect(result.current.rows[0].status).toBe("approved")
    expect(result.current.rows[0].decided_by).toBe("alice")
  })

  it("patchRow with non-matching id is a no-op", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ rows: [sampleRow] }))

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))

    const before = result.current.rows[0]
    act(() => {
      result.current.patchRow("appr_unknown", { status: "approved" })
    })
    expect(result.current.rows[0]).toEqual(before)
  })

  it("refresh forces a new fetch even with the same status", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))

    const { result } = renderHook(() => useApprovals({ status: "pending", pollMs: 0 }))
    await vi.waitFor(() => expect(result.current.loading).toBe(false))
    expect(fetchMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      await result.current.refresh()
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})

describe("decideApproval", () => {
  it("posts JSON to /decide and returns the parsed result", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ status: "approved", decided_by: "alice" }),
    )

    const got = await decideApproval("appr_1", "approved", "looks good")
    expect(got.status).toBe("approved")
    expect(got.decided_by).toBe("alice")

    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/approvals/appr_1/decide",
      expect.objectContaining({
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ status: "approved", comment: "looks good" }),
      }),
    )
  })

  it("URL-encodes the id (defends against path-ish input)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ status: "denied" }))

    await decideApproval("appr/with slash", "denied", "")
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain("appr%2Fwith%20slash")
  })

  it("throws backend error message on non-OK", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      errJSON(409, { error: "already decided" }),
    )

    await expect(decideApproval("appr_1", "approved", "")).rejects.toThrow("already decided")
  })

  it("throws Malformed when response shape is wrong", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ unexpected: 1 }))

    await expect(decideApproval("appr_1", "approved", "")).rejects.toThrow(/Malformed/i)
  })
})
