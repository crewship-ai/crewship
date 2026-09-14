/**
 * The folders data layer (#2527). What is pinned here is the wire: the
 * routes, the bodies a move and a removal send, and the shape a 409 comes
 * back as — a typed fence naming which version moved, never a string the
 * dialog would have to parse.
 */
import React from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => undefined }))

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch }))

import {
  FolderBatchRefusal,
  FolderFenceError,
  folderConflictOf,
  normalizeFolderAcl,
  normalizeFolderConflict,
  normalizeFolderList,
  refusedPageOf,
  toPageFolderView,
  useFolderAcl,
  useFolderAclMutations,
  usePageAccessMe,
  usePageFolderMutations,
  usePageFolders,
} from "@/hooks/use-page-folders"
import { PagesRequestError } from "@/hooks/use-pages"

function json(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: async () => body,
    text: async () => (body === null ? "" : JSON.stringify(body)),
    headers: { get: () => null },
  } as unknown as Response
}

function wrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

const lastCall = () => {
  const [url, init] = apiFetch.mock.calls.at(-1) as [string, RequestInit | undefined]
  return { url, method: (init?.method ?? "GET").toUpperCase(), body: init?.body ? JSON.parse(String(init.body)) : null }
}

beforeEach(() => apiFetch.mockReset())

describe("normalising", () => {
  it("reads a folder row tolerantly and clamps the count", () => {
    const view = toPageFolderView({ slug: "ops", name: " Ops ", icon: "", color: "amber", owner: "crew/lookout", page_count: -2, acl_version: 3 })!
    expect(view).toMatchObject({ slug: "ops", name: "Ops", icon: null, color: "amber", ownerRef: "crew/lookout", ownerLabel: "lookout", pageCount: 0, aclVersion: 3, shared: "unknown" })
    expect(toPageFolderView({ slug: "x", shared: "workspace" })!.shared).toBe("workspace")
    expect(toPageFolderView({ slug: "x", shared: "bogus" })!.shared).toBe("unknown")
    expect(toPageFolderView({ name: "no slug" })).toBeNull()
    expect(toPageFolderView({ slug: "x", owner_crew_name: "Lookout", owner: "crew/lookout" })!.ownerLabel).toBe("Lookout")
  })

  it("reads the {folders} envelope and a bare array", () => {
    expect(normalizeFolderList({ folders: [{ slug: "a" }] })).toEqual([{ slug: "a" }])
    expect(normalizeFolderList([{ slug: "b" }])).toEqual([{ slug: "b" }])
    expect(normalizeFolderList("nonsense")).toEqual([])
  })

  it("types the 409 body and keeps the server's sentence", () => {
    expect(normalizeFolderConflict({ error: "stale", conflict: "acl_version", pages_version: 2, acl_version: 7 })).toEqual({
      error: "stale",
      conflict: "acl_version",
      pages_version: 2,
      acl_version: 7,
    })
    expect(normalizeFolderConflict({ conflict: "bogus" })).toEqual({ error: "The folder or the page changed; try again." })
    expect(folderConflictOf(new FolderFenceError({ error: "x" }))).toEqual({ error: "x" })
    expect(folderConflictOf(new Error("x"))).toBeNull()
  })

  it("carries the ACL a 409 sends a manager, and the page a batch stopped at", () => {
    const c = normalizeFolderConflict({
      error: "acl_version is stale",
      conflict: "acl_version",
      acl_version: 8,
      page: "my-notes",
      acl: [{ subject_type: "crew", subject_id: "c2", label: "Ops", can_read: true, can_write: true }],
    })
    expect(c).toMatchObject({ conflict: "acl_version", acl_version: 8, page: "my-notes" })
    expect(c.acl).toEqual([{ subjectType: "crew", subjectId: "c2", label: "Ops", canWrite: true, setBy: null, setAt: null }])
    expect(refusedPageOf(new FolderFenceError(c))).toBe("my-notes")
    expect(refusedPageOf(new FolderBatchRefusal(403, "no", "fleet-201"))).toBe("fleet-201")
    expect(refusedPageOf(new Error("x"))).toBeNull()
  })

  it("reads the ACL envelope, always with read, and names the workspace row itself", () => {
    const acl = normalizeFolderAcl({
      acl_version: 4,
      acl: [
        { subject_type: "workspace", subject_id: "", label: "", can_read: true, can_write: false, set_by: "ada@example.com", set_at: "2026-09-13T10:00:00Z" },
        { subject_type: "user", subject_id: "u2", label: "bob@example.com", can_read: true, can_write: "yes" },
        { subject_type: "agent", subject_id: "ag1", label: "watcher" },
        { subject_type: "crew", subject_id: "" },
      ],
    })
    expect(acl.aclVersion).toBe(4)
    expect(acl.entries).toEqual([
      { subjectType: "workspace", subjectId: "", label: "Everyone in this workspace", canWrite: false, setBy: "ada@example.com", setAt: "2026-09-13T10:00:00Z" },
      { subjectType: "user", subjectId: "u2", label: "bob@example.com", canWrite: false, setBy: null, setAt: null },
    ])
    expect(normalizeFolderAcl("nonsense")).toEqual({ entries: [], aclVersion: null })
  })
})

