import { describe, it, expect, vi, beforeEach, afterEach, beforeAll } from "vitest"
import {
  apiFetch,
  tryRefresh,
  broadcastSignOut,
  broadcastSessionExpired,
  AUTH_EVENT,
  AUTH_CHANNEL,
  _resetRefreshInflightForTesting,
} from "../api-fetch"

// Coverage companion for api-fetch.test.ts. That file owns the 401 →
// refresh state machine; this one covers the same-origin guard
// (assertSameOrigin), the non-replayable-body bail-out after a successful
// refresh, the malformed-401-body peekReason path, and the cross-tab
// BroadcastChannel emitters.

// Fake BroadcastChannel — installed before the module lazily caches its
// channel so we can assert on postMessage payloads deterministically.
class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  name: string
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: MessageEvent) => void) | null = null
  constructor(name: string) {
    this.name = name
    FakeBroadcastChannel.instances.push(this)
  }
}

beforeAll(() => {
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
})

function mockResponse(body: unknown, init: { status?: number } = {}): Response {
  const status = init.status ?? 200
  const text = typeof body === "string" ? body : JSON.stringify(body ?? {})
  const make = (): Response =>
    ({
      ok: status >= 200 && status < 300,
      status,
      headers: new Headers(),
      clone: make,
      text: () => Promise.resolve(text),
      json: () => Promise.resolve(JSON.parse(text || "null")),
    }) as unknown as Response
  return make()
}

// A 401 whose body is NOT JSON — peekReason must swallow the parse error
// and return null (reason unknown → refresh path).
function mock401NonJSON(): Response {
  const make = (): Response =>
    ({
      ok: false,
      status: 401,
      headers: new Headers(),
      clone: make,
      text: () => Promise.resolve("<html>nope</html>"),
      json: () => Promise.reject(new SyntaxError("Unexpected token <")),
    }) as unknown as Response
  return make()
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  _resetRefreshInflightForTesting()
})

afterEach(() => {
  vi.clearAllMocks()
  _resetRefreshInflightForTesting()
})

