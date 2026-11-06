import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, render, waitFor } from "@testing-library/react"

const q = vi.hoisted(() => ({ suspend: vi.fn(), capture: vi.fn(), remove: vi.fn(), successors: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: q.suspend, captureDocumentDrafts: q.capture, removeCapturedDocumentDrafts: q.remove, suspendedDocumentDraftRevisions: q.successors, documentSuspensionSettled: async () => { await q.suspend.mock.results.at(-1)?.value } }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL, AUTH_EVENT } from "@/lib/api-fetch"

const realPolicy = { ...sessionRetryPolicy }
const user = (id: string) => ({ ok: true, status: 200, json: async () => ({ user: { id, name: "A", email: "a@b" }, expires: "2099-01-01" }) }) as unknown as Response
const signedOut = () => ({ ok: false, status: 401, json: async () => ({}) }) as unknown as Response
const key: [string, string] = [JSON.stringify(["ana", "w", "p"]), JSON.stringify([1, "native-v1", "native:row"])]
const confirmed = { key, revision: "r1" }, successor = { key, revision: "r2" }
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
  q.suspend.mockClear()
}
beforeEach(() => {
  for (const fn of Object.values(q)) fn.mockReset()
  q.suspend.mockResolvedValue(undefined); q.capture.mockResolvedValue([]); q.remove.mockResolvedValue(undefined); q.successors.mockReturnValue([])
  FakeBroadcastChannel.instances = []
  vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch)
  sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2
  replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
})
afterEach(() => { replace.mockRestore(); vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

it("names the confirmed revisions in the sign-out broadcast", async () => {
  await signedIn()
  q.capture.mockResolvedValue([confirmed])
  vi.stubGlobal("confirm", () => true)
  mockFetch.mockResolvedValue(user("ana"))
  await act(() => auth.signOut())
  const sent = FakeBroadcastChannel.instances.flatMap(c => c.postMessage.mock.calls.map(([message]) => message)).find(message => message.type === "signout")
  expect(sent).toEqual({ type: "signout", human: "ana", documents: [confirmed] })
})

it("the holder tab erases its own suspension successor before it leaves", async () => {
  await signedIn()
  q.successors.mockImplementation(rows => rows.length ? [successor] : [])
  mockFetch.mockResolvedValueOnce(signedOut())
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana", documents: [confirmed] } }) })
  await waitFor(() => expect(replace).toHaveBeenCalled())
  expect(q.successors).toHaveBeenCalledWith([confirmed])
  expect(q.remove).toHaveBeenCalledWith([successor])
  expect(q.suspend.mock.invocationCallOrder[0]).toBeLessThan(q.remove.mock.invocationCallOrder[0])
  expect(q.remove.mock.invocationCallOrder[0]).toBeLessThan(replace.mock.invocationCallOrder[0])
})

it("a tab already leaving because the session ended still erases the successor", async () => {
  await signedIn()
  let suspended!: () => void
  q.suspend.mockReturnValue(new Promise<void>(resolve => { suspended = resolve }))
  q.successors.mockImplementation(rows => rows.length ? [successor] : [])
  act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_revoked" } })) })
  await waitFor(() => expect(q.suspend).toHaveBeenCalledWith(null))
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana", documents: [confirmed] } }) })
  suspended()
  await waitFor(() => expect(replace).toHaveBeenCalled())
  expect(q.remove).toHaveBeenCalledWith([successor])
  expect(q.remove.mock.invocationCallOrder[0]).toBeLessThan(replace.mock.invocationCallOrder[0])
})

it("a delayed message erases nothing while the person is still signed in", async () => {
  await signedIn()
  q.successors.mockImplementation(rows => rows.length ? [successor] : [])
  mockFetch.mockResolvedValueOnce(user("ana"))
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana", documents: [confirmed] } }) })
  await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(2))
  await waitFor(() => expect(auth.session?.user.id).toBe("ana"))
  expect(q.remove).not.toHaveBeenCalled(); expect(replace).not.toHaveBeenCalled()
})

it.each([
  ["another person's rows", [{ key: [JSON.stringify(["bob", "w", "p"]), key[1]], revision: "r1" }]],
  ["a malformed list", [{ key: "row" }]],
  ["a non-native row", [{ key: [key[0], JSON.stringify([1, "doc", "row"])], revision: "r1" }]],
])("ignores %s", async (_, documents) => {
  await signedIn()
  q.successors.mockImplementation(rows => rows.map((row: { key: unknown }) => ({ key: row.key, revision: "r2" })))
  mockFetch.mockResolvedValueOnce(signedOut())
  act(() => { channel().onmessage?.({ data: { type: "signout", human: "ana", documents } }) })
  await waitFor(() => expect(replace).toHaveBeenCalled())
  expect(q.remove).not.toHaveBeenCalled()
})
