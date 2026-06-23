import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderHook, act, waitFor } from "@testing-library/react"

import { AuthProvider, useAuth } from "@/hooks/use-auth"
import { AUTH_EVENT, AUTH_CHANNEL } from "@/lib/api-fetch"

// Coverage companion for use-auth.test.tsx — covers the session-expired
// redirect listener (goLoginExpired / goLoginSignedOutElsewhere), the
// BroadcastChannel fan-in, and the signOut "server did not acknowledge"
// guard.

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function errJSON(status: number, body: unknown = {}): Response {
  return {
    ok: false,
    status,
    json: async () => body,
  } as unknown as Response
}

const SESSION = { user: { id: "u1", name: "A", email: "a@b" }, expires: "2026-12-01" }

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <AuthProvider>{children}</AuthProvider>
)

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  name: string
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string } }) => void) | null = null
  constructor(name: string) {
    this.name = name
    FakeBroadcastChannel.instances.push(this)
  }
}

describe("AuthProvider session-expired redirect", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let replaceSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    FakeBroadcastChannel.instances = []
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
    replaceSpy = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("redirects to /login?reason=expired&redirect=<path> on the auth event", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    act(() => {
      window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_revoked" } }))
    })

    expect(replaceSpy).toHaveBeenCalledTimes(1)
    const target = replaceSpy.mock.calls[0][0] as string
    expect(target).toContain("/login?")
    expect(target).toContain("reason=expired")
    // happy-dom default location is http://localhost:3000/ — the current
    // path rides along so post-login can return the user.
    expect(target).toContain(`redirect=${encodeURIComponent("/")}`)
  })

  it("redirects at most once even when the event fires repeatedly", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    act(() => {
      window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "x" } }))
      window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "x" } }))
      window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "x" } }))
    })

    expect(replaceSpy).toHaveBeenCalledTimes(1)
  })

  it("handles a cross-tab 'session-expired' broadcast like the local event", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    expect(channel).toBeDefined()
    act(() => {
      channel!.onmessage?.({ data: { type: "session-expired" } })
    })

    expect(replaceSpy).toHaveBeenCalledTimes(1)
    expect(replaceSpy.mock.calls[0][0]).toContain("reason=expired")
  })

  it("handles a cross-tab 'signout' broadcast with a clean /login redirect (no expired toast)", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    act(() => {
      channel!.onmessage?.({ data: { type: "signout" } })
    })

    expect(replaceSpy).toHaveBeenCalledTimes(1)
    expect(replaceSpy.mock.calls[0][0]).toBe("/login")
  })

  it("ignores unknown broadcast message types", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    act(() => {
      channel!.onmessage?.({ data: { type: "something-else" } })
      channel!.onmessage?.({ data: undefined })
    })
    expect(replaceSpy).not.toHaveBeenCalled()
  })

  it("closes the channel on unmount", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result, unmount } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    unmount()
    expect(channel!.close).toHaveBeenCalled()
  })

  it("still redirects via the local event when BroadcastChannel construction throws", async () => {
    vi.stubGlobal(
      "BroadcastChannel",
      class {
        constructor() {
          throw new Error("denied")
        }
      },
    )
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    act(() => {
      window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "x" } }))
    })
    expect(replaceSpy).toHaveBeenCalledTimes(1)
  })
})

describe("signOut server-acknowledgement guard", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    FakeBroadcastChannel.instances = []
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
    vi.spyOn(window.location, "replace").mockImplementation(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("keeps the session when the server answers a non-401 error (e.g. 500)", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    mockFetch.mockResolvedValueOnce(errJSON(500))
    await act(() => result.current.signOut())

    // Server did not acknowledge — local state must stay intact so the
    // tab doesn't desync from a still-active server session.
    expect(result.current.status).toBe("authenticated")
    expect(result.current.session?.user.id).toBe("u1")
  })

  it("treats a 401 from /signout as an effective sign-out (stale tab logout)", async () => {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))

    mockFetch.mockResolvedValueOnce(errJSON(401))
    await act(() => result.current.signOut())

    expect(result.current.status).toBe("unauthenticated")
    expect(result.current.session).toBeNull()
  })
})
