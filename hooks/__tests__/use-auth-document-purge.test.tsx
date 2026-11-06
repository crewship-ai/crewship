import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, renderHook, waitFor } from "@testing-library/react"

const purge = vi.hoisted(() => vi.fn())
const drafts = vi.hoisted(() => ({ capture: vi.fn(), remove: vi.fn(), suspended: vi.fn() }))
vi.mock("@/lib/document-queue", () => ({ suspendOtherDocumentDrafts: purge, captureDocumentDrafts: drafts.capture, removeCapturedDocumentDrafts: drafts.remove, suspendedDocumentDraftRevisions: drafts.suspended }))
import { AuthProvider, useAuth, sessionRetryPolicy } from "@/hooks/use-auth"
import { AUTH_EVENT } from "@/lib/api-fetch"

const okJSON = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>
const realPolicy = { ...sessionRetryPolicy }
let mockFetch: ReturnType<typeof vi.fn>
beforeEach(() => { drafts.capture.mockReset().mockResolvedValue([]); drafts.suspended.mockReset().mockReturnValue([]); drafts.remove.mockReset().mockResolvedValue(undefined); purge.mockReset().mockResolvedValue(undefined); mockFetch = vi.fn(); vi.stubGlobal("fetch", mockFetch); sessionRetryPolicy.baseMs = 1; sessionRetryPolicy.capMs = 2 })
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

it("a confirmed sign-out erases the revision an open editor stored while it was suspended", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  const row = (id: string) => JSON.stringify([1, "native-v1", id])
  const before = { key: ["owner", row("native:row")], revision: "typed" }, after = { key: ["owner", row("native:row")], revision: "suspended" }, arrived = { key: ["owner", row("native:new")], revision: "later" }
  drafts.capture.mockResolvedValue([before])
  purge.mockImplementation(async (human: string | null) => { if (human === null) { drafts.capture.mockResolvedValue([after, arrived]); drafts.suspended.mockImplementation((rows: unknown[]) => rows === undefined ? [] : [after]) } })
  mockFetch.mockResolvedValueOnce(okJSON({}))
  vi.stubGlobal("confirm", () => true)
  await act(() => result.current.signOut())
  const removed = drafts.remove.mock.calls.flatMap(([rows]) => rows)
  expect(removed).toContainEqual(after)
  expect(removed).not.toContainEqual(arrived)
})

it("a confirmed sign-out keeps a newer revision another tab wrote to a confirmed row", async () => {
  mockFetch.mockResolvedValueOnce(okJSON({ user: { id: "u1", name: "A", email: "a@b" }, expires: "2099-01-01" }))
  const { result } = renderHook(() => useAuth(), { wrapper })
  await waitFor(() => expect(result.current.status).toBe("authenticated"))
  const key = ["owner", JSON.stringify([1, "native-v1", "native:tab-b"])]
  const confirmed = { key, revision: "r1" }, newer = { key, revision: "r2" }
  drafts.capture.mockResolvedValue([confirmed])
  // Tab B wrote R2 while tab A's dialog was open; this tab's suspension wrote nothing.
  purge.mockImplementation(async (human: string | null) => { if (human === null) drafts.capture.mockResolvedValue([newer]) })
  mockFetch.mockResolvedValueOnce(okJSON({}))
  vi.stubGlobal("confirm", () => true)
  await act(() => result.current.signOut())
  const removed = drafts.remove.mock.calls.flatMap(([rows]) => rows)
  expect(removed).toContainEqual(confirmed)
  expect(removed).not.toContainEqual(newer)
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
