import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { useUserPreference } from "@/hooks/use-user-preference"

// A preference changed right before the page reloads (e2e routines-h2 R05:
// click "Full detail", reload at once). The write is debounced by 400 ms, a
// reload never unmounts React, so without a flush on `pagehide` the PUT is
// never sent — and on the next load the server's older value overrides the
// newer one the browser kept. Both halves are covered here.

const LS_PREFIX = "crewship.pref."
let fetchMock: ReturnType<typeof vi.fn>
let storage: Map<string, string>

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const puts = () => fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT")

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  storage = new Map<string, string>()
  vi.spyOn(window.localStorage, "getItem").mockImplementation((k) => storage.get(k) ?? null)
  vi.spyOn(window.localStorage, "setItem").mockImplementation((k, v) => {
    storage.set(k, String(v))
  })
  vi.spyOn(window.localStorage, "removeItem").mockImplementation((k) => {
    storage.delete(k)
  })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("useUserPreference across a reload", () => {
  it("sends a pending change with keepalive when the page is hidden, before the debounce", async () => {
    fetchMock.mockResolvedValue(okJSON({}))
    const { result } = renderHook(() => useUserPreference("routines.detailDepth", "simple"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    act(() => result.current[1]("full"))
    expect(puts()).toHaveLength(0)
    act(() => {
      window.dispatchEvent(new Event("pagehide"))
    })
    expect(puts()).toHaveLength(1)
    const [url, init] = puts()[0]
    expect(String(url)).toContain("/api/v1/me/preferences/routines.detailDepth")
    expect(init).toMatchObject({ method: "PUT", keepalive: true, body: JSON.stringify("full") })
  })

  it("keeps a local change the server never confirmed, and sends it again, instead of taking the older server value", async () => {
    // The state a reload leaves behind when the PUT did not get out: the
    // browser holds "full" and remembers it is not confirmed yet.
    storage.set(`${LS_PREFIX}routines.detailDepth`, JSON.stringify("full"))
    storage.set(`${LS_PREFIX}routines.detailDepth.unsynced`, "1")
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
      init?.method === "PUT" ? okJSON(null) : okJSON({ "routines.detailDepth": "simple" }),
    )
    const { result } = renderHook(() => useUserPreference("routines.detailDepth", "simple"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    expect(result.current[0]).toBe("full")
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0][1]).toMatchObject({ method: "PUT", body: JSON.stringify("full") })
    await waitFor(() => expect(storage.has(`${LS_PREFIX}routines.detailDepth.unsynced`)).toBe(false))
  })

  it("a confirmed write clears the marker, so the server wins again on the next load", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fetchMock.mockResolvedValue(okJSON({}))
    const { result } = renderHook(() => useUserPreference("routines.detailDepth", "simple"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    act(() => result.current[1]("full"))
    expect(storage.get(`${LS_PREFIX}routines.detailDepth.unsynced`)).toBe("1")
    await act(async () => {
      vi.advanceTimersByTime(450)
    })
    await waitFor(() => expect(storage.has(`${LS_PREFIX}routines.detailDepth.unsynced`)).toBe(false))
  })
})