describe("usePageFolders", () => {
  it("reads the list for the workspace, sorted A→Z", async () => {
    apiFetch.mockResolvedValue(json({ folders: [{ slug: "ops", name: "Ops", page_count: 2 }, { slug: "arc", name: "Archive" }] }))
    const { result } = renderHook(() => usePageFolders("ws-1"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(lastCall()).toMatchObject({ url: "/api/v1/page-folders?workspace_id=ws-1", method: "GET" })
    expect(result.current.folders.map((f) => f.name)).toEqual(["Archive", "Ops"])
    expect(result.current.supported).toBe(true)
  })

  it("reports a 404 as 'this server has no folders', not as an error", async () => {
    apiFetch.mockResolvedValue(json({ error: "not found" }, 404))
    const { result } = renderHook(() => usePageFolders("ws-1"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.supported).toBe(false)
    expect(result.current.error).toBeNull()
    expect(result.current.folders).toEqual([])
  })

  it("keeps a refusal's sentence", async () => {
    apiFetch.mockResolvedValue(json({ error: "no workspace" }, 403))
    const { result } = renderHook(() => usePageFolders("ws-1"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.error).toBe("no workspace"))
  })
})

describe("usePageFolderMutations", () => {
  it("files a page with both versions in the body", async () => {
    apiFetch.mockResolvedValue(json({ page: { slug: "p", folder: { slug: "ops" }, pages_version: 4 } }))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const page = await result.current.addPage.mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, aclVersion: 5 })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders/ops/pages?workspace_id=ws-1",
      method: "POST",
      body: { page: "p", pages_version: 3, acl_version: 5 },
    })
    expect(page).toMatchObject({ slug: "p", pages_version: 4 })
  })

  it("throws the typed fence on a 409, carrying the fresh versions", async () => {
    apiFetch.mockResolvedValue(json({ error: "pages_version is stale", conflict: "pages_version", pages_version: 9, acl_version: 5 }, 409))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const err = await result.current.addPage
      .mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, aclVersion: 5 })
      .catch((e: unknown) => e)
    expect(err).toBeInstanceOf(FolderFenceError)
    expect(folderConflictOf(err)).toEqual({ error: "pages_version is stale", conflict: "pages_version", pages_version: 9, acl_version: 5 })
  })

  it("keeps a 403's status and sentence", async () => {
    apiFetch.mockResolvedValue(json({ error: "Only a manager of Lookout can file pages into Ops." }, 403))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const err = (await result.current.addPage
      .mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, aclVersion: 5 })
      .catch((e: unknown) => e)) as PagesRequestError
    expect(err).toBeInstanceOf(PagesRequestError)
    expect(err.status).toBe(403)
    expect(err.message).toBe("Only a manager of Lookout can file pages into Ops.")
  })

  it("removes a page with its pages_version in the DELETE body", async () => {
    apiFetch.mockResolvedValue(json(null, 204))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    await result.current.removePage.mutateAsync({ folder: "ops", page: "fleet 201", pagesVersion: 3 })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders/ops/pages/fleet%20201?workspace_id=ws-1",
      method: "DELETE",
      body: { pages_version: 3 },
    })
  })

  it("creates with name, owner, icon and colour, and reads the folder back", async () => {
    apiFetch.mockResolvedValue(json({ id: "f1", slug: "runbooks", name: "Runbooks", icon: "rocket", color: "amber", owner: "crew/ops" }, 201))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const folder = await result.current.create.mutateAsync({ name: "Runbooks", owner: "crew/ops", icon: "rocket", color: "amber" })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders?workspace_id=ws-1",
      method: "POST",
      body: { name: "Runbooks", owner: "crew/ops", icon: "rocket", color: "amber" },
    })
    expect(folder).toMatchObject({ slug: "runbooks", icon: "rocket", color: "amber" })
  })

  it("patches only the fields given, and deletes with no body", async () => {
    apiFetch.mockResolvedValue(json({ slug: "ops", name: "Operations" }))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    await result.current.update.mutateAsync({ slug: "ops", name: "Operations" })
    expect(lastCall()).toEqual({ url: "/api/v1/page-folders/ops?workspace_id=ws-1", method: "PATCH", body: { name: "Operations" } })

    apiFetch.mockResolvedValue(json({ error: "Move its 3 pages out first." }, 409))
    const err = (await result.current.remove.mutateAsync("ops").catch((e: unknown) => e)) as Error
    expect(lastCall()).toEqual({ url: "/api/v1/page-folders/ops?workspace_id=ws-1", method: "DELETE", body: null })
    expect(err.message).toBe("Move its 3 pages out first.")
  })

  it("files several pages through the batch route, every page with its own version", async () => {
    apiFetch.mockResolvedValue(json({ pages: [{ slug: "a", pages_version: 1 }, { slug: "b", pages_version: 2 }] }))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const pages = await result.current.addPages.mutateAsync({
      folder: "ops",
      pages: [{ page: "a", pagesVersion: 0 }, { page: "b", pagesVersion: 1 }],
      aclVersion: 5,
    })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders/ops/pages:batch?workspace_id=ws-1",
      method: "POST",
      body: { pages: [{ page: "a", pages_version: 0 }, { page: "b", pages_version: 1 }], acl_version: 5 },
    })
    expect(pages.map((p) => p.slug)).toEqual(["a", "b"])
  })

  it("names the page a batch was refused for, on a 403 and on a 409", async () => {
    apiFetch.mockResolvedValue(json({ error: "You do not own b.", page: "b" }, 403))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const refused = await result.current.addPages
      .mutateAsync({ folder: "ops", pages: [{ page: "a", pagesVersion: 0 }, { page: "b", pagesVersion: 1 }], aclVersion: 5 })
      .catch((e: unknown) => e)
    expect(refused).toBeInstanceOf(FolderBatchRefusal)
    expect(refusedPageOf(refused)).toBe("b")
    expect((refused as Error).message).toBe("You do not own b.")

    apiFetch.mockResolvedValue(json({ error: "pages_version is stale", conflict: "pages_version", pages_version: 3, acl_version: 5, page: "a" }, 409))
    const stale = await result.current.addPages
      .mutateAsync({ folder: "ops", pages: [{ page: "a", pagesVersion: 0 }], aclVersion: 5 })
      .catch((e: unknown) => e)
    expect(stale).toBeInstanceOf(FolderFenceError)
    expect(refusedPageOf(stale)).toBe("a")
  })
})

