import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, render, waitFor } from "@testing-library/react"

const q = vi.hoisted(() => ({ suspend: vi.fn(), sweep: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: q.suspend, captureDocumentDrafts: vi.fn(async () => []), removeCapturedDocumentDrafts: vi.fn(async () => {}) }))
vi.mock("@/lib/document-draft-sign-out", () => ({ recordSignOut: vi.fn(async () => {}), sweepSignOuts: q.sweep, clearSignOut: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL, AUTH_EVENT } from "@/lib/api-fetch"

const realPolicy = { ...sessionRetryPolicy }
const user = (id: string) => ({ ok: true, status: 200, json: async () => ({ user: { id, name: "A", email: "a@b" }, expires: "2099-01-01" }) }) as unknown as Response
const signedOut = () => ({ ok: false, status: 401, json: async () => ({}) }) as unknown as Response
class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: unknown }) => void) | null = null
  constructor(readonly name: string) { FakeBroadcastChannel.instances.push(this) }
}
const channel = () => FakeBroadcastChannel.instances.find(c => c.name === AUTH_CHANNEL)!
let auth!: ReturnType<typeof useAuth>, mockFetch: ReturnType<typeof vi.fn>, replace: ReturnType<typeof vi.spyOn>
function Probe() { auth = useAuth(); return null }
async function signedIn() {
  mockFetch.mockResolvedValueOnce(user("ana"))
  render(<AuthProvider><Probe /></AuthProvider>)
  await waitFor(() => expect(auth.session?.user.id).toBe("ana"))
  q.suspend.mockClear(); q.sweep.mockClear()
}
beforeEach(() => {
  q.suspend.mockReset().mockResolvedValue(undefined); q.sweep.mockReset().mockResolvedValue(undefined)
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch)
  sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2
  replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
})
afterEach(() => { replace.mockRestore(); vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

const expectSweptBetweenSuspensionAndLeaving = () => {
  expect(q.sweep).toHaveBeenCalled()
  expect(q.suspend.mock.invocationCallOrder[0]).toBeLessThan(q.sweep.mock.invocationCallOrder[0])
  expect(q.sweep.mock.invocationCallOrder[0]).toBeLessThan(replace.mock.invocationCallOrder[0])
}

it("a tab told of a sign-out sweeps after suspending and before leaving", async () => {
  await signedIn()
  mockFetch.mockResolvedValueOnce(signedOut())
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana" } }) })
  await waitFor(() => expect(replace).toHaveBeenCalled())
  expectSweptBetweenSuspensionAndLeaving()
})

it("a tab leaving on the ended session sweeps too, with no message at all", async () => {
  await signedIn()
  act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_revoked" } })) })
  await waitFor(() => expect(replace).toHaveBeenCalled())
  expectSweptBetweenSuspensionAndLeaving()
})

it("a failed sweep does not keep the person on the page", async () => {
  await signedIn()
  q.sweep.mockRejectedValue(new Error("storage"))
  act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_revoked" } })) })
  await waitFor(() => expect(replace).toHaveBeenCalled())
})
