import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, render, waitFor } from "@testing-library/react"

vi.mock("@/lib/artifact-drafts", () => ({ suspendOtherArtifactDrafts: vi.fn(async () => {}), finishArtifactDraftIdentity: vi.fn(), captureArtifactDrafts: vi.fn(async () => []), removeCapturedArtifactDrafts: vi.fn(async () => {}), captureLegacyLocalDrafts: vi.fn(() => []), removeCapturedLegacyLocalDrafts: vi.fn() }))
const suspendDocuments = vi.hoisted(() => vi.fn(async (_human: string | null) => {}))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: suspendDocuments, captureDocumentDrafts: vi.fn(async () => []), removeCapturedDocumentDrafts: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL, AUTH_EVENT } from "@/lib/api-fetch"
import { SIGNED_OUT_URL } from "@/lib/signed-out"

// A tab that re-settles into a live session after a sign-out elsewhere (a
// delayed message) must still follow a later sign-out or expiry (#4705).

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

async function resettledTab(replace: ReturnType<typeof vi.spyOn>) {
  mockFetch.mockResolvedValueOnce(user("ana"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("ana"))
  // A delayed sign-out message; the session is still live, so the tab stays.
  mockFetch.mockResolvedValueOnce(user("ana"))
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana" } }) })
  await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))
  // The re-settle has drained for ana again (initial settle + re-settle).
  await waitFor(() => expect(suspendDocuments.mock.calls.filter(c => c[0] === "ana")).toHaveLength(2))
  await waitFor(() => expect(auth.session?.user.id).toBe("ana"))
  expect(replace).not.toHaveBeenCalled()
}

it("follows a later sign-out elsewhere after re-settling", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await resettledTab(replace)
    mockFetch.mockResolvedValueOnce(signedOut())
    act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana" } }) })
    await waitFor(() => expect(replace).toHaveBeenCalledWith(SIGNED_OUT_URL))
  } finally { replace.mockRestore() }
})

it("follows a later session expiry after re-settling", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await resettledTab(replace)
    act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
    await waitFor(() => expect(replace).toHaveBeenCalledTimes(1))
    expect(String(replace.mock.calls[0][0])).toMatch(/^\/login\?/)
  } finally { replace.mockRestore() }
})

it("still ignores a sign-out for a different person after re-settling", async () => {
  const replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  try {
    await resettledTab(replace)
    act(() => { channel().onmessage?.({ data: { type: "signout", human: "someone-else" } }) })
    await new Promise(r => setTimeout(r, 50))
    expect(replace).not.toHaveBeenCalled()
    expect(auth.session?.user.id).toBe("ana")
  } finally { replace.mockRestore() }
})
