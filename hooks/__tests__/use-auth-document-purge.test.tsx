import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const purge = vi.hoisted(() => vi.fn())
const drafts = vi.hoisted(() => ({ capture: vi.fn(), remove: vi.fn(), record: vi.fn(), sweep: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: purge, captureDocumentDrafts: drafts.capture, removeCapturedDocumentDrafts: drafts.remove }))
vi.mock("@/lib/document-draft-sign-out", () => ({ recordSignOut: drafts.record, sweepSignOuts: drafts.sweep }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_EVENT } from "@/lib/api-fetch"

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>
const realPolicy = { ...sessionRetryPolicy }
let mockFetch: ReturnType<typeof vi.fn>
beforeEach(() => { drafts.capture.mockReset().mockResolvedValue([]); drafts.record.mockReset().mockResolvedValue(undefined); drafts.sweep.mockReset().mockResolvedValue(undefined); drafts.remove.mockReset().mockResolvedValue(undefined); purge.mockReset().mockResolvedValue(undefined); mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch); sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2 })
afterEach(() => { vi.unstubAllGlobals(); Object.assign(sessionRetryPolicy, realPolicy) })

it("admits the signed-in user's Document journal without deleting other identities", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  expect(purge).toHaveBeenCalledWith("u1")
})

it("drains Document writers on sign-out before releasing the session", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  purge.mockClear()
  mockFetch.mockResolvedValueOnce(okJSON({}))
  await act(() => result.current.signOut())
  expect(purge).toHaveBeenCalledWith(null)
})

it("records the confirmed chains after the server acknowledged and before any cleanup", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  const key = ["owner", JSON.stringify([1, "native-v1", "native:row"])]
  drafts.capture.mockResolvedValue([{ key, revision: "r2", root: "r1" }, { key: ["owner", "other"], revision: "r9" }])
  purge.mockClear(); mockFetch.mockClear(); mockFetch.mockResolvedValueOnce(okJSON({}))
  vi.stubGlobal("confirm", () => true)
  await act(() => result.current.signOut())
  expect(drafts.record).toHaveBeenCalledWith("u1", ["r1", "r9"])
  const signOut = mockFetch.mock.calls.findIndex(([url]) => String(url).includes("/api/auth/signout"))
  expect(mockFetch.mock.invocationCallOrder[signOut]).toBeLessThan(drafts.record.mock.invocationCallOrder[0])
  expect(drafts.record.mock.invocationCallOrder[0]).toBeLessThan(drafts.remove.mock.invocationCallOrder[0])
  expect(drafts.sweep).toHaveBeenCalledWith(null)
})

it("a sign-out the server did not acknowledge records nothing", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  drafts.capture.mockResolvedValue([{ key: ["owner", "row"], revision: "r1" }])
  mockFetch.mockResolvedValueOnce({ ok: false, status: 500, json: async () => ({}) } as unknown as Response)
  vi.stubGlobal("confirm", () => true)
  await act(() => result.current.signOut())
  expect(drafts.record).not.toHaveBeenCalled(); expect(drafts.remove).not.toHaveBeenCalled()
})

it("a provider mount sweeps without a server, and signing in clears that person's intent", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  expect(drafts.sweep).toHaveBeenCalledWith(null)
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  expect(drafts.sweep).toHaveBeenCalledWith("u1")
})

it("a session-expired message caused by the sign-out itself does not cancel the erase", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  const row = { key: ["owner", "row"], revision: "typed" }
  drafts.capture.mockResolvedValue([row])
  vi.stubGlobal("confirm", () => true)
  // Another tab sees its session end as soon as the server has revoked it.
  mockFetch.mockImplementationOnce(async () => { window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason: "expired" } })); return okJSON({}) })
  await act(() => result.current.signOut())
  expect(drafts.remove.mock.calls.flatMap(([rows]) => rows)).toContainEqual(row)
})
