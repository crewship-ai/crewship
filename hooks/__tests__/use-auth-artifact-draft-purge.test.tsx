import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const purge = vi.hoisted(() => vi.fn())
vi.mock("@/lib/artifact-drafts", () => ({ purgeOtherArtifactDrafts: purge }))
vi.mock("@/lib/document-queue", () => ({ purgeOtherDocumentDrafts: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_EVENT, AUTH_CHANNEL } from "@/lib/api-fetch"

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>
const realPolicy = { ...sessionRetryPolicy }
let mockFetch: ReturnType<typeof vi.fn>
beforeEach(() => { purge.mockReset().mockResolvedValue(undefined); mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch); sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2 })
afterEach(() => { vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

it("keeps only the signed-in user's artifact drafts when the session settles (account switch)", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  expect(purge).toHaveBeenCalledWith("u1")
})

it("deletes every artifact draft before sign-out completes", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  purge.mockClear()
  let finish!: () => void
  purge.mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
  mockFetch.mockResolvedValueOnce(okJSON({}))
  let signedOut = false
  const pending = act(async () => { await result.current.signOut(); signedOut = true })
  await waitFor(() => expect(purge).toHaveBeenCalledWith(null))
  expect(signedOut).toBe(false)
  finish(); await pending
  expect(signedOut).toBe(true)
})

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string } }) => void) | null = null
  constructor(readonly name: string) { FakeBroadcastChannel.instances.push(this) }
}

it.each([
  ["an expired session", () => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) }],
  ["a sign-out in another tab", () => { FakeBroadcastChannel.instances.find(c => c.name === AUTH_CHANNEL)!.onmessage?.({ data: { type: "signout" } }) }],
])("deletes every artifact draft before leaving for the login page after %s", async (_name, trigger) => {
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))
    purge.mockClear()
    let finish!: () => void
    purge.mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
    act(trigger)
    await waitFor(() => expect(purge).toHaveBeenCalledWith(null))
    expect(replace).not.toHaveBeenCalled()
    finish()
    await waitFor(() => expect(replace).toHaveBeenCalledTimes(1))
  } finally { replace.mockRestore() }
})
