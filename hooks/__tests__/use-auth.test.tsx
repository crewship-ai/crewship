import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderHook, act, waitFor } from "@testing-library/react"

import { AuthProvider, useAuth, useSession } from "@/hooks/use-auth"

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, body: unknown): Response {
  return {
    ok: false,
    status,
    json: async () => body,
  } as unknown as Response
}

function errNoJSON(status: number): Response {
  return {
    ok: false,
    status,
    json: async () => {
      throw new Error("not json")
    },
  } as unknown as Response
}

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <AuthProvider>{children}</AuthProvider>
)

describe("useAuth / AuthProvider", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("throws when useAuth is called outside a provider", () => {
    // Vitest catches the render-time throw and surfaces it via result.error.
    expect(() => renderHook(() => useAuth())).toThrow(/AuthProvider/)
  })

  it("starts 'loading' and transitions to 'authenticated' on a valid session", async () => {
    mockFetch.mockResolvedValueOnce(
      okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-05-01" }),
    )

    const { result } = renderHook(() => useAuth(), { wrapper })
    // Starts loading.
    expect(result.current.status).toBe("loading")

    await waitFor(() => expect(result.current.status).toBe("authenticated"))
    expect(result.current.session?.user.id).toBe("u1")
  })

  it("becomes 'unauthenticated' on non-OK session response", async () => {
    mockFetch.mockResolvedValueOnce(errNoJSON(401))

    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("unauthenticated"))
    expect(result.current.session).toBeNull()
  })

  it("rejects a session payload that fails Zod validation", async () => {
    // Missing required `expires` field — schema should reject.
    mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1" } }))

    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("unauthenticated"))
    expect(result.current.session).toBeNull()
  })

  it("treats a network error on /session as unauthenticated", async () => {
    mockFetch.mockRejectedValueOnce(new Error("conn refused"))

    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("unauthenticated"))
  })

  describe("signIn", () => {
    async function setupAuth() {
      // Initial /session call — unauthenticated to start from a clean slate.
      mockFetch.mockResolvedValueOnce(errNoJSON(401))
      const hook = renderHook(() => useAuth(), { wrapper })
      await waitFor(() => expect(hook.result.current.status).toBe("unauthenticated"))
      return hook
    }

    it("fails when the CSRF endpoint is unreachable", async () => {
      const hook = await setupAuth()
      mockFetch.mockRejectedValueOnce(new Error("csrf down"))

      const res = await act(() => hook.result.current.signIn("a@b", "secret"))
      expect(res).toEqual({ ok: false, error: "Failed to get CSRF token" })
    })

    it("fails when CSRF payload is malformed", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ nope: true }))

      const res = await act(() => hook.result.current.signIn("a@b", "x"))
      expect(res.ok).toBe(false)
      expect(res.error).toBe("Failed to get CSRF token")
    })

    it("maps CredentialsSignin to a human message", async () => {
      const hook = await setupAuth()
      // CSRF ok, credentials callback returns a CredentialsSignin error.
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockResolvedValueOnce(okJSON({ error: "CredentialsSignin" }))

      const res = await act(() => hook.result.current.signIn("a@b", "wrong"))
      expect(res).toEqual({ ok: false, error: "Invalid email or password" })
    })

    it("passes through other server-side error codes", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockResolvedValueOnce(okJSON({ error: "RateLimited" }))

      const res = await act(() => hook.result.current.signIn("a@b", "x"))
      expect(res).toEqual({ ok: false, error: "RateLimited" })
    })

    it("returns error from body on non-OK credentials response", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockResolvedValueOnce(errJSON(500, { error: "server boom" }))

      const res = await act(() => hook.result.current.signIn("a@b", "x"))
      expect(res).toEqual({ ok: false, error: "server boom" })
    })

    it("defaults the error message when the body is not JSON", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockResolvedValueOnce(errNoJSON(500))

      const res = await act(() => hook.result.current.signIn("a@b", "x"))
      expect(res).toEqual({ ok: false, error: "Login failed" })
    })

    it("returns 'Network error' when the request rejects", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockRejectedValueOnce(new Error("timeout"))

      const res = await act(() => hook.result.current.signIn("a@b", "x"))
      expect(res).toEqual({ ok: false, error: "Network error" })
    })

    it("refreshes the session on success and flips status to authenticated", async () => {
      const hook = await setupAuth()
      mockFetch.mockResolvedValueOnce(okJSON({ csrfToken: "t" }))
      mockFetch.mockResolvedValueOnce(okJSON({})) // credentials endpoint — no error
      // refresh() fires another /session call.
      mockFetch.mockResolvedValueOnce(
        okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-05-01" }),
      )

      const res = await act(() => hook.result.current.signIn("a@b", "ok"))
      expect(res).toEqual({ ok: true })

      await waitFor(() => expect(hook.result.current.status).toBe("authenticated"))
      expect(hook.result.current.session?.user.id).toBe("u1")
    })
  })

  describe("signOut", () => {
    it("POSTs to /signout and clears session state", async () => {
      // Start authenticated.
      mockFetch.mockResolvedValueOnce(
        okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-05-01" }),
      )
      const { result } = renderHook(() => useAuth(), { wrapper })
      await waitFor(() => expect(result.current.status).toBe("authenticated"))

      // signOut: fetch returns 200.
      mockFetch.mockResolvedValueOnce(okJSON({}))
      await act(() => result.current.signOut())

      const [url, init] = mockFetch.mock.calls[1] as [string, RequestInit]
      expect(url).toBe("/api/auth/signout")
      expect(init.method).toBe("POST")
      expect(result.current.status).toBe("unauthenticated")
      expect(result.current.session).toBeNull()
    })

    it("preserves session state when sign-out fetch rejects (network error)", async () => {
      // PR #233 CodeRabbit-driven change: signOut now gates the local
      // reset on a server acknowledgement. On network error the catch
      // arm in hooks/use-auth.tsx returns early so a transient outage
      // doesn't desync this tab from a still-active server session.
      mockFetch.mockResolvedValueOnce(
        okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-05-01" }),
      )
      const { result } = renderHook(() => useAuth(), { wrapper })
      await waitFor(() => expect(result.current.status).toBe("authenticated"))

      mockFetch.mockRejectedValueOnce(new Error("offline"))
      await act(() => result.current.signOut())

      // Local state intentionally untouched — see comment above.
      expect(result.current.status).toBe("authenticated")
      expect(result.current.session?.user.id).toBe("u1")
    })
  })

  describe("useSession", () => {
    it("exposes {data, status} matching the auth context", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-05-01" }),
      )
      const { result } = renderHook(() => useSession(), { wrapper })

      await waitFor(() => expect(result.current.status).toBe("authenticated"))
      expect(result.current.data?.user.id).toBe("u1")
    })
  })
})
