import { beforeEach, describe, expect, it, vi } from "vitest"
import type { LibraryItem } from "../library-items"

/*
 * httpSharingApi against the routes #4444 serves (25560ed4b). Every body here
 * is the handler's wire shape: pages_access.go + pages_access_impact.go,
 * pages_grants.go, pages_folder_acl.go, pages_archive.go, pages_folder_review.go.
 */

const net = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: net.fetch }))

import { httpSharingApi, SharingError } from "../sharing/sharing-api"

type Route = { method: string; path: string; status?: number; body?: unknown }
interface Call { method: string; path: string; query: URLSearchParams; body: unknown }

let routes: Route[] = []
let calls: Call[] = []

/** Answers each request with the first unused route that matches; an unmatched request fails the test. */
function serve(...next: Route[]) {
  routes = next.map(r => ({ ...r }))
  calls = []
  net.fetch.mockImplementation(async (input: string, init?: RequestInit) => {
    const url = new URL(input, "http://host")
    const method = (init?.method ?? "GET").toUpperCase()
    calls.push({ method, path: url.pathname, query: url.searchParams, body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined })
    const index = routes.findIndex(r => r.method === method && r.path === url.pathname)
    if (index < 0) throw new Error(`unexpected ${method} ${url.pathname}${url.search}`)
    const [route] = routes.splice(index, 1)
    const status = route.status ?? 200
    return new Response(status === 204 ? null : JSON.stringify(route.body ?? {}), { status })
  })
}

beforeEach(() => { net.fetch.mockReset(); routes = []; calls = [] })

const item = (over: Partial<LibraryItem> = {}): LibraryItem => ({
  slug: "brief", name: "Partner brief", kind: "word", family: "document", sharedVersion: 1, native: true, projectBacked: false,
  empty: false, private: false, ownerLabel: "You", folder: { slug: "fin", name: "Finance", icon: null, color: null }, updatedAt: null,
  pagesVersion: 7, ...over,
})

const perms = (read: boolean, comment = false, edit = false, produce = false) => ({ read, comment, edit, produce })
const subject = (subject_type: string, subject_id: string, label: string, sources: [string, ReturnType<typeof perms>][]) => ({
  subject_type, subject_id, label, paths: sources.map(([path]) => path),
  effective_permissions: sources.reduce((all, [, p]) => ({ read: all.read || p.read, comment: all.comment || p.comment, edit: all.edit || p.edit, produce: all.produce || p.produce }), perms(false)),
  sources: sources.map(([path, permissions]) => ({ path, inherited: path.startsWith("folder:"), permissions })),
})
const grant = (subject_type: string, subject: string, subject_id: string, level: string, over: Record<string, unknown> = {}) => ({
  subject_type, subject, subject_id, level, granted_by: "owner@example.test", granted_by_user_id: "u1", granted_at: "2026-10-28T19:00:00Z", live: true, ...over,
})

const api = () => httpSharingApi("ws1")
const ws = (call: Call) => call.query.get("workspace_id")

