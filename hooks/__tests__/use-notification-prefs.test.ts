import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

import { useNotificationPrefs } from "@/hooks/use-notification-prefs"

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, error: string): Response {
  return {
    ok: false,
    status,
    json: async () => ({ error }),
  } as unknown as Response
}

const CELL = { category: "approvals", channel_id: "nch_1", state: "immediate" as const }

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useNotificationPrefs", () => {
  it("fetches the matrix on mount, scoped to the workspace", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON({ cells: [CELL] }))
    const { result } = renderHook(() => useNotificationPrefs("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBeNull()
    expect(result.current.cells).toHaveLength(1)
    expect(result.current.cells[0].category).toBe("approvals")
    const url = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0][0] as string
    expect(url).toContain("/api/v1/me/notification-prefs")
    expect(url).toContain("workspace_id=ws1")
  })

  it("does not fetch without a workspace id", async () => {
    const { result } = renderHook(() => useNotificationPrefs(null))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(global.fetch).not.toHaveBeenCalled()
    expect(result.current.cells).toEqual([])
  })

  it("surfaces a non-2xx fetch as error", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(errJSON(500, "boom"))
    const { result } = renderHook(() => useNotificationPrefs("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toContain("500")
  })

  it("setCell optimistically updates, then PUTs the single cell", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ cells: [] })) // mount
      .mockResolvedValueOnce(okJSON({ ok: true })) // put

    const { result } = renderHook(() => useNotificationPrefs("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await act(async () => {
      await result.current.setCell(CELL)
    })
    expect(result.current.cells).toEqual([CELL])
    const [putUrl, putInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(putUrl).toContain("/api/v1/me/notification-prefs")
    expect(putInit.method).toBe("PUT")
    expect(JSON.parse(putInit.body as string)).toEqual({ cells: [CELL] })
  })

  it("setCell updates an EXISTING cell in place rather than duplicating it", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ cells: [CELL] })) // mount
      .mockResolvedValueOnce(okJSON({ ok: true })) // put

    const { result } = renderHook(() => useNotificationPrefs("ws1"))
    await waitFor(() => expect(result.current.cells).toHaveLength(1))

    await act(async () => {
      await result.current.setCell({ ...CELL, state: "off" })
    })
    expect(result.current.cells).toHaveLength(1)
    expect(result.current.cells[0].state).toBe("off")
  })

  it("setCell rolls back the optimistic update on failure", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock
      .mockResolvedValueOnce(okJSON({ cells: [] })) // mount
      .mockResolvedValueOnce(errJSON(400, "notify: unknown state"))

    const { result } = renderHook(() => useNotificationPrefs("ws1"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    await expect(
      act(async () => {
        await result.current.setCell(CELL)
      }),
    ).rejects.toThrow(/unknown state/)
    expect(result.current.cells).toEqual([])
  })
})
