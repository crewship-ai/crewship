import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock NextResponse and NextRequest for Edge middleware testing
const mockRedirect = vi.fn((url: URL) => ({
  type: "redirect",
  url: url.toString(),
}))
const mockNext = vi.fn(() => ({ type: "next" }))

vi.mock("next/server", () => ({
  NextResponse: {
    redirect: (url: URL) => mockRedirect(url),
    next: () => mockNext(),
  },
}))

// Import after mocks
import { middleware } from "@/middleware"

function createRequest(pathname: string, opts?: { hasCookie?: boolean; protocol?: string; forwardedProto?: string }) {
  const protocol = opts?.protocol ?? "http:"
  const base = `${protocol}//localhost:3001`
  const url = new URL(pathname, base)
  const cookies = new Map<string, { value: string }>()
  if (opts?.hasCookie) {
    const cookieName = protocol === "https:"
      ? "__Secure-authjs.session-token"
      : "authjs.session-token"
    cookies.set(cookieName, { value: "mock-session-token" })
  }
  const headerMap = new Map<string, string>()
  if (opts?.forwardedProto) {
    headerMap.set("x-forwarded-proto", opts.forwardedProto)
  }
  return {
    nextUrl: url,
    url: url.toString(),
    cookies: { get: (name: string) => cookies.get(name) },
    headers: { get: (name: string) => headerMap.get(name) ?? null },
  } as Parameters<typeof middleware>[0]
}

describe("middleware", () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  describe("public paths (no auth required)", () => {
    it.each([
      "/login",
      "/signup",
      "/api/auth/session",
      "/api/auth/csrf",
      "/api/auth/callback/credentials",
      "/api/auth/signin",
      "/api/v1/health",
      "/api/v1/webhooks",
      "/api/v1/webhooks/github",
      "/api/v1/internal/sessions",
      "/api/v1/internal/credentials",
    ])("allows %s without cookie", (path) => {
      const req = createRequest(path, { hasCookie: false })
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
      expect(mockRedirect).not.toHaveBeenCalled()
    })
  })

  describe("protected paths (auth required)", () => {
    it.each([
      "/",
      "/agents",
      "/agents/123/chat",
      "/teams",
      "/credentials",
      "/settings",
      "/api/v1/agents",
      "/api/v1/credentials",
      "/api/v1/ws-token",
    ])("redirects %s to /login without cookie", (path) => {
      const req = createRequest(path, { hasCookie: false })
      middleware(req)
      expect(mockRedirect).toHaveBeenCalledTimes(1)
      const redirectUrl = mockRedirect.mock.calls[0][0] as URL
      expect(redirectUrl.pathname).toBe("/login")
      expect(redirectUrl.searchParams.get("callbackUrl")).toBe(path)
      expect(mockNext).not.toHaveBeenCalled()
    })

    it.each([
      "/",
      "/agents",
      "/agents/123/chat",
      "/teams",
      "/credentials",
      "/api/v1/agents",
      "/api/v1/ws-token",
    ])("allows %s with valid cookie", (path) => {
      const req = createRequest(path, { hasCookie: true })
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
      expect(mockRedirect).not.toHaveBeenCalled()
    })
  })

  describe("cookie name by protocol", () => {
    it("uses authjs.session-token for http", () => {
      const req = createRequest("/agents", { hasCookie: true, protocol: "http:" })
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
    })

    it("uses __Secure-authjs.session-token for https", () => {
      const req = createRequest("/agents", { hasCookie: true, protocol: "https:" })
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
    })

    it("redirects https without __Secure- cookie", () => {
      // Create request with http cookie name but https protocol
      const url = new URL("/agents", "https://localhost:3001")
      const cookies = new Map<string, { value: string }>()
      cookies.set("authjs.session-token", { value: "token" }) // wrong cookie for https
      const req = {
        nextUrl: url,
        url: url.toString(),
        cookies: { get: (name: string) => cookies.get(name) },
        headers: { get: () => null },
      } as Parameters<typeof middleware>[0]

      middleware(req)
      expect(mockRedirect).toHaveBeenCalled()
    })
  })

  describe("callbackUrl preservation", () => {
    it("includes original path in redirect", () => {
      const req = createRequest("/agents/abc/chat", { hasCookie: false })
      middleware(req)
      const redirectUrl = mockRedirect.mock.calls[0][0] as URL
      expect(redirectUrl.searchParams.get("callbackUrl")).toBe("/agents/abc/chat")
    })

    it("preserves deep nested paths", () => {
      const req = createRequest("/teams/t1/agents/a1/settings", { hasCookie: false })
      middleware(req)
      const redirectUrl = mockRedirect.mock.calls[0][0] as URL
      expect(redirectUrl.searchParams.get("callbackUrl")).toBe("/teams/t1/agents/a1/settings")
    })

    it("preserves query parameters in callbackUrl", () => {
      const req = createRequest("/agents?tab=chat&org=123", { hasCookie: false })
      middleware(req)
      const redirectUrl = mockRedirect.mock.calls[0][0] as URL
      expect(redirectUrl.searchParams.get("callbackUrl")).toBe("/agents?tab=chat&org=123")
    })
  })

  describe("x-forwarded-proto handling", () => {
    it("handles comma-separated proto values", () => {
      const url = new URL("/agents", "http://localhost:3001")
      const cookies = new Map<string, { value: string }>()
      cookies.set("authjs.session-token", { value: "token" })
      const headerMap = new Map<string, string>()
      headerMap.set("x-forwarded-proto", "http, https")
      const req = {
        nextUrl: url,
        url: url.toString(),
        cookies: { get: (name: string) => cookies.get(name) },
        headers: { get: (name: string) => headerMap.get(name) ?? null },
      } as Parameters<typeof middleware>[0]
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
    })

    it("uses first proto when comma-separated with https first", () => {
      const url = new URL("/agents", "http://localhost:3001")
      const cookies = new Map<string, { value: string }>()
      cookies.set("__Secure-authjs.session-token", { value: "token" })
      const headerMap = new Map<string, string>()
      headerMap.set("x-forwarded-proto", "https, http")
      const req = {
        nextUrl: url,
        url: url.toString(),
        cookies: { get: (name: string) => cookies.get(name) },
        headers: { get: (name: string) => headerMap.get(name) ?? null },
      } as Parameters<typeof middleware>[0]
      middleware(req)
      expect(mockNext).toHaveBeenCalled()
    })
  })
})