describe("access: GET /pages/{slug}/access + GET /pages/{slug}/grants", () => {
  const ACCESS_PAGE_1 = {
    page: "brief", page_id: "p1", pages_version: 7, next_cursor: "c1",
    subjects: [
      subject("user", "u1", "owner@example.test", [["owner", perms(true, true, true)]]),
      // Reaches through the workspace's read grant: a path, not a direct grant of their own.
      subject("user", "u2", "ann@example.test", [["grant:page:read", perms(true)], ["grant:page:comment", perms(true, true)]]),
    ],
  }
  const ACCESS_PAGE_2 = {
    page: "brief", page_id: "p1", pages_version: 7,
    subjects: [
      subject("user", "u3", "bo@example.test", [["folder:fin", perms(true)], ["grant:page:read", perms(true)]]),
      subject("crew", "c1", "research", [["crew:research", perms(true, true, true)]]),
      subject("agent", "a1", "scribe", [["grant:page:produce", perms(true, false, false, true)]]),
      { subject_type: "crew", subject_id: "withheld", paths: ["panel_crew:withheld"], effective_permissions: perms(true), sources: [{ path: "panel_crew:withheld", inherited: false, permissions: perms(true) }] },
    ],
  }
  const GRANTS = {
    page: "brief",
    grants: [
      grant("workspace", "workspace", "", "read"),
      grant("user", "ann@example.test", "u2", "comment"),
      grant("agent", "scribe", "a1", "produce", { panels: ["kpi"] }),
      grant("user", "gone@example.test", "u9", "write", { live: false, inert_reason: "the issuer is no longer an admin" }),
    ],
  }

  it("reads subjects[].sources, follows the cursor, and offers only real grants as removable", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/access", body: ACCESS_PAGE_1 },
      { method: "GET", path: "/api/v1/pages/brief/access", body: ACCESS_PAGE_2 },
      { method: "GET", path: "/api/v1/pages/brief/grants", body: GRANTS },
    )
    const entries = await api().access(item())
    const access = calls.filter(c => c.path.endsWith("/access"))
    expect(access.map(c => [ws(c), c.query.get("cursor")])).toEqual([["ws1", null], ["ws1", "c1"]])
    const rows = entries.map(e => [e.subjectType, e.subjectId, e.label, e.level, e.via, e.folderSlug ?? e.crew ?? e.inactive ?? ""])
    expect(rows).toEqual([
      ["user", "u1", "owner@example.test", "write", "owner", ""],
      ["user", "u3", "bo@example.test", "read", "folder", "fin"],
      ["crew", "c1", "research", "write", "crew", "research"],
      ["crew", "withheld", "A crew you cannot see", "read", "crew", "withheld"],
      // The grants themselves: Everyone, Ann's Comment, and the inert one with its reason. Produce is not a human level.
      ["workspace", "", "Everyone in this workspace", "read", "grant", ""],
      ["user", "u2", "ann@example.test", "comment", "grant", ""],
      ["user", "u9", "gone@example.test", "write", "grant", "the issuer is no longer an admin"],
    ])
    // Ann's `grant:page:read` came from the workspace grant: it is not a second direct grant to remove.
    expect(entries.filter(e => e.subjectId === "u2")).toHaveLength(1)
    expect(entries.find(e => e.folderSlug === "fin")?.folderName).toBe("Finance")
  })

  it("an unrecognised envelope is an error, never an empty (private) list", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/access", body: { entries: [] } },
      { method: "GET", path: "/api/v1/pages/brief/grants", body: GRANTS },
    )
    await expect(api().access(item())).rejects.toBeInstanceOf(SharingError)
  })

  it("passes the server's refusal through with its status", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/access", status: 403, body: { error: "only the page owner or a workspace admin may read this page's access" } },
      { method: "GET", path: "/api/v1/pages/brief/grants", status: 403, body: { error: "forbidden" } },
    )
    const error = await api().access(item()).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(SharingError)
    expect((error as SharingError).status).toBe(403)
  })
})

describe("share and revoke: PUT/DELETE /pages/{slug}/grants", () => {
  it("a downgrade adds the new level and removes the stronger one, by query", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [grant("user", "ann@example.test", "u2", "write"), grant("user", "ann@example.test", "u2", "comment")] } },
      { method: "PUT", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [], changed: 1 } },
      { method: "DELETE", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [], changed: 1 } },
      { method: "DELETE", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [], changed: 1 } },
    )
    await api().share(item(), { subjectType: "user", subjectId: "u2", label: "Ann", level: "read" })
    const [, put, ...deletes] = calls
    expect(put.body).toEqual({ subject_type: "user", subject: "u2", level: "read" })
    expect(ws(put)).toBe("ws1")
    expect(deletes.map(d => [d.body, d.query.get("subject_type"), d.query.get("subject"), d.query.get("level"), ws(d)])).toEqual([
      [undefined, "user", "u2", "write", "ws1"],
      [undefined, "user", "u2", "comment", "ws1"],
    ])
  })

  it("everyone in the workspace has no subject", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } },
      { method: "PUT", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [], changed: 1 } },
    )
    await api().share(item(), { subjectType: "workspace", subjectId: "", label: "Everyone", level: "comment" })
    expect(calls[1].body).toEqual({ subject_type: "workspace", level: "comment" })
  })

  it("says what was kept when the stronger level could not be removed", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [grant("user", "ann@example.test", "u2", "write")] } },
      { method: "PUT", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } },
      { method: "DELETE", path: "/api/v1/pages/brief/grants", status: 500, body: { error: "internal" } },
    )
    await expect(api().share(item(), { subjectType: "user", subjectId: "u2", label: "Ann", level: "read" }))
      .rejects.toThrow(/Ann still has Edit/)
  })

  it("revoke removes each human level the subject holds and leaves an agent's produce alone", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [grant("workspace", "workspace", "", "read"), grant("workspace", "workspace", "", "write"), grant("user", "x", "u2", "read")] } },
      { method: "DELETE", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } },
      { method: "DELETE", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } },
    )
    await api().revoke(item(), "workspace", "")
    expect(calls.slice(1).map(d => [d.query.get("subject_type"), d.query.has("subject"), d.query.get("level")])).toEqual([
      ["workspace", false, "read"], ["workspace", false, "write"],
    ])
  })
})

