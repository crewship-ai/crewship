import { afterEach, describe, expect, it, vi } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { canonicalArtifactHref, legacyPagesTarget, resolvedArtifactId, type LegacyPagesTarget } from "../legacy-pages-url"

// #5129: every artifact has one address, /artifacts/<immutable page ID>. Each
// old Pages address shape either names its canonical target directly or names
// the reference that resolves to the ID; anything only Pages can show stays.
const trusted = { workspaceId: "ws1", library: true, trusted: true }

const resolve = (reference: string, extra: Partial<Extract<LegacyPagesTarget, { to: "resolve" }>> = {}): LegacyPagesTarget =>
  ({ to: "resolve", reference, workspaceId: "ws1", project: false, ...extra })

describe("legacyPagesTarget: every old URL shape and its canonical target", () => {
  it.each<[string, string, string, LegacyPagesTarget]>([
    ["overview", "/pages", "", { to: "library", href: "/artifacts" }],
    ["overview with its workspace", "/pages", "workspace_id=ws1", { to: "library", href: "/artifacts?workspace_id=ws1" }],
    ["overview with a trailing slash", "/pages/", "workspace_id=ws1", { to: "library", href: "/artifacts?workspace_id=ws1" }],
    ["reference link", "/pages", "ref=page-brief&workspace_id=ws1", resolve("@page-brief")],
    ["slug", "/pages/brief", "", resolve("brief")],
    ["slug with its workspace", "/pages/brief", "workspace_id=ws1", resolve("brief")],
    ["slug with a trailing slash", "/pages/brief/", "workspace_id=ws1", resolve("brief")],
    ["percent-encoded slug", "/pages/q4%20plan", "", resolve("q4 plan")],
    ["immutable ID", "/pages/%40page-brief", "workspace_id=ws1", resolve("@page-brief")],
    ["unencoded immutable ID", "/pages/@page-brief", "workspace_id=ws1", resolve("@page-brief")],
    ["project link (#4995)", "/pages/brief", "artifact=1&workspace_id=ws1", resolve("brief", { project: true })],
    ["pinned project link", "/pages/%40page-brief", "artifact=1&version=3&workspace_id=ws1", resolve("@page-brief", { project: true, version: 3 })],
    ["pinned version without artifact=1", "/pages/brief", "version=2&workspace_id=ws1", resolve("brief", { project: true, version: 2 })],
    ["unusable pin", "/pages/brief", "artifact=1&version=bogus", resolve("brief", { project: true })],
    ["native editor", "/pages/craft/editor", "workspace_id=ws1&page_id=page-brief", resolve("@page-brief")],
    ["native editor, other parameter order", "/pages/craft/editor", "page_id=page-brief&workspace_id=ws1", resolve("@page-brief")],
  ])("%s: %s?%s", (_name, pathname, search, target) => {
    expect(legacyPagesTarget(pathname, search, trusted)).toEqual(target)
  })

  it.each<[string, string, string]>([
    // Only Pages has these: the panel editor and the project source editor.
    ["Pages editor", "/pages/brief", "mode=edit&workspace_id=ws1"],
    ["project source editor", "/pages/brief", "artifact=1&edit=1&workspace_id=ws1"],
    ["reference on a single page", "/pages/brief", "ref=page-brief"],
    // A malformed reference is refused on Pages exactly as before.
    ["reference without its workspace", "/pages", "ref=page-brief"],
    ["reference from another workspace", "/pages", "ref=page-brief&workspace_id=ws2"],
    ["reference given twice", "/pages", "ref=a&ref=b&workspace_id=ws1"],
    ["reference with an unsafe ID", "/pages", "ref=..%2Fx&workspace_id=ws1"],
    ["native editor without its workspace", "/pages/craft/editor", "page_id=page-brief"],
    ["native editor from another workspace", "/pages/craft/editor", "page_id=page-brief&workspace_id=ws2"],
    ["native editor for two pages", "/pages/craft/editor", "page_id=a&page_id=b&workspace_id=ws1"],
    ["native editor with an unsafe ID", "/pages/craft/editor", "page_id=a%00b&workspace_id=ws1"],
    ["malformed escape", "/pages/%", ""],
    ["deeper path", "/pages/brief/extra", ""],
  ])("stays on Pages for the %s", (_name, pathname, search) => {
    expect(legacyPagesTarget(pathname, search, trusted)).toEqual({ to: "stay" })
  })

  it.each([
    ["/pages", "workspace_id=ws1"],
    ["/pages", "ref=page-brief&workspace_id=ws1"],
    ["/pages/brief", ""],
    ["/pages/%40page-brief", "artifact=1&version=3"],
    ["/pages/craft/editor", "workspace_id=ws1&page_id=page-brief"],
  ])("keeps a restricted member on Pages and the library-off build on Pages: %s?%s", (pathname, search) => {
    expect(legacyPagesTarget(pathname, search, { ...trusted, trusted: false })).toEqual({ to: "stay" })
    expect(legacyPagesTarget(pathname, search, { ...trusted, library: false })).toEqual({ to: "stay" })
  })

  it("does not touch the public share address", () => {
    expect(legacyPagesTarget("/p/token123", "", trusted)).toEqual({ to: "stay" })
  })
})

describe("canonicalArtifactHref", () => {
  it.each<[string, string, number | undefined, string]>([
    ["page-brief", "ws1", undefined, "/artifacts/page-brief?workspace_id=ws1"],
    ["page-brief", "ws1", 4, "/artifacts/page-brief?workspace_id=ws1&version=4"],
    ["page a", "ws 1", undefined, "/artifacts/page%20a?workspace_id=ws+1"],
  ])("%s in %s pinned %s", (id, workspace, version, href) => {
    expect(canonicalArtifactHref(id, workspace, version)).toBe(href)
  })
})

describe("resolvedArtifactId", () => {
  afterEach(() => vi.unstubAllEnvs())
  const page = (extra: Record<string, unknown>) => toPageView({ id: "page-brief", slug: "brief", name: "Brief", owner: "user/u1", reach: ["owner"], ...extra } as unknown as WirePage)

  it.each([
    ["a native Word", { artifact_kind: "word", has_document: true }],
    ["a native Sheet", { artifact_kind: "sheet", has_workbook: true }],
    ["a panel Page", {}],
    ["a project", { artifact_kind: "project" }],
  ])("opens %s at its ID", (_name, extra) => {
    expect(resolvedArtifactId(resolve("brief"), page(extra))).toBe("page-brief")
  })

  it("keeps an older Craft and an invalid ID on Pages", () => {
    expect(resolvedArtifactId(resolve("brief"), page({ has_workbook: true }))).toBeNull()
    expect(resolvedArtifactId(resolve("brief"), page({ id: "not a valid id!" }))).toBeNull()
    expect(resolvedArtifactId(resolve("brief"), null)).toBeNull()
  })

  it("moves a project link only to a project viewer the detail has", () => {
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
    expect(resolvedArtifactId(resolve("brief", { project: true }), page({ artifact_kind: "project" }))).toBe("page-brief")
    expect(resolvedArtifactId(resolve("brief", { project: true }), page({ artifact_kind: "word", has_document: true }))).toBeNull()
    vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "false")
    expect(resolvedArtifactId(resolve("brief", { project: true }), page({ artifact_kind: "project" }))).toBeNull()
  })
})
