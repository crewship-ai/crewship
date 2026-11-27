// Only a still-open Sheet whose final local write failed gets the automatic retry.
import React from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const artifact = vi.hoisted(() => vi.fn())
const documents = vi.hoisted(() => vi.fn())
vi.mock("@/lib/artifact-drafts", () => ({ suspendOtherArtifactDrafts: artifact, finishArtifactDraftIdentity: vi.fn(), captureArtifactDrafts: vi.fn(async () => []), removeCapturedArtifactDrafts: vi.fn(async () => {}), captureLegacyLocalDrafts: vi.fn(() => []), removeCapturedLegacyLocalDrafts: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: documents, captureDocumentDrafts: vi.fn(async () => []), removeCapturedDocumentDrafts: vi.fn(async () => {}) }))
vi.mock("@/lib/document-draft-sign-out", () => ({ recordSignOut: vi.fn(async () => {}), sweepSignOuts: vi.fn(async () => {}), clearSignOut: vi.fn(async () => {}) }))
import { AuthProvider, useAuth, sessionEndDraftRetry, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_CHANNEL, AUTH_EVENT } from "@/lib/api-fetch"
import { SIGNED_OUT_URL } from "@/lib/signed-out"
import { PendingSheetDraftCapture } from "@/lib/local-draft-seal"

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>
const realPolicy = { ...sessionRetryPolicy }
const realRetry = [...sessionEndDraftRetry.delaysMs]
const pendingCapture = () => Promise.reject(new PendingSheetDraftCapture("The Sheet still holds an unconfirmed draft"))
const overlay = () => document.querySelector('[role="alertdialog"]')
let replace: ReturnType<typeof vi.spyOn>
class FakeBroadcastChannel {
  static instances: FakeBroadcastChannel[] = []
  postMessage = vi.fn()
  close = vi.fn()
  onmessage: ((ev: { data?: { type?: string; human?: string } }) => void) | null = null
  constructor(readonly name: string) { FakeBroadcastChannel.instances.push(this) }
}

beforeEach(() => {
  artifact.mockReset().mockResolvedValue(undefined); documents.mockReset().mockResolvedValue(undefined)
  vi.stubGlobal("fetch", vi.fn(async () => okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" })))
  FakeBroadcastChannel.instances = []; vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel)
  replace = vi.spyOn(window.location, "replace").mockImplementation(() => {})
  sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2
  sessionEndDraftRetry.delaysMs = [1, 1, 1]
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); Object.assign(sessionRetryPolicy, realPolicy); sessionEndDraftRetry.delaysMs = realRetry })

async function expireAfter(breakIt: () => void) {
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  artifact.mockClear(); documents.mockClear()
  breakIt()
  vi.mocked(fetch).mockResolvedValue({ ok: false, status: 401, json: async () => ({}) } as unknown as Response)
  act(() => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "session_expired" } })) })
}

describe("a session that ends while a draft is still being written", () => {
  it("tries both stores again when the Sheet's final capture fails once", async () => {
    await expireAfter(() => documents.mockImplementationOnce(pendingCapture))
    await waitFor(() => expect(replace).toHaveBeenCalledTimes(1))
    expect(String(replace.mock.calls[0][0])).toContain("/login")
    expect(overlay()).toBeNull()
    expect(artifact).toHaveBeenCalledTimes(2); expect(documents).toHaveBeenCalledTimes(2)
    expect(artifact).toHaveBeenLastCalledWith(null); expect(documents).toHaveBeenLastCalledWith(null)
  })

  it("follows a sign-out elsewhere when the drafts miss their time once", async () => {
    const { result } = renderHook(() => useAuth(), { wrapper })
    await waitFor(() => expect(result.current.status).toBe("authenticated"))
    artifact.mockClear(); documents.mockClear()
    documents.mockImplementationOnce(pendingCapture)
    vi.mocked(fetch).mockResolvedValue({ ok: false, status: 401, json: async () => ({}) } as unknown as Response)
    act(() => { FakeBroadcastChannel.instances.find(c => c.name === AUTH_CHANNEL)!.onmessage?.({ data: { type: "signout", human: "u1" } }) })
    await waitFor(() => expect(replace).toHaveBeenCalledWith(SIGNED_OUT_URL))
    expect(overlay()).toBeNull()
    expect(artifact).toHaveBeenCalledTimes(2); expect(documents).toHaveBeenCalledTimes(2)
  })

  it("keeps the tab with Keep this tab open when the sweep fails every time, and leaves nothing", async () => {
    await expireAfter(() => documents.mockImplementation(pendingCapture))
    await waitFor(() => expect(overlay()).not.toBeNull())
    expect(documents).toHaveBeenCalledTimes(1 + sessionEndDraftRetry.delaysMs.length)
    expect(replace).not.toHaveBeenCalled()
  })

  it.each([
    ["the artifact store", artifact],
    ["a document without a pending Sheet capture", documents],
  ] as const)("offers recovery after the first failure from %s", async (_, store) => {
    await expireAfter(() => store.mockRejectedValueOnce(new Error("storage is unavailable")))
    await waitFor(() => expect(overlay()).not.toBeNull())
    expect(artifact).toHaveBeenCalledTimes(1)
    expect(documents).toHaveBeenCalledTimes(1)
    expect(replace).not.toHaveBeenCalled()
  })
})