describe("folder settings: /page-folders/{slug}/acl", () => {
  const ACL = {
    folder: "fin", acl_version: 4,
    acl: [
      { subject_type: "workspace", subject_id: "", label: "workspace", can_read: true, can_write: false, set_by: "o", set_by_user_id: "u1", set_at: "t" },
      { subject_type: "user", subject_id: "u3", label: "bo@example.test", can_read: true, can_write: true, set_by: "o", set_by_user_id: "u1", set_at: "t" },
    ],
  }

  it("reads Read or Edit from can_write and reports no default audience", async () => {
    serve({ method: "GET", path: "/api/v1/page-folders/fin/acl", body: ACL })
    const settings = await api().folderSettings("fin")
    expect(settings.defaultAudience).toBeNull()
    expect(settings.entries.map(e => [e.subjectType, e.subjectId, e.label, e.level, e.via])).toEqual([
      ["workspace", "", "Everyone in this workspace", "read", "folder"],
      ["user", "u3", "bo@example.test", "write", "folder"],
    ])
    expect(api().capabilities).toMatchObject({ folderDefaultAudience: false, folderLevels: ["read", "write"], atomicFolderSave: false })
  })

  it("saves by entry: DELETE spells the workspace twice, PUT sends can_write and no level", async () => {
    serve(
      { method: "GET", path: "/api/v1/page-folders/fin/acl", body: ACL },
      { method: "DELETE", path: "/api/v1/page-folders/fin/acl/workspace/workspace", status: 204 },
      { method: "PUT", path: "/api/v1/page-folders/fin/acl", body: ACL },
      { method: "PUT", path: "/api/v1/page-folders/fin/acl", body: ACL },
    )
    await api().saveFolderSettings("fin", { defaultAudience: null, entries: [
      { subjectType: "user", subjectId: "u3", label: "Bo", level: "read", via: "folder" },
      { subjectType: "crew", subjectId: "c1", label: "Research", level: "write", via: "folder" },
    ] })
    expect(calls.slice(1).map(c => [c.method, c.path, c.body])).toEqual([
      ["DELETE", "/api/v1/page-folders/fin/acl/workspace/workspace", undefined],
      ["PUT", "/api/v1/page-folders/fin/acl", { subject_type: "user", subject_id: "u3", can_write: false }],
      ["PUT", "/api/v1/page-folders/fin/acl", { subject_type: "crew", subject_id: "c1", can_write: true }],
    ])
  })

  it("refuses Comment on a folder and a default audience before writing anything", async () => {
    serve()
    await expect(api().saveFolderSettings("fin", { defaultAudience: null, entries: [{ subjectType: "user", subjectId: "u3", label: "Bo", level: "comment", via: "folder" }] })).rejects.toThrow(/Read or Edit/)
    await expect(api().saveFolderSettings("fin", { defaultAudience: "private", entries: [] })).rejects.toThrow(/default/)
    expect(calls).toEqual([])
  })
})

