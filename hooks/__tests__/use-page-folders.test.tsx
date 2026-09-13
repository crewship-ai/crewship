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
  FolderFenceError,
  folderConflictOf,
  normalizeFolderConflict,
  normalizeFolderList,
  toPageFolderView,
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
    const view = toPageFolderView({ slug: "ops", name: " Ops ", icon: "", color: "amber", owner: "crew/lookout", page_count: -2, grants_version: 3 })!
    expect(view).toMatchObject({ slug: "ops", name: "Ops", icon: null, color: "amber", ownerRef: "crew/lookout", ownerLabel: "lookout", pageCount: 0, grantsVersion: 3 })
    expect(toPageFolderView({ name: "no slug" })).toBeNull()
    expect(toPageFolderView({ slug: "x", owner_crew_name: "Lookout", owner: "crew/lookout" })!.ownerLabel).toBe("Lookout")
  })

  it("reads the {folders} envelope and a bare array", () => {
    expect(normalizeFolderList({ folders: [{ slug: "a" }] })).toEqual([{ slug: "a" }])
    expect(normalizeFolderList([{ slug: "b" }])).toEqual([{ slug: "b" }])
    expect(normalizeFolderList("nonsense")).toEqual([])
  })

  it("types the 409 body and keeps the server's sentence", () => {
    expect(normalizeFolderConflict({ error: "stale", conflict: "grants_version", pages_version: 2, grants_version: 7 })).toEqual({
      error: "stale",
      conflict: "grants_version",
      pages_version: 2,
      grants_version: 7,
    })
    expect(normalizeFolderConflict({ conflict: "bogus" })).toEqual({ error: "The folder or the page changed; try again." })
    expect(folderConflictOf(new FolderFenceError({ error: "x" }))).toEqual({ error: "x" })
    expect(folderConflictOf(new Error("x"))).toBeNull()
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
    const page = await result.current.addPage.mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, grantsVersion: 5 })
    expect(lastCall()).toEqual({
      url: "/api/v1/page-folders/ops/pages?workspace_id=ws-1",
      method: "POST",
      body: { page: "p", pages_version: 3, grants_version: 5 },
    })
    expect(page).toMatchObject({ slug: "p", pages_version: 4 })
  })

  it("throws the typed fence on a 409, carrying the fresh versions", async () => {
    apiFetch.mockResolvedValue(json({ error: "pages_version is stale", conflict: "pages_version", pages_version: 9, grants_version: 5 }, 409))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const err = await result.current.addPage
      .mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, grantsVersion: 5 })
      .catch((e: unknown) => e)
    expect(err).toBeInstanceOf(FolderFenceError)
    expect(folderConflictOf(err)).toEqual({ error: "pages_version is stale", conflict: "pages_version", pages_version: 9, grants_version: 5 })
  })

  it("keeps a 403's status and sentence", async () => {
    apiFetch.mockResolvedValue(json({ error: "Only a manager of Lookout can file pages into Ops." }, 403))
    const { result } = renderHook(() => usePageFolderMutations("ws-1"), { wrapper: wrapper() })
    const err = (await result.current.addPage
      .mutateAsync({ folder: "ops", page: "p", pagesVersion: 3, grantsVersion: 5 })
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
})