describe("useFolderAcl", () => {
  it("reads the folder's permissions and says the reader manages it", async () => {
    apiFetch.mockResolvedValue(json({ acl_version: 2, acl: [{ subject_type: "crew", subject_id: "c1", label: "Support", can_read: true, can_write: false }] }))
    const { result } = renderHook(() => useFolderAcl("ws-1", "ops"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(lastCall()).toMatchObject({ url: "/api/v1/page-folders/ops/acl?workspace_id=ws-1", method: "GET" })
    expect(result.current.manages).toBe(true)
    expect(result.current.aclVersion).toBe(2)
    expect(result.current.entries.map((e) => e.label)).toEqual(["Support"])
    expect(result.current.refusal).toBeNull()
  })

  it("keeps a 403 as the refusal, apart from an error, and the reader does not manage", async () => {
    apiFetch.mockResolvedValue(json({ error: "Only a manager of Lookout or a workspace admin can read who Ops is shared with." }, 403))
    const { result } = renderHook(() => useFolderAcl("ws-1", "ops"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.refusal).toBe("Only a manager of Lookout or a workspace admin can read who Ops is shared with.")
    expect(result.current.error).toBeNull()
    expect(result.current.manages).toBe(false)

    apiFetch.mockResolvedValue(json({ error: "boom" }, 500))
    const broken = renderHook(() => useFolderAcl("ws-1", "archive"), { wrapper: wrapper() })
    await waitFor(() => expect(broken.result.current.loading).toBe(false))
    expect(broken.result.current.error).toBe("boom")
    expect(broken.result.current.refusal).toBeNull()
  })
})

describe("useFolderAclMutations", () => {
  it("PUTs an entry with its subject and whether it may edit, never a bare write", async () => {
    // The server answers the whole ACL document with the written row under
    // `entry` (PutFolderACL → folderACLDocument), not the row alone.
    const written = { subject_type: "crew", subject_id: "c2", label: "Ops", can_read: true, can_write: true }
    apiFetch.mockResolvedValue(json({ folder: "ops", acl: [written], acl_version: 2, entry: written }))
    const { result } = renderHook(() => useFolderAclMutations("ws-1", "ops"), { wrapper: wrapper() })
    const entry = await result.current.set.mutateAsync({ subjectType: "crew", subjectId: "ops", canWrite: true })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders/ops/acl?workspace_id=ws-1",
      method: "PUT",
      body: { subject_type: "crew", subject_id: "ops", can_write: true },
    })
    expect(entry).toMatchObject({ subjectType: "crew", label: "Ops", canWrite: true })

    await result.current.set.mutateAsync({ subjectType: "workspace", canWrite: false })
    expect(lastCall().body).toEqual({ subject_type: "workspace", can_write: false })
  })

  it("DELETEs by subject, and the workspace row by its type alone", async () => {
    apiFetch.mockResolvedValue(json(null, 204))
    const { result } = renderHook(() => useFolderAclMutations("ws-1", "ops"), { wrapper: wrapper() })
    await result.current.unset.mutateAsync({ subjectType: "user", subjectId: "u 1" })
    expect(lastCall()).toEqual({ url: "/api/v1/page-folders/ops/acl/user/u%201?workspace_id=ws-1", method: "DELETE", body: null })
    await result.current.unset.mutateAsync({ subjectType: "workspace", subjectId: "" })
    // A path segment cannot be empty, so the workspace row is `/workspace/workspace`.
    expect(lastCall().url).toBe("/api/v1/page-folders/ops/acl/workspace/workspace?workspace_id=ws-1")
  })

  it("keeps the server's sentence on a refusal", async () => {
    apiFetch.mockResolvedValue(json({ error: "An agent cannot be given folder permissions." }, 400))
    const { result } = renderHook(() => useFolderAclMutations("ws-1", "ops"), { wrapper: wrapper() })
    const err = (await result.current.set.mutateAsync({ subjectType: "user", subjectId: "x", canWrite: false }).catch((e: unknown) => e)) as Error
    expect(err.message).toBe("An agent cannot be given folder permissions.")
  })
})

describe("usePageAccessMe", () => {
  it("reads the caller's own paths to a page", async () => {
    apiFetch.mockResolvedValue(json({ paths: ["crew:lookout", "folder:ops", 3, " "] }))
    const { result } = renderHook(() => usePageAccessMe("ws-1", "fleet 201"), { wrapper: wrapper() })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(lastCall()).toMatchObject({ url: "/api/v1/pages/fleet%20201/access/me?workspace_id=ws-1", method: "GET" })
    expect(result.current.paths).toEqual(["crew:lookout", "folder:ops"])
  })
})
