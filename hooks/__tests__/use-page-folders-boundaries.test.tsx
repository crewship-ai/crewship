import React, { type ReactNode } from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
const { api, events } = vi.hoisted(() => ({ api: vi.fn(), events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (name: string, handler: () => void) => events.set(name, handler) }))
import { useFolderOwnerChoices, usePageFolders, useFolderAcl, usePageAccessMe, usePageFolderMutations, useFolderAclMutations, normalizeFolder, normalizeFolderAcl, normalizeFolderConflict, normalizeFolderList, toPageFolderView, refusedPageOf, FolderFenceError } from "@/hooks/use-page-folders"
const clients: QueryClient[] = []
function setup() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
 clients.push(client)
 return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> }
}
beforeEach(() => { api.mockReset(); events.clear() })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.restoreAllMocks() })
function reply(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }) }

it("preserves fallback owner labels and rejects invalid dates and counts", () => {
 expect(toPageFolderView({ slug: "ops", owner: "crew/", created_at: "bad", updated_at: "2026-01-01T00:00:00Z", page_count: Infinity })).toMatchObject({ ownerLabel: "crew/", createdAt: null, updatedAt: new Date("2026-01-01T00:00:00Z"), pageCount: 0 })
 expect(toPageFolderView({ slug: "ops", owner: "legacy", page_count: 3.9 })).toMatchObject({ ownerLabel: "legacy", pageCount: 3 })
 expect(toPageFolderView({ slug: "ops", owner: null })).toMatchObject({ ownerLabel: null })
})
it.each([null, "bad", {}, { folder: null }])("does not invent a folder from %j", body => expect(normalizeFolder(body)).toBeNull())
it.each(["rows", "items", "data"])("accepts the %s list envelope", key => expect(normalizeFolderList({ [key]: [{ slug: "ops" }] })).toEqual([{ slug: "ops" }]))
it("treats unrecognized conflict metadata as absent", () => {
 expect(normalizeFolderConflict(null)).toEqual({ error: "The folder or the page changed; try again." })
 expect(normalizeFolderConflict({ conflict: "pages_version", pages_version: Infinity, acl_version: NaN, page: " ", acl: false })).toEqual({ error: "The folder or the page changed; try again.", conflict: "pages_version" })
 expect(refusedPageOf(new FolderFenceError({ error: "stale" }))).toBeNull()
 expect(normalizeFolderAcl(null)).toEqual({ entries: [], aclVersion: null })
 expect(normalizeFolderAcl([{ subject_type: "user", subject_id: "u1", can_write: "true" }, { subject_type: "unknown" }, { subject_type: "crew" }])).toEqual({ aclVersion: null, entries: [{ subjectType: "user", subjectId: "u1", label: "u1", canWrite: false, setAt: null, setBy: null }] })
})
it.each([
 [[{ slug: "z", name: "Zed" }, null, { name: "No identity" }, { slug: "a" }], ["a", "z"]],
 [{ data: [{ slug: "ops", id: "c1", name: "Operations" }] }, ["ops"]],
 [null, []],
 [{ data: "invalid" }, []],
])("reads usable folder owner choices from %j", async (body, slugs) => {
 api.mockResolvedValue(reply(body));const { wrapper } = setup()
 const { result } = renderHook(() => useFolderOwnerChoices("ws & one"), { wrapper })
 await waitFor(() => expect(result.current.isSuccess).toBe(true))
 expect(result.current.data?.map(c => c.slug)).toEqual(slugs)
 expect(api.mock.calls[0][0]).toBe("/api/v1/crews?workspace_id=ws+%26+one")
})
it.each([[null, "ops", true], ["ws", undefined, true], ["ws", "ops", false]])("keeps permission reads disabled for incomplete selection", async (workspace, slug, enabled) => {
 const { wrapper } = setup()
 const { result } = renderHook(() => ({ acl: useFolderAcl(workspace, slug, enabled), access: usePageAccessMe(workspace, slug, enabled) }), { wrapper })
 await act(async () => { if (!workspace || !slug) await result.current.acl.reread() })
 expect(api).not.toHaveBeenCalled();expect(result.current.acl.loading).toBe(false);expect(result.current.access.loading).toBe(false)
})
it("does not refresh folder data without a workspace", async () => {
 const { client, wrapper } = setup();const invalidate = vi.spyOn(client, "invalidateQueries")
 const { result } = renderHook(() => ({ folders: usePageFolders(undefined), choices: useFolderOwnerChoices(undefined) }), { wrapper })
 await act(async () => { result.current.folders.refresh();await result.current.folders.reread();events.get("page.folder.updated")!() })
 expect(api).not.toHaveBeenCalled();expect(invalidate).not.toHaveBeenCalled()
})
it("refreshes folder and page lists together and re-reads ACL on demand", async () => {
 api.mockImplementation(async (url: string) => reply(url.includes("/acl") ? { acl: [], acl_version: 2 } : { folders: [{ slug: "z" }, { slug: "a" }] }))
 const { client, wrapper } = setup();const refetch = vi.spyOn(client, "refetchQueries");const invalidate = vi.spyOn(client, "invalidateQueries")
 const { result } = renderHook(() => ({ folders: usePageFolders("ws"), acl: useFolderAcl("ws", "ops") }), { wrapper })
 await waitFor(() => expect(result.current.folders.loading || result.current.acl.loading).toBe(false))
 expect(result.current.folders.folders.map(f => f.slug)).toEqual(["a", "z"])
 await act(async () => { await result.current.folders.reread();await result.current.acl.reread();events.get("page.updated")!() })
 expect(refetch).toHaveBeenCalledWith({ queryKey: ["page-folders", "ws"] })
 expect(refetch).toHaveBeenCalledWith({ queryKey: ["pages", "ws"] })
 expect(invalidate).toHaveBeenCalledWith({ queryKey: ["pages", "ws"] })
})
it.each([null, { paths: "invalid" }])("does not invent access paths from %j", async body => {
 api.mockResolvedValue(reply(body));const { wrapper } = setup()
 const { result } = renderHook(() => usePageAccessMe("ws", "ops"), { wrapper })
 await waitFor(() => expect(result.current.loading).toBe(false));expect(result.current.paths).toEqual([])
})
it("keeps an unreadable server refusal visible", async () => {
 api.mockResolvedValue(new Response("not JSON", { status: 503 }));const { wrapper } = setup()
 const { result } = renderHook(() => usePageAccessMe("ws", "ops"), { wrapper })
 await waitFor(() => expect(result.current.error).toBe("page access: 503"))
})
it.each([null, {}, { page: "invalid", pages: [null, 3, { slug: "real" }] }])("does not fabricate mutation receipts from %j", async body => {
 api.mockImplementation(async () => reply(body));const { wrapper } = setup()
 const { result } = renderHook(() => ({ folders: usePageFolderMutations("ws"), acl: useFolderAclMutations("ws", "ops") }), { wrapper })
 await act(async () => {
  expect(await result.current.folders.create.mutateAsync({ name: "Ops", owner: "crew/ops" })).toBeNull()
  expect(await result.current.folders.update.mutateAsync({ slug: "ops" })).toBeNull()
  expect(await result.current.folders.addPage.mutateAsync({ folder: "ops", page: "page", pagesVersion: null, aclVersion: null })).toBeNull()
  expect(await result.current.folders.addPages.mutateAsync({ folder: "ops", pages: [], aclVersion: null })).toEqual(body && "pages" in body ? [{ slug: "real" }] : [])
  expect(await result.current.acl.set.mutateAsync({ subjectType: "user", canWrite: false })).toBeNull()
 })
})
it("preserves a batch refusal even when its body cannot be read", async () => {
 api.mockResolvedValue({ ok: false, status: 502, text: async () => { throw new Error("stream interrupted") } });const { wrapper } = setup()
 const { result } = renderHook(() => usePageFolderMutations("ws"), { wrapper })
 await act(async () => { await expect(result.current.addPages.mutateAsync({ folder: "ops", pages: [{ page: "page", pagesVersion: 1 }], aclVersion: 2 })).rejects.toMatchObject({ status: 502, page: null, message: "move pages: 502" }) })
})
