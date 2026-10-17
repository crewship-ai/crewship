import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderHook, act, waitFor } from "@testing-library/react"

import { AuthProvider, useAuth } from "@/hooks/use-auth"
import { AUTH_EVENT } from "@/lib/api-fetch"

// On the static export an unknown path is answered with the dashboard page,
// whose first requests answer 401 for a visitor without a session. That 401
// fires the session-expired event; it must not send the visitor to
// /login?reason=expired — the dashboard layout shows the not-found state.

const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>
const noSession = () => ({ ok: true, status: 200, json: async () => ({}) }) as unknown as Response

describe("session-expired redirect on a path the app has no page for", () => {
  let replaceSpy: ReturnType<typeof vi.spyOn>
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn(async () => noSession()))
    vi.stubGlobal("BroadcastChannel", undefined)
    replaceSpy = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  })
  afterEach(() => {
    window.history.replaceState({}, "", "/")
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("stays on an unknown path", async () => {
    window.history.replaceState({}, "", "/does-not-exist")
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("unauthenticated"))
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "auth_failed" } })) })
    expect(replaceSpy).not.toHaveBeenCalled()
  })

  it("still redirects with the path on a real page", async () => {
    window.history.replaceState({}, "", "/issues/CRE-12")
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("unauthenticated"))
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "auth_failed" } })) })
    expect(replaceSpy).toHaveBeenCalledTimes(1)
    expect(replaceSpy.mock.calls[0][0]).toBe(`/login?reason=expired&redirect=${encodeURIComponent("/issues/CRE-12")}`)
  })
})
