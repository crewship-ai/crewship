import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { apiFetch, tryRefresh, AUTH_EVENT, _resetRefreshInflightForTesting } from "../api-fetch"

// MockResponse builds the fetch Response shape with just enough surface
// for apiFetch — we don't pull in `whatwg-fetch` to keep the test light.
function mockResponse(body: unknown, init: { status?: number } = {}): Response {
  const status = init.status ?? 200
  // Provide the minimal shape: ok, status, json(), clone().
  const text = typeof body === "string" ? body : JSON.stringify(body ?? {})
  const cloneBase = (): Response => ({
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    headers: new Headers(),
    redirected: false,
    type: "basic",
    url: "",
    body: null,
    bodyUsed: false,
    clone: cloneBase,
    arrayBuffer: () => Promise.resolve(new ArrayBuffer(0)),
    blob: () => Promise.resolve(new Blob()),
    formData: () => Promise.resolve(new FormData()),
    text: () => Promise.resolve(text),
    json: () => Promise.resolve(JSON.parse(text || "null")),
    bytes: () => Promise.resolve(new Uint8Array()),
  } as unknown as Response)
  return cloneBase()
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  _resetRefreshInflightForTesting()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  _resetRefreshInflightForTesting()
})

describe("apiFetch", () => {
  it("passes through 200 responses untouched", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ x: 1 }))
    const res = await apiFetch("/api/v1/things")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: "include" })
  })

  it("on 401 session_expired tries refresh once and retries", async () => {
    fetchMock
      // initial 401 with refreshable reason
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
      // refresh 200
      .mockResolvedValueOnce(mockResponse({ ok: true }, { status: 200 }))
      // retried original 200
      .mockResolvedValueOnce(mockResponse({ x: 2 }, { status: 200 }))

    const res = await apiFetch("/api/v1/things")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/token/refresh")
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: "POST" })
  })

  it("on session_revoked emits session-expired and does NOT call refresh", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ error: "session_revoked" }, { status: 401 }))
    const handler = vi.fn()
    window.addEventListener(AUTH_EVENT, handler)

    const res = await apiFetch("/api/v1/things")
    expect(res.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(1) // no refresh call
    expect(handler).toHaveBeenCalledTimes(1)
    expect((handler.mock.calls[0][0] as CustomEvent).detail.reason).toBe("session_revoked")

    window.removeEventListener(AUTH_EVENT, handler)
  })

  it("on session_invalid emits session-expired and does NOT call refresh", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ error: "session_invalid" }, { status: 401 }))
    const handler = vi.fn()
    window.addEventListener(AUTH_EVENT, handler)
    await apiFetch("/api/v1/things")
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledTimes(1)
    window.removeEventListener(AUTH_EVENT, handler)
  })

  it("when refresh itself fails, emits session-expired and returns the original 401", async () => {
    fetchMock
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 })) // refresh 401

    const handler = vi.fn()
    window.addEventListener(AUTH_EVENT, handler)
    const res = await apiFetch("/api/v1/things")
    expect(res.status).toBe(401)
    expect(handler).toHaveBeenCalledTimes(1)
    window.removeEventListener(AUTH_EVENT, handler)
  })

  it("concurrent 401s share a single refresh attempt", async () => {
    let pendingRefresh: ((value: Response) => void) | null = null
    const refreshSeen = new Promise<void>((seen) => {
      fetchMock
        // Two original requests, both 401.
        .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
        .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
        // ONE refresh — controlled so we can verify both calls await it.
        .mockImplementationOnce(
          () =>
            new Promise<Response>((resolve) => {
              pendingRefresh = resolve
              seen()
            }),
        )
        // Two retries.
        .mockResolvedValueOnce(mockResponse({ ok: true }))
        .mockResolvedValueOnce(mockResponse({ ok: true }))
    })

    const a = apiFetch("/api/v1/x")
    const b = apiFetch("/api/v1/y")
    await refreshSeen
    pendingRefresh!(mockResponse({ ok: true }))

    const [resA, resB] = await Promise.all([a, b])
    expect(resA.status).toBe(200)
    expect(resB.status).toBe(200)

    // 2 originals + 1 (ONE!) refresh + 2 retries = 5
    expect(fetchMock).toHaveBeenCalledTimes(5)
    const refreshCalls = fetchMock.mock.calls.filter((c) => c[0] === "/api/auth/token/refresh")
    expect(refreshCalls.length).toBe(1)
  })

  it("body is replayable for typical bodies (string)", async () => {
    fetchMock
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
      .mockResolvedValueOnce(mockResponse({ ok: true }))
      .mockResolvedValueOnce(mockResponse({ done: true }))

    const res = await apiFetch("/api/v1/x", { method: "POST", body: "abc" })
    expect(res.status).toBe(200)
  })

  it("includes credentials by default", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({}))
    await apiFetch("/api/v1/x")
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: "include" })
  })

  it("strips skipRefresh from outbound init", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({}))
    await apiFetch("/api/v1/x", { skipRefresh: true })
    const passedInit = fetchMock.mock.calls[0][1] as RequestInit & { skipRefresh?: boolean }
    expect("skipRefresh" in passedInit).toBe(false)
  })

  it("skipRefresh prevents refresh attempt on 401", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
    const res = await apiFetch("/api/v1/x", { skipRefresh: true })
    expect(res.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(1) // no refresh call
  })
})

describe("tryRefresh", () => {
  it("returns true on 200", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true }))
    const ok = await tryRefresh()
    expect(ok).toBe(true)
  })

  it("returns false on non-2xx", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({}, { status: 401 }))
    const ok = await tryRefresh()
    expect(ok).toBe(false)
  })

  it("returns false on network error", async () => {
    fetchMock.mockRejectedValueOnce(new Error("network down"))
    const ok = await tryRefresh()
    expect(ok).toBe(false)
  })

  it("dedupes concurrent calls into one network round-trip", async () => {
    let resolveCall: ((value: Response) => void) | null = null
    fetchMock.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          resolveCall = resolve
        }),
    )
    const a = tryRefresh()
    const b = tryRefresh()
    resolveCall!(mockResponse({ ok: true }))
    const [ra, rb] = await Promise.all([a, b])
    expect(ra).toBe(true)
    expect(rb).toBe(true)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
