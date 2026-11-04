import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { tryRefresh, _resetRefreshInflightForTesting } from "../api-fetch"

// A refresh rotates the refresh token; the server treats a second use of the
// old token as a replay and revokes the session. On an exported build,
// "New artifact → Sheet → Create and edit" mounts the editor in the library,
// whose admission refreshes, and then opens /artifacts/<id> as a NEW document,
// which refreshes again. Without these guarantees the second document sent
// the rotated-away token and the person was signed out within ~2 s.

const MARK = "crewship:auth-refresh"

function ok(): Response {
  return { ok: true, status: 200, json: () => Promise.resolve({ ok: true }), clone() { return this } } as unknown as Response
}

let fetchMock: ReturnType<typeof vi.fn>
// vitest.setup.ts stubs localStorage with no-op mocks; back them with a Map here.
const store = new Map<string, string>()
function realStorage() {
  const ls = localStorage as unknown as Record<"getItem" | "setItem" | "removeItem" | "clear", ReturnType<typeof vi.fn>>
  ls.getItem.mockImplementation((k: string) => store.get(k) ?? null)
  ls.setItem.mockImplementation((k: string, v: string) => void store.set(k, String(v)))
  ls.removeItem.mockImplementation((k: string) => void store.delete(k))
  ls.clear.mockImplementation(() => store.clear())
}

beforeEach(() => {
  fetchMock = vi.fn().mockResolvedValue(ok())
  vi.stubGlobal("fetch", fetchMock)
  realStorage()
  localStorage.clear()
  _resetRefreshInflightForTesting()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  localStorage.clear()
  _resetRefreshInflightForTesting()
})

describe("token refresh across documents", () => {
  it("is keepalive, so a page replaced mid-refresh still stores the rotated cookies", async () => {
    await tryRefresh()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST", credentials: "include", keepalive: true })
  })

  it("waits for a refresh another document started instead of presenting the same token", async () => {
    vi.useFakeTimers()
    localStorage.setItem(MARK, JSON.stringify({ at: Date.now() - 200, realm: "previous-document" }))
    const result = tryRefresh()
    await vi.advanceTimersByTimeAsync(2_000)
    expect(fetchMock).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await expect(result).resolves.toBe("ok")
  })

  it("stops waiting as soon as the other document's refresh clears its mark", async () => {
    vi.useFakeTimers()
    localStorage.setItem(MARK, JSON.stringify({ at: Date.now(), realm: "other-tab" }))
    const result = tryRefresh()
    await vi.advanceTimersByTimeAsync(500)
    expect(fetchMock).not.toHaveBeenCalled()
    localStorage.removeItem(MARK)
    await vi.advanceTimersByTimeAsync(150)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await expect(result).resolves.toBe("ok")
  })

  it("does not wait for an old mark, and removes its own mark when done", async () => {
    localStorage.setItem(MARK, JSON.stringify({ at: Date.now() - 60_000, realm: "long-gone" }))
    const result = tryRefresh()
    // Sent at once, as before.
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(JSON.parse(localStorage.getItem(MARK)!).realm).not.toBe("long-gone")
    await expect(result).resolves.toBe("ok")
    expect(localStorage.getItem(MARK)).toBeNull()
  })
})
