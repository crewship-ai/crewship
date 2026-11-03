import React, { useEffect } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, render, waitFor } from "@testing-library/react"

vi.mock("@/lib/artifact-drafts", () => ({ suspendOtherArtifactDrafts: vi.fn(async () => {}), finishArtifactDraftIdentity: vi.fn(), captureArtifactDrafts: vi.fn(async () => []), removeCapturedArtifactDrafts: vi.fn(async () => {}), captureLegacyLocalDrafts: vi.fn(() => []), removeCapturedLegacyLocalDrafts: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: vi.fn(async () => {}), captureDocumentDrafts: vi.fn(async () => []), removeCapturedDocumentDrafts: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL } from "@/lib/api-fetch"

// The draft-admission key remounts the tree so components holding one person's
// drafts are dropped when that person is replaced. The first settle and a
// sign-in from signed-out hold nobody's drafts: remounting there re-ran page
// effects (/bootstrap went to /login) and wiped input typed before the settle.

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const signedOut = () => ({ ok: false, status: 401, json: async () => ({}) }) as unknown as Response
const user = (id: string) => okJSON({ user: { id, name: id, email: `${id}@b` }, expires: "2099-01-01" })
const realPolicy = { ...sessionRetryPolicy }
let mockFetch: ReturnType<typeof vi.fn>
let mounts = 0
let auth!: ReturnType<typeof useAuth>

function Probe() {
  auth = useAuth()
  useEffect(() => { mounts++ }, [])
  return null
}

class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string; human?: string } }) => void) | null = null
  constructor(readonly name: string) { FakeBroadcastChannel.instances.push(this) }
}

beforeEach(() => { mounts = 0; mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch); sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2 })
afterEach(() => { vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

it.each([
  ["the first settle to signed-out", signedOut],
  ["the first settle to a user", () => user("first")],
])("keeps children mounted through %s", async (_name, response) => {
  mockFetch.mockResolvedValueOnce(response())
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.status).not.toBe("loading"))
  expect(mounts).toBe(1)
})

it("keeps children mounted when a signed-out page signs in (bootstrap → onboarding)", async () => {
  mockFetch.mockResolvedValueOnce(signedOut())
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.status).toBe("unauthenticated"))
  mockFetch.mockResolvedValueOnce(user("admin"))
  await act(async () => { await auth.refresh() })
  expect(auth.status).toBe("authenticated")
  expect(mounts).toBe(1)
})

it("remounts children when the settled person is replaced by another", async () => {
  mockFetch.mockResolvedValueOnce(user("alice"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("alice"))
  expect(mounts).toBe(1)
  mockFetch.mockResolvedValueOnce(user("bob"))
  await act(async () => { await auth.refresh() })
  expect(auth.session?.user.id).toBe("bob")
  expect(mounts).toBe(2)
})

it("remounts children when the settled person signs out", async () => {
  // Keep the sign-out broadcast from echoing into this provider.
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  mockFetch.mockResolvedValueOnce(user("carol"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("carol"))
  mockFetch.mockResolvedValueOnce(okJSON({}))
  let ok: boolean | void = false
  await act(async () => { ok = await auth.signOut() })
  expect(ok).toBe(true)
  expect(auth.status).toBe("unauthenticated")
  expect(mounts).toBe(2)
})

it("remounts children when a refresh finds the settled person signed out", async () => {
  mockFetch.mockResolvedValueOnce(user("dave"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("dave"))
  mockFetch.mockResolvedValueOnce(signedOut())
  await act(async () => { await auth.refresh() })
  expect(auth.status).toBe("unauthenticated")
  expect(mounts).toBe(2)
})

it("remounts children when the same person returns after a sign-out elsewhere (A -> null -> A)", async () => {
  // The draft layer retired this tab's writers for null; only a remount
  // brings the person's editors back.
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  mockFetch.mockResolvedValueOnce(user("erin"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("erin"))
  expect(mounts).toBe(1)
  mockFetch.mockResolvedValueOnce(user("erin"))
  act(() => { FakeBroadcastChannel.instances.find(c => c.name === AUTH_CHANNEL)!.onmessage?.({ data: { type: "signout", human: "erin" } }) })
  await waitFor(() => expect(mounts).toBe(2))
  expect(auth.session?.user.id).toBe("erin")
})

it("does not remount children on a routine refresh of the same person", async () => {
  mockFetch.mockResolvedValueOnce(user("frank"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("frank"))
  mockFetch.mockResolvedValueOnce(user("frank"))
  await act(async () => { await auth.refresh() })
  expect(mounts).toBe(1)
})
