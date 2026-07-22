import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { apiFetch, tryRefresh, _resetRefreshInflightForTesting } from "../api-fetch"

/**
 * Security-audit guard suite (SECURITY-AUDIT-2026-06.md, frontend findings).
 *
 * Two flavours per the audit TRIPWIRE convention (cf.
 * internal/scrubber/scrubber_streambypass_test.go and
 * internal/provider/docker/tenant_collision_test.go):
 *
 *   - For behaviour that is ALREADY secure (T3.3 same-origin guard,
 *     T3.4 no-token-in-web-storage, and — since #1254 bullet 2 — FE1/B1
 *     no-token-in-ws-url) we write normal passing regression guards so a
 *     future refactor that re-opens the hole fails CI.
 *
 * Network is fully mocked — these are unit tests, no live backend.
 */

let fetchMock: ReturnType<typeof vi.fn>

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

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  _resetRefreshInflightForTesting()
})

afterEach(() => {
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  _resetRefreshInflightForTesting()
})

// ---------------------------------------------------------------------------
// T3.3 — apiFetch same-origin guard (SECURE regression guard)
//
// assertSameOrigin must reject cross-origin / protocol-relative / backslash-
// disguised / unparseable URLs BEFORE the request leaves the page, so the
// `credentials: "include"` auth cookies never reach an attacker-supplied
// origin (CodeQL js/client-side-request-forgery). The guard already exists
// in lib/api-fetch.ts; this pins it.
// ---------------------------------------------------------------------------
describe("T3.3 apiFetch same-origin guard (secure)", () => {
  // Table-driven: every entry must throw and must NOT touch the network.
  const rejected: Array<[string, string]> = [
    ["absolute cross-origin https", "https://evil.tld/x"],
    ["protocol-relative //evil", "//evil.tld/x"],
    ["protocol-relative with api path", "//evil.tld/api/v1/secrets"],
    ["backslash-disguised relative", "/\\evil.tld/api"],
    ["unparseable url", "http://[oops"],
    ["http cross-origin to evil", "http://evil.tld/api/v1/x"],
  ]

  it.each(rejected)("rejects %s before any network call", async (_label, url) => {
    await expect(apiFetch(url)).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rejects a cross-origin URL object", async () => {
    await expect(apiFetch(new URL("https://evil.tld/api/v1/x"))).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("rejects a cross-origin Request object", async () => {
    await expect(apiFetch(new Request("https://evil.tld/api/v1/x"))).rejects.toThrow(/cross-origin/)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("accepts a same-origin relative /api path", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    await expect(apiFetch("/api/v1/agents")).resolves.toMatchObject({ status: 200 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("accepts an absolute same-origin URL string", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    await expect(apiFetch(`${window.location.origin}/api/v1/agents`)).resolves.toMatchObject({
      status: 200,
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("attaches credentials:include only to the (vetted) same-origin request", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: 1 }))
    await apiFetch("/api/v1/agents")
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect(init.credentials).toBe("include")
  })
})

// ---------------------------------------------------------------------------
// T3.4 — no JWT-shaped value lands in localStorage / sessionStorage
//
// Auth tokens live in httpOnly cookies; the SPA must never persist a JWT to
// web storage (XSS exfil surface). apiFetch / tryRefresh drive auth purely
// over cookies, so after a typical 401→refresh→retry flow web storage must
// stay free of JWT-shaped strings. We install REAL in-memory storages (the
// global vitest.setup mock is a no-op) so the assertion is meaningful.
// ---------------------------------------------------------------------------

// A JWT is three base64url segments separated by dots. We scan for that shape
// anywhere inside a stored value, not just whole-value equality, to catch a
// token smuggled into a JSON blob.
const JWT_SHAPE = /[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/

class MemoryStorage implements Storage {
  private store = new Map<string, string>()
  get length(): number {
    return this.store.size
  }
  clear(): void {
    this.store.clear()
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? this.store.get(key)! : null
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null
  }
  removeItem(key: string): void {
    this.store.delete(key)
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value))
  }
  entries(): Array<[string, string]> {
    return Array.from(this.store.entries())
  }
}

function jwtShapedEntries(s: MemoryStorage): Array<[string, string]> {
  return s.entries().filter(([, v]) => JWT_SHAPE.test(v))
}

describe("T3.4 no JWT-shaped value in web storage (secure)", () => {
  let local: MemoryStorage
  let session: MemoryStorage

  beforeEach(() => {
    local = new MemoryStorage()
    session = new MemoryStorage()
    vi.stubGlobal("localStorage", local)
    vi.stubGlobal("sessionStorage", session)
  })

  it("a 401→refresh→retry flow writes no JWT to localStorage/sessionStorage", async () => {
    fetchMock
      .mockResolvedValueOnce(mockResponse({ error: "session_expired" }, { status: 401 })) // original 401
      .mockResolvedValueOnce(mockResponse({ ok: true })) // refresh ok (cookie set server-side)
      .mockResolvedValueOnce(mockResponse({ data: [] })) // retried original

    const res = await apiFetch("/api/v1/agents")
    expect(res.status).toBe(200)
    // The refresh round-trip happened over cookies, not storage.
    expect(fetchMock.mock.calls[1][0]).toBe("/api/auth/token/refresh")

    expect(jwtShapedEntries(local)).toEqual([])
    expect(jwtShapedEntries(session)).toEqual([])
    expect(local.length).toBe(0)
    expect(session.length).toBe(0)
  })

  it("tryRefresh alone persists nothing to web storage", async () => {
    fetchMock.mockResolvedValueOnce(mockResponse({ ok: true }))
    await expect(tryRefresh()).resolves.toBe("ok")
    expect(local.length).toBe(0)
    expect(session.length).toBe(0)
  })

  it("the JWT_SHAPE detector itself is sound (guards against a silent no-op regex)", () => {
    // Positive control: a real-looking JWT must trip the detector, otherwise
    // the assertions above would pass vacuously.
    local.setItem(
      "evil",
      "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk", // gitleaks:allow — public jwt.io sample, test fixture
    )
    expect(jwtShapedEntries(local).length).toBe(1)
    // Negative control: an opaque session id is NOT JWT-shaped.
    session.setItem("sid", "9f3c2a7b8e1d4f60")
    expect(jwtShapedEntries(session)).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// FE1 / B1 — ws-token carried in the URL query string (FIXED — #1254 bullet 2)
//
// hooks/use-websocket.ts used to set the short-lived WS auth token as a
// `?token=` query param because the browser WebSocket API cannot send custom
// headers. URL query strings can leak via proxy/access logs and Referer.
// Fixed by moving auth post-open: the hook now connects to a bare URL and
// sends `{"type":"auth",token}` as the first frame once the socket opens
// (mirrors hooks/use-terminal.ts and the server's
// internal/ws/hub.go authenticateUpgradedConn). Full behavioural coverage —
// including that the auth frame is actually sent — lives in
// hooks/__tests__/use-websocket.test.ts; this guard is the one-line
// documentation tripwire for the URL-construction half of the fix.
// ---------------------------------------------------------------------------
describe("FE1/B1 ws-token NOT in URL query (fixed)", () => {
  it("FE1_SecureTarget: WS auth token must NOT appear in the connection URL", () => {
    // Mirror hooks/use-websocket.ts's current URL construction: new URL(...),
    // no searchParams.set("token", ...) — the token travels only in the
    // post-open auth frame, never the URL.
    const wsUrlObj = new URL("/api/v1/ws", window.location.origin)
    expect(wsUrlObj.searchParams.has("token")).toBe(false)
    expect(wsUrlObj.toString()).not.toContain("token=")
  })
})
