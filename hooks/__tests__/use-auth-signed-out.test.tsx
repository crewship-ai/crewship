import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { renderHook, act, waitFor } from "@testing-library/react"

import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_EVENT, AUTH_CHANNEL } from "@/lib/api-fetch"
import { noteUnsentDraft, readSignedOut } from "@/lib/signed-out"

// #3787 (wireframe round 64, Spojení §4): the auth provider tells the login
// page WHICH signed-out screen to show — expired, revoked elsewhere, or a
// plain Log out — and remembers who was signed in and whether a chat draft
// is waiting. A cold load that never reaches the backend is not "signed out":
// it stays loading, says when it tries next, and keeps trying.

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

const SESSION = { user: { id: "u1", name: "Ada", email: "ada@example.test" }, expires: "2099-12-01" }
const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  name: string
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string } }) => void) | null = null
  constructor(name: string) { this.name = name; FakeBroadcastChannel.instances.push(this) }
}

describe("AuthProvider → signed-out screens", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let replaceSpy: ReturnType<typeof vi.spyOn>
  const realPolicy = { ...sessionRetryPolicy }

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    FakeBroadcastChannel.instances = []
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
    replaceSpy = vi.spyOn(window.location, "replace").mockImplementation(() => {})
    sessionStorage.clear()
    // vitest.setup.ts stubs localStorage with no-op mocks; give it a memory.
    const store = new Map<string, string>()
    vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
    vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, String(v)) })
    sessionRetryPolicy.baseMs = 1
    sessionRetryPolicy.capMs = 2
    sessionRetryPolicy.maxAttempts = 3
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    Object.assign(sessionRetryPolicy, realPolicy)
    noteUnsentDraft("", undefined, "")
  })

  async function signedIn() {
    mockFetch.mockResolvedValueOnce(okJSON(SESSION))
    const hook = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(hook.result.current.status).toBe("authenticated"))
    return hook
  }
  const target = () => new URL(replaceSpy.mock.calls[0][0] as string, "http://localhost")

  it("an expired session goes to reason=expired with the way back, and remembers who was signed in", async () => {
    await signedIn()
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
    expect(target().pathname).toBe("/login")
    expect(target().searchParams.get("reason")).toBe("expired")
    expect(target().searchParams.get("redirect")).toBe("/")
    expect(readSignedOut()).toEqual({ email: "ada@example.test", draftAgent: null })
  })

  it("a session revoked elsewhere goes to reason=revoked", async () => {
    await signedIn()
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_revoked" } })) })
    expect(target().searchParams.get("reason")).toBe("revoked")
  })

  it("names the agent of an unsent chat draft that is still on this device", async () => {
    await signedIn()
    const key = JSON.stringify(["user", "u1", "s-1"])
    localStorage.setItem("crewship-composer", JSON.stringify({ state: { modelId: null, drafts: { [key]: "Rebuild both images tonight" } }, version: 0 }))
    noteUnsentDraft(key, "Riley", "Rebuild both images tonight")
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
    expect(readSignedOut()).toEqual({ email: "ada@example.test", draftAgent: "Riley" })
    // A draft sent or cleared since is not claimed to be kept.
    localStorage.setItem("crewship-composer", JSON.stringify({ state: { modelId: null, drafts: {} }, version: 0 }))
    expect(readSignedOut().draftAgent).toBeNull()
  })

  it("a visitor who never had a session goes to plain sign-in", async () => {
    await signedIn()
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "no_credentials" } })) })
    expect(target().searchParams.get("reason")).toBeNull()
  })

  it("a Log out in another tab shows the signed-out screen here too", async () => {
    await signedIn()
    const channel = FakeBroadcastChannel.instances.find((c) => c.name === AUTH_CHANNEL)
    act(() => { channel!.onmessage?.({ data: { type: "signout" } }) })
    expect(replaceSpy.mock.calls[0][0]).toBe("/login?reason=signed-out")
  })

  it("a cold load that never reaches the backend stays loading, says when it tries next, and recovers", async () => {
    vi.useFakeTimers({ now: 1_000_000 })
    try {
      mockFetch.mockRejectedValue(new Error("conn refused"))
      const { result } = renderHook(() => useAuth(), { wrapper })
      await act(async () => { await vi.advanceTimersByTimeAsync(50) })
      expect(result.current.status).toBe("loading")
      const retryAt = result.current.unreachable?.retryAt ?? 0
      expect(retryAt).toBeGreaterThan(Date.now())
      expect(retryAt).toBeLessThanOrEqual(Date.now() + sessionRetryPolicy.unreachableRetryMs)
      // Not signed out: nothing sent the person to the login page.
      expect(replaceSpy).not.toHaveBeenCalled()
      const calls = mockFetch.mock.calls.length
      await act(async () => { await vi.advanceTimersByTimeAsync(sessionRetryPolicy.unreachableRetryMs) })
      expect(mockFetch.mock.calls.length).toBe(calls + 1)
      // Try now: at once, and it recovers.
      mockFetch.mockReset()
      mockFetch.mockResolvedValue(okJSON(SESSION))
      await act(async () => { result.current.retryNow?.(); await vi.advanceTimersByTimeAsync(1) })
      expect(result.current.status).toBe("authenticated")
      expect(result.current.unreachable).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
