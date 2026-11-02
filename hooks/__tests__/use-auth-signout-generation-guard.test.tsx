import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, render, waitFor } from "@testing-library/react"

vi.mock("@/lib/artifact-drafts", () => ({ suspendOtherArtifactDrafts: vi.fn(async () => {}), finishArtifactDraftIdentity: vi.fn(), captureArtifactDrafts: vi.fn(async () => []), removeCapturedArtifactDrafts: vi.fn(async () => {}), captureLegacyLocalDrafts: vi.fn(() => []), removeCapturedLegacyLocalDrafts: vi.fn() }))
const suspendDocuments = vi.hoisted(() => vi.fn(async (_human: string | null) => {}))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: suspendDocuments, captureDocumentDrafts: vi.fn(async () => []), removeCapturedDocumentDrafts: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL, AUTH_EVENT } from "@/lib/api-fetch"
import { SIGNED_OUT_URL } from "@/lib/signed-out"

// The sign-out listeners are guarded by the refresh generation, not a sticky
// flag: any newer refresh or settle re-arms them (#4714).

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const signedOut = () => ({ ok: false, status: 401, json: async () => ({}) }) as unknown as Response
const user = (id: string) => okJSON({ user: { id, name: id, email: `${id}@b` }, expires: "2099-01-01" })
const realPolicy = { ...sessionRetryPolicy }
let mockFetch: ReturnType<typeof vi.fn>
let auth!: ReturnType<typeof useAuth>

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string; human?: string } }) => void) | null = null
  constructor(readonly name: string) { FakeBroadcastChannel.instances.push(this) }
}
const channel = () => FakeBroadcastChannel.instances.find(c => c.name === AUTH_CHANNEL)!

function Probe() { auth = useAuth(); return null }

beforeEach(() => {
  suspendDocuments.mockClear()
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch)
  sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2
})
afterEach(() => { vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

async function liveTab() {
  mockFetch.mockResolvedValueOnce(user("ana"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("ana"))
}
const signOutElsewhere = () => act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana" } }) })
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r }); return { promise, resolve } }

it("follows a sign-out after a failed draft drain was followed by a refresh", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await liveTab()
    // The drain for the sign-out fails: the tab stays on the recovery overlay.
    suspendDocuments.mockImplementationOnce(async () => { throw new Error("drain failed") })
    signOutElsewhere()
    await waitFor(() => expect(suspendDocuments).toHaveBeenCalledWith(null))
    // A still-mounted component refreshes and the live session settles again.
    mockFetch.mockResolvedValueOnce(user("ana"))
    await act(async () => { await auth.refresh() })
    expect(auth.session?.user.id).toBe("ana")
    expect(replace).not.toHaveBeenCalled()
    // A real sign-out elsewhere must now be followed.
    mockFetch.mockResolvedValueOnce(signedOut())
    signOutElsewhere()
    await waitFor(() => expect(replace).toHaveBeenCalledWith(SIGNED_OUT_URL))
  } finally { replace.mockRestore() }
})

it("follows a second sign-out after a refresh superseded the first one during its drain", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await liveTab()
    const drain = deferred()
    suspendDocuments.mockImplementationOnce(async () => { await drain.promise })
    signOutElsewhere()
    await waitFor(() => expect(suspendDocuments).toHaveBeenCalledWith(null))
    mockFetch.mockResolvedValueOnce(user("ana"))
    const refreshed = auth.refresh()
    await act(async () => { drain.resolve(); await refreshed })
    expect(replace).not.toHaveBeenCalled()
    mockFetch.mockResolvedValueOnce(signedOut())
    signOutElsewhere()
    await waitFor(() => expect(replace).toHaveBeenCalledWith(SIGNED_OUT_URL))
    expect(replace).toHaveBeenCalledTimes(1)
  } finally { replace.mockRestore() }
})

it("follows an expiry that arrives during the re-settle drain", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await liveTab()
    // A delayed sign-out message finds the session live; hold its re-settle drain.
    const drain = deferred()
    mockFetch.mockResolvedValueOnce(user("ana"))
    suspendDocuments.mockImplementation(async (human: string | null) => { if (human === "ana") await drain.promise })
    signOutElsewhere()
    await waitFor(() => expect(suspendDocuments.mock.calls.filter(c => c[0] === "ana")).toHaveLength(2))
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
    await act(async () => { drain.resolve() })
    await waitFor(() => expect(replace).toHaveBeenCalledTimes(1))
    expect(String(replace.mock.calls[0][0])).toMatch(/^\/login\?/)
  } finally { replace.mockRestore(); suspendDocuments.mockImplementation(async () => {}) }
})

it("starts one leave per generation: a second sign-out during the drain is not a second drain", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await liveTab()
    const drain = deferred()
    suspendDocuments.mockImplementationOnce(async () => { await drain.promise })
    signOutElsewhere()
    signOutElsewhere()
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
    expect(suspendDocuments.mock.calls.filter(c => c[0] === null)).toHaveLength(1)
    mockFetch.mockResolvedValueOnce(signedOut())
    await act(async () => { drain.resolve() })
    await waitFor(() => expect(replace).toHaveBeenCalledWith(SIGNED_OUT_URL))
    expect(replace).toHaveBeenCalledTimes(1)
    expect(suspendDocuments.mock.calls.filter(c => c[0] === null)).toHaveLength(1)
  } finally { replace.mockRestore() }
})

it("an online event cannot settle a live session under a sign-out whose drain failed", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    // Cold load against an unreachable backend: the provider arms its probe.
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"))
    render(<AuthProvider><Probe /></AuthProvider>)
    await waitFor(() => expect(auth.unreachable).toBeTruthy())
    const calls = mockFetch.mock.calls.length
    // A sign-out elsewhere whose drain fails: the tab stays on the recovery overlay.
    suspendDocuments.mockImplementationOnce(async () => { throw new Error("drain failed") })
    signOutElsewhere()
    await waitFor(() => expect(suspendDocuments).toHaveBeenCalledWith(null))
    // The network returns; the backend would now answer with a live session.
    mockFetch.mockReset(); mockFetch.mockResolvedValue(user("ana"))
    await act(async () => { window.dispatchEvent(new Event("online")); await Promise.resolve() })
    expect(mockFetch).not.toHaveBeenCalled()
    expect(calls).toBeGreaterThan(0)
    expect(auth.session).toBeNull()
  } finally { replace.mockRestore() }
})
