import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act, waitFor } from "@testing-library/react"

import { useUserPreference } from "@/hooks/use-user-preference"

// First test coverage for use-user-preference: localStorage-first read,
// server override on mount, debounced PUT on set(), and the
// unmount-flush keepalive write.

const LS_PREFIX = "crewship.pref."

let fetchMock: ReturnType<typeof vi.fn>
let storage: Map<string, string>

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  // The global vitest setup stubs localStorage with a null-returning mock;
  // swap in a Map-backed impl so reads observe writes (same pattern as
  // use-workspace.test.ts).
  storage = new Map<string, string>()
  vi.spyOn(window.localStorage, "getItem").mockImplementation((k) => storage.get(k) ?? null)
  vi.spyOn(window.localStorage, "setItem").mockImplementation((k, v) => {
    storage.set(k, String(v))
  })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("initial read", () => {
  it("returns the cached localStorage value synchronously on first render", () => {
    storage.set(`${LS_PREFIX}panel.height`, JSON.stringify(420))
    fetchMock.mockReturnValue(new Promise(() => {})) // server never answers

    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    expect(result.current[0]).toBe(420)
    expect(result.current[2].ready).toBe(false)
  })

  it("falls back to the default when localStorage has no entry", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    expect(result.current[0]).toBe(100)
  })

  it("falls back to the default when the cached value is corrupt JSON", () => {
    storage.set(`${LS_PREFIX}panel.height`, "{not json")
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    expect(result.current[0]).toBe(100)
  })
})

describe("server sync on mount", () => {
  it("overrides local state when the server has a different value and caches it", async () => {
    storage.set(`${LS_PREFIX}theme`, JSON.stringify("light"))
    fetchMock.mockResolvedValueOnce(okJSON({ theme: "dark" }))

    const { result } = renderHook(() => useUserPreference("theme", "system"))
    expect(result.current[0]).toBe("light") // instant cached read

    await waitFor(() => expect(result.current[2].ready).toBe(true))
    expect(result.current[0]).toBe("dark") // server wins on initial sync
    expect(storage.get(`${LS_PREFIX}theme`)).toBe(JSON.stringify("dark"))
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/me/preferences", { credentials: "include" })
  })

  it("keeps the local value when the server agrees (no churn)", async () => {
    storage.set(`${LS_PREFIX}theme`, JSON.stringify("dark"))
    fetchMock.mockResolvedValueOnce(okJSON({ theme: "dark" }))

    const { result } = renderHook(() => useUserPreference("theme", "system"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    expect(result.current[0]).toBe("dark")
  })

  it("keeps the default when the server has no entry for the key", async () => {
    fetchMock.mockResolvedValueOnce(okJSON({ otherKey: 1 }))
    const { result } = renderHook(() => useUserPreference("theme", "system"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    expect(result.current[0]).toBe("system")
  })

  it("still becomes ready when the preferences fetch rejects", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useUserPreference("theme", "system"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    expect(result.current[0]).toBe("system")
  })

  it("survives a localStorage quota error while applying the server value", async () => {
    ;(window.localStorage.setItem as ReturnType<typeof vi.fn>).mockImplementation(() => {
      throw new Error("QuotaExceededError")
    })
    fetchMock.mockResolvedValueOnce(okJSON({ theme: "dark" }))

    const { result } = renderHook(() => useUserPreference("theme", "system"))
    await waitFor(() => expect(result.current[2].ready).toBe(true))
    // State still updated even though the cache write failed.
    expect(result.current[0]).toBe("dark")
  })
})

describe("set() — local update + debounced PUT", () => {
  it("updates state and localStorage synchronously, PUTs after the 400ms tail", async () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))

    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length

    act(() => {
      result.current[1](250)
    })
    expect(result.current[0]).toBe(250)
    expect(storage.get(`${LS_PREFIX}panel.height`)).toBe("250")
    // Debounced — nothing on the wire yet.
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount)

    act(() => {
      vi.advanceTimersByTime(400)
    })
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)
    const [url, init] = fetchMock.mock.calls[callsAfterMount] as [string, RequestInit]
    expect(url).toBe("/api/v1/me/preferences/panel.height")
    expect(init).toMatchObject({
      method: "PUT",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: "250",
    })
  })

  it("coalesces rapid set() calls into one PUT carrying the last value", () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))

    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length

    act(() => {
      result.current[1](110)
      result.current[1](150)
      result.current[1](199)
    })
    act(() => {
      vi.advanceTimersByTime(400)
    })

    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)
    const [, init] = fetchMock.mock.calls[callsAfterMount] as [string, RequestInit]
    expect(init.body).toBe("199")
  })

  it("URL-encodes the preference key", () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))

    const { result } = renderHook(() => useUserPreference("weird key/slash", 1))
    const callsAfterMount = fetchMock.mock.calls.length
    act(() => {
      result.current[1](2)
    })
    act(() => {
      vi.advanceTimersByTime(400)
    })
    const [url] = fetchMock.mock.calls[callsAfterMount] as [string]
    expect(url).toBe("/api/v1/me/preferences/weird%20key%2Fslash")
  })

  it("swallows localStorage write failures and still schedules the PUT", () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))
    ;(window.localStorage.setItem as ReturnType<typeof vi.fn>).mockImplementation(() => {
      throw new Error("private browsing")
    })

    const { result } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length
    act(() => {
      result.current[1](300)
    })
    expect(result.current[0]).toBe(300)
    act(() => {
      vi.advanceTimersByTime(400)
    })
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)
  })
})

describe("unmount flush", () => {
  it("flushes a pending debounced write with keepalive on unmount", () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))

    const { result, unmount } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length

    act(() => {
      result.current[1](777)
    })
    unmount() // before the 400ms tail elapses

    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)
    const [url, init] = fetchMock.mock.calls[callsAfterMount] as [string, RequestInit]
    expect(url).toBe("/api/v1/me/preferences/panel.height")
    expect(init).toMatchObject({ method: "PUT", keepalive: true, body: "777" })

    // The cleared debounce timer must not fire a duplicate PUT.
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)
  })

  it("does NOT flush when the debounced PUT already went out", () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(okJSON({}))

    const { result, unmount } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length

    act(() => {
      result.current[1](555)
    })
    act(() => {
      vi.advanceTimersByTime(400) // debounce fires
    })
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1)

    unmount()
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount + 1) // no extra keepalive write
  })

  it("does not flush anything when set() was never called", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { unmount } = renderHook(() => useUserPreference("panel.height", 100))
    const callsAfterMount = fetchMock.mock.calls.length
    unmount()
    expect(fetchMock.mock.calls.length).toBe(callsAfterMount)
  })
})