describe("assertSameOrigin guard", () => {
  it("rejects absolute cross-origin string URLs without fetching", async () => {
    await expect(apiFetch("https://evil.test/api/v1/x")).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rejects protocol-relative URLs (//evil.test/…)", async () => {
    await expect(apiFetch("//evil.test/api/v1/x")).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rejects backslash-disguised relative URLs (/\\evil.test)", async () => {
    await expect(apiFetch("/\\evil.test/api")).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rejects unparseable URLs", async () => {
    await expect(apiFetch("http://[oops")).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("accepts an absolute same-origin string URL", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    const res = await apiFetch(`${window.location.origin}/api/v1/x`)
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("accepts a same-origin URL object and rejects a cross-origin one", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    const good = new URL("/api/v1/x", window.location.origin)
    await expect(apiFetch(good)).resolves.toMatchObject({ status: 200 })

    const bad = new URL("https://evil.test/api/v1/x")
    await expect(apiFetch(bad)).rejects.toThrow(/cross-origin/)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("accepts a same-origin Request object and rejects a cross-origin one", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    const good = new Request(`${window.location.origin}/api/v1/x`)
    await expect(apiFetch(good)).resolves.toMatchObject({ status: 200 })

    const bad = new Request("https://evil.test/api/v1/x")
    await expect(apiFetch(bad)).rejects.toThrow(/cross-origin/)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe("non-replayable body after successful refresh", () => {
  it("returns the original 401 instead of replaying a consumed stream", async () => {
    fetchMock
      // Original request 401s with a refreshable reason.
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 }))
      // Refresh succeeds…
      .mockResolvedValueOnce(mockResponse({ ok: true }))

    const stream = new ReadableStream({
      start(c) {
        c.close()
      },
    })
    const res = await apiFetch("/api/v1/upload", {
      method: "POST",
      body: stream,
    } as RequestInit)

    // …but the body can't be re-sent, so the caller gets the original
    // 401 back and NO third fetch happens.
    expect(res.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/token/refresh")
  })
})

describe("peekReason with non-JSON 401 body", () => {
  it("treats an unparseable body as unknown reason and goes through refresh", async () => {
    fetchMock
      .mockResolvedValueOnce(mock401NonJSON())
      .mockResolvedValueOnce(mockResponse({ ok: true })) // refresh ok
      .mockResolvedValueOnce(mockResponse({ x: 1 })) // retried original

    const res = await apiFetch("/api/v1/x")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/token/refresh")
  })

  it("falls back to 'session_expired' reason when refresh auth-fails on a reasonless 401", async () => {
    fetchMock
      .mockResolvedValueOnce(mock401NonJSON())
      .mockResolvedValueOnce(mockResponse({}, { status: 401 })) // refresh 401

    const handler = vi.fn()
    window.addEventListener(AUTH_EVENT, handler)
    const res = await apiFetch("/api/v1/x")
    expect(res.status).toBe(401)
    expect(handler).toHaveBeenCalledTimes(1)
    expect((handler.mock.calls[0][0] as CustomEvent).detail.reason).toBe("session_expired")
    window.removeEventListener(AUTH_EVENT, handler)
  })
})

describe("peekReason with JSON body but non-string error", () => {
  it("treats {error: <number>} as unknown reason and goes through refresh", async () => {
    fetchMock
      .mockResolvedValueOnce(mockResponse({ error: 42 }, { status: 401 }))
      .mockResolvedValueOnce(mockResponse({ ok: true })) // refresh ok
      .mockResolvedValueOnce(mockResponse({ x: 1 })) // retried original

    const res = await apiFetch("/api/v1/x")
    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/token/refresh")
  })
})

describe("tryRefresh timeout", () => {
  it("aborts a hung refresh after 10s and reports retryable_failed", async () => {
    vi.useFakeTimers()
    try {
      // A refresh that never answers but honors its AbortSignal — like a
      // half-open TCP connection behind a buffering proxy.
      fetchMock.mockImplementationOnce(
        (_url: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () =>
              reject(new DOMException("The operation was aborted.", "AbortError")),
            )
          }),
      )

      const pending = tryRefresh()
      await vi.advanceTimersByTimeAsync(9_999)
      // Not yet — the timeout is exactly 10s.
      await vi.advanceTimersByTimeAsync(1)
      await expect(pending).resolves.toBe("retryable_failed")
    } finally {
      vi.useRealTimers()
    }
  })
})

describe("SSR (no window) behavior — fresh module", () => {
  it("skips the origin check and the session-expired emit when window is undefined", async () => {
    vi.resetModules()
    const mod = await import("../api-fetch")
    const realWindow = globalThis.window
    vi.stubGlobal("window", undefined)
    try {
      // Cross-origin URL allowed server-side (no cookie context to protect).
      fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
      const res = await mod.apiFetch("https://internal-service.test/api/v1/x")
      expect(res.status).toBe(200)
      // emitSessionExpired no-ops without a window — must not throw.
      expect(() => mod.broadcastSessionExpired("session_expired")).not.toThrow()
    } finally {
      vi.stubGlobal("window", realWindow)
    }
  })
})

describe("BroadcastChannel degradation — fresh module", () => {
  it("missing BroadcastChannel global → local event still fires, postMessage skipped", async () => {
    vi.resetModules()
    vi.stubGlobal("BroadcastChannel", undefined)
    const mod = await import("../api-fetch")

    const handler = vi.fn()
    window.addEventListener(mod.AUTH_EVENT, handler)
    expect(() => mod.broadcastSessionExpired("session_revoked")).not.toThrow()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(() => mod.broadcastSignOut()).not.toThrow()
    window.removeEventListener(mod.AUTH_EVENT, handler)

    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  })

  it("throwing BroadcastChannel constructor → channel null, emitters still safe", async () => {
    vi.resetModules()
    vi.stubGlobal(
      "BroadcastChannel",
      class {
        constructor() {
          throw new Error("denied by browser policy")
        }
      },
    )
    const mod = await import("../api-fetch")

    expect(() => mod.broadcastSignOut()).not.toThrow()
    // Second call exercises the cached-null path.
    expect(() => mod.broadcastSignOut()).not.toThrow()

    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  })
})

describe("cross-tab broadcast emitters", () => {
  it("broadcastSignOut posts {type: 'signout'} on the auth channel", () => {
    broadcastSignOut()
    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    expect(channel).toBeDefined()
    expect(channel!.postMessage).toHaveBeenCalledWith({ type: "signout" })
  })

  it("broadcastSessionExpired dispatches the local event AND posts cross-tab", () => {
    const handler = vi.fn()
    window.addEventListener(AUTH_EVENT, handler)

    broadcastSessionExpired("ws_close_4401")

    expect(handler).toHaveBeenCalledTimes(1)
    expect((handler.mock.calls[0][0] as CustomEvent).detail.reason).toBe("ws_close_4401")
    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    expect(channel!.postMessage).toHaveBeenCalledWith({
      type: "session-expired",
      reason: "ws_close_4401",
    })
    window.removeEventListener(AUTH_EVENT, handler)
  })

  it("reuses one cached channel across emits", () => {
    broadcastSignOut()
    broadcastSignOut()
    const channels = FakeBroadcastChannel.instances.filter((c) => c.name === AUTH_CHANNEL)
    expect(channels.length).toBe(1)
  })
})