describe("bulk: per-page reviewed routes, no /pages/bulk", () => {
  const archiveWire = (over: Record<string, unknown> = {}) => ({
    dry_run: true, page_id: "p1", page: "brief", pages_version: 7, before: { archived: false }, after: { archived: true },
    impact: { removed_from_default_lists: true, access_changed: false, content_deleted: false }, changed: false, review_token: "a".repeat(64), ...over,
  })

  it("archive previews with dry_run + pages_version and confirms with the review token", async () => {
    serve(
      { method: "POST", path: "/api/v1/pages/brief/archive", body: archiveWire() },
      { method: "POST", path: "/api/v1/pages/old/archive", body: archiveWire({ page: "old", before: { archived: true } }) },
      { method: "POST", path: "/api/v1/pages/crew-doc/archive", status: 403, body: { error: "archiving is initiated by the page owner or an authorized workspace admin" } },
    )
    const items = [item(), item({ slug: "old", pagesVersion: 2 }), item({ slug: "crew-doc", pagesVersion: 1 })]
    const preview = await api().bulk({ action: "archive", items, dryRun: true })
    expect(calls.map(c => [c.path, c.body, ws(c)])).toEqual([
      ["/api/v1/pages/brief/archive", { dry_run: true, pages_version: 7 }, "ws1"],
      ["/api/v1/pages/old/archive", { dry_run: true, pages_version: 2 }, "ws1"],
      ["/api/v1/pages/crew-doc/archive", { dry_run: true, pages_version: 1 }, "ws1"],
    ])
    expect(preview.dryRun).toBe(true)
    expect(preview.items.map(r => [r.slug, r.ok, r.reason ?? ""])).toEqual([
      ["brief", true, ""],
      ["old", false, "Already archived"],
      ["crew-doc", false, "archiving is initiated by the page owner or an authorized workspace admin"],
    ])

    serve({ method: "POST", path: "/api/v1/pages/brief/archive", body: archiveWire({ dry_run: false, changed: true, pages_version: 8, review_token: undefined }) })
    const done = await api().bulk({ action: "archive", items: [item()], dryRun: false, reviewed: preview.items.filter(r => r.ok) })
    expect(calls[0].body).toEqual({ confirm: true, review_token: "a".repeat(64), pages_version: 7 })
    expect(done.items.map(r => [r.slug, r.ok])).toEqual([["brief", true]])
  })

  it("never confirms an item without its preview", async () => {
    serve()
    const done = await api().bulk({ action: "unarchive", items: [item()], dryRun: false })
    expect(calls).toEqual([])
    expect(done.items[0]).toMatchObject({ ok: false, reason: expect.stringMatching(/preview/i) })
  })

  it("moving into a folder previews its real access impact and confirms against both versions", async () => {
    const change = (subject_type: string, subject_id: string, label: string, before: ReturnType<typeof perms>, after: ReturnType<typeof perms>) => ({ subject_type, subject_id, label, before, after })
    const review = {
      dry_run: true, page_id: "p1", page: "brief", pages_version: 7, from_folder: null, to_folder: { id: "f2", slug: "ops", acl_version: 3 },
      before: { subjects: [], withheld: false }, after: { subjects: [], withheld: false },
      changes: [change("user", "u4", "cy@example.test", perms(false), perms(true, true, true)), change("user", "u3", "bo@example.test", perms(true, true, true), perms(true))],
      changed: false, review_token: "b".repeat(64),
    }
    serve(
      { method: "GET", path: "/api/v1/page-folders/ops", body: { id: "f2", slug: "ops", name: "Ops", acl_version: 3, pages: [] } },
      { method: "POST", path: "/api/v1/page-folders/ops/pages", body: review },
    )
    const target = { slug: "ops", name: "Ops" }
    const preview = await api().bulk({ action: "move", items: [item({ folder: null })], folder: target, dryRun: true })
    expect(calls[1].body).toEqual({ page: "brief", pages_version: 7, acl_version: 3, dry_run: true })
    const [row] = preview.items
    expect(row.ok).toBe(true)
    expect(row.gains.map(e => [e.label, e.level])).toEqual([["cy@example.test", "write"]])
    expect(row.loses.map(e => [e.label, e.level])).toEqual([["bo@example.test", "write"]])

    serve({ method: "POST", path: "/api/v1/page-folders/ops/pages", body: { ...review, dry_run: false, changed: true, review_token: undefined } })
    await api().bulk({ action: "move", items: [item({ folder: null })], folder: target, dryRun: false, reviewed: preview.items })
    expect(calls[0].body).toEqual({ page: "brief", pages_version: 7, acl_version: 3, confirm: true, review_token: "b".repeat(64) })
  })

  it("a withheld folder ACL is said, not shown as no change", async () => {
    serve(
      { method: "GET", path: "/api/v1/page-folders/ops", body: { slug: "ops", acl_version: 3, pages: [] } },
      { method: "POST", path: "/api/v1/page-folders/ops/pages", body: { dry_run: true, page: "brief", pages_version: 7, to_folder: { id: "f2", slug: "ops", acl_version: 3 }, from_folder: null, before: { subjects: [], withheld: true }, after: { subjects: [], withheld: true }, changes: [], changed: false, review_token: "c".repeat(64) } },
    )
    const preview = await api().bulk({ action: "move", items: [item({ folder: null })], folder: { slug: "ops", name: "Ops" }, dryRun: true })
    expect(preview.items[0]).toMatchObject({ ok: true, gains: [], loses: [], note: expect.stringMatching(/cannot see/) })
  })

  it("moving to Unfiled has no server preview and says so, then removes with pages_version", async () => {
    serve()
    const preview = await api().bulk({ action: "move", items: [item()], folder: null, dryRun: true })
    expect(calls).toEqual([])
    expect(preview.items[0]).toMatchObject({ ok: true, note: expect.stringMatching(/not previewed/) })
    serve({ method: "DELETE", path: "/api/v1/page-folders/fin/pages/brief", body: { slug: "brief" } })
    await api().bulk({ action: "move", items: [item()], folder: null, dryRun: false, reviewed: preview.items })
    expect(calls.map(c => [c.method, c.path, c.body])).toEqual([["DELETE", "/api/v1/page-folders/fin/pages/brief", { pages_version: 7 }]])
  })

  it("share previews from the real access list and applies one grant per item", async () => {
    serve(
      { method: "GET", path: "/api/v1/pages/brief/access", body: { page: "brief", page_id: "p1", pages_version: 7, subjects: [] } },
      { method: "GET", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } },
      { method: "GET", path: "/api/v1/pages/memo/access", body: { page: "memo", page_id: "p2", pages_version: 1, subjects: [] } },
      { method: "GET", path: "/api/v1/pages/memo/grants", body: { page: "memo", grants: [grant("user", "ann@example.test", "u2", "write")] } },
    )
    const items = [item(), item({ slug: "memo" })]
    const preview = await api().bulk({ action: "share", items, subjectType: "user", subjectId: "u2", subjectLabel: "Ann", level: "comment", dryRun: true })
    expect(preview.items.map(r => [r.slug, r.ok, r.reason ?? "", r.gains.map(g => g.level)])).toEqual([
      ["brief", true, "", ["comment"]], ["memo", false, "Already has this access", []],
    ])
    serve({ method: "PUT", path: "/api/v1/pages/brief/grants", body: { page: "brief", grants: [] } })
    await api().bulk({ action: "share", items: [item()], subjectType: "user", subjectId: "u2", subjectLabel: "Ann", level: "comment", dryRun: false, reviewed: preview.items })
    expect(calls.map(c => c.body)).toEqual([{ subject_type: "user", subject: "u2", level: "comment" }])
  })
})

describe("library state from the page fields", () => {
  it("archived and test come from the server's flags, never from the name", () => {
    const state = api().libraryState([item({ slug: "a", archived: true }), item({ slug: "b", name: "smoke test 3" }), item({ slug: "c", test: true })])
    expect([...state.archived]).toEqual(["a"])
    expect([...state.test]).toEqual(["c"])
  })
})
