import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({
  linkUnavailable: false, params: new URLSearchParams("workspace_id=ws1"), replace: vi.fn(), push: vi.fn(), access: "trusted" as string,
  read: vi.fn(), wire: null as Record<string, unknown> | null, loading: false, denied: false, catalog: vi.fn(),
}))
// Like the static export: every ID is served the "_" shell and useParams() keeps returning it.
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useParams: () => ({ id: "_" }), usePathname: () => window.location.pathname, useRouter: () => ({ replace: nav.replace, push: nav.push }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: nav.linkUnavailable }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => nav.access }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({ workspaceName: null, role: null, surfaces: ["pages"] }) }))
vi.mock("@/components/features/artifacts-library/restricted-artifacts", () => ({
  RestrictedArtifacts: ({ workspaceId, focus, byId, hrefFor, homeHref }: { workspaceId: string; focus: string | null; byId?: boolean; hrefFor: (slug: string) => string; homeHref: string }) => {
    nav.catalog(workspaceId, focus, Boolean(byId))
    return <p data-testid="restricted-catalog" data-focus={focus ?? "none"} data-by={byId ? "id" : "slug"} data-open={hrefFor("brief")} data-home={homeHref}>Restricted catalog</p>
  },
}))
vi.mock("@/hooks/use-pages", async importOriginal => {
  const original = await importOriginal<typeof import("@/hooks/use-pages")>()
  return {
    ...original,
    usePage: (...args: unknown[]) => {
      nav.read(...args)
      const page = nav.wire && !nav.denied ? original.toPageView(nav.wire as never) : null
      return { page, raw: page ? nav.wire : null, loading: nav.loading, error: nav.denied ? "denied" : null, accessDenied: nav.denied, notFound: false, live: "live" }
    },
  }
})
vi.mock("@/components/features/artifacts-library/artifacts-library", () => ({
  ArtifactsLibrary: ({ workspaceId, item, version, bySlug, onSelect, onCanonical, filters, onFiltersChange, edit, onEditChange }: {
    workspaceId: string; item: string | null; version?: number; bySlug?: boolean; onSelect: (id: string | null, slug?: string) => void
    onCanonical: (id: string) => void; filters: { view: string; q: string }; onFiltersChange: (next: unknown) => void
    edit?: boolean; onEditChange?: (edit: boolean) => void
  }) =>
    <div data-testid="library" data-ws={workspaceId} data-item={item ?? ""} data-version={version ?? ""} data-by-slug={String(Boolean(bySlug))} data-view={filters.view} data-q={filters.q} data-edit={String(Boolean(edit))}>
      <button onClick={() => onEditChange?.(true)}>start editing</button><button onClick={() => onEditChange?.(false)}>stop editing</button>
      <button onClick={() => onSelect("page-site")}>pick</button><button onClick={() => onSelect(null)}>back</button>
      <button onClick={() => onSelect(null, "no-id")}>pick without ID</button><button onClick={() => onCanonical("page-brief")}>resolved</button>
      <button onClick={() => onFiltersChange({ ...filters, view: "all", q: "kind:word" })}>search</button>
    </div>,
}))

vi.mock("next/dynamic", () => ({ default: () => ({ slug, initialVersion }: { slug: string; initialVersion?: number }) => <div data-testid="design" data-slug={slug} data-version={initialVersion ?? ""} /> }))

import ArtifactsPage from "../page"
import ArtifactRoute from "../[id]/page"

const at = (path: string) => window.history.replaceState(null, "", path)
beforeEach(() => { Object.assign(nav, { access: "trusted", wire: null, loading: false, denied: false }); nav.read.mockReset(); nav.catalog.mockReset(); nav.linkUnavailable = false; at("/artifacts"); nav.params = new URLSearchParams("workspace_id=ws1"); nav.replace.mockReset(); nav.push.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("with the flag off sends saved links to Pages", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  render(<ArtifactsPage />)
  expect(screen.queryByTestId("library")).toBeNull()
  expect(nav.replace).toHaveBeenCalledWith("/pages?workspace_id=ws1")
})

it("opens an artifact at its own address inside Artifacts, and Back returns to the list", () => {
  render(<ArtifactsPage />)
  expect(screen.getByTestId("library").dataset.item).toBe("")
  fireEvent.click(screen.getByText("pick"))
  expect(nav.push).toHaveBeenCalledWith("/artifacts/page-site?workspace_id=ws1")
  cleanup()
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ ws: "ws1", item: "page-site" })
  fireEvent.click(screen.getByText("back"))
  expect(nav.push).toHaveBeenLastCalledWith("/artifacts?workspace_id=ws1")
})

it("reads the ID from the live URL, not the exported placeholder", () => {
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ item: "page-site", bySlug: "false" })
  cleanup()
  at("/artifacts/page%20a")
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset.item).toBe("page a")
  cleanup()
  at("/artifacts/_")
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset.item).toBe("")
})

it("opens a link from before IDs, /artifacts?item=<slug>, by slug and then moves to the ID address", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&view=all&item=brief")
  render(<ArtifactsPage />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ item: "brief", bySlug: "true" })
  fireEvent.click(screen.getByText("resolved"))
  expect(nav.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1&view=all")
})

it("keeps the slug address for an artifact without an immutable ID", () => {
  render(<ArtifactsPage />)
  fireEvent.click(screen.getByText("pick without ID"))
  expect(nav.push).toHaveBeenCalledWith("/artifacts?workspace_id=ws1&item=no-id")
})

it("keeps the view and filters in the URL and replaces the entry while filtering", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&view=attention&q=brief")
  render(<ArtifactsPage />)
  expect([screen.getByTestId("library").dataset.view, screen.getByTestId("library").dataset.q]).toEqual(["attention", "brief"])
  fireEvent.click(screen.getByText("search"))
  expect(nav.replace).toHaveBeenCalledWith("/artifacts?workspace_id=ws1&view=all&q=kind%3Aword")
  fireEvent.click(screen.getByText("pick"))
  expect(nav.push).toHaveBeenCalledWith("/artifacts/page-site?workspace_id=ws1&view=attention&q=brief")
})

it("opens a shared Design version full screen instead of moving the link", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true")
  nav.params = new URLSearchParams("design=1&item=wireframe&version=2&workspace_id=ws1")
  render(<ArtifactsPage />)
  expect(screen.getByTestId("design").dataset).toMatchObject({ slug: "wireframe", version: "2" })
  expect(nav.replace).not.toHaveBeenCalled()
})

// #4995: the API returns /artifacts/<page_id>?workspace_id=…[&version=N] for projects.
it("opens the version a project link pins, and only for that artifact", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&version=2")
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ ws: "ws1", item: "page-site", version: "2" })
  cleanup()
  for (const bad of ["0", "-1", "1.5", "bogus"]) {
    nav.params = new URLSearchParams(`workspace_id=ws1&version=${bad}`)
    render(<ArtifactRoute />)
    expect(screen.getByTestId("library").dataset.version, bad).toBe("")
    cleanup()
  }
  nav.params = new URLSearchParams("workspace_id=ws1&version=2")
  at("/artifacts")
  render(<ArtifactsPage />)
  expect(screen.getByTestId("library").dataset.version).toBe("")
})

const wire = (extra: Record<string, unknown> = {}) => ({ id: "page-site", slug: "site", name: "Site", owner: "user/u1", reach: ["owner"], ...extra })

it("without the library keeps a project link's identity, workspace and pin on Pages", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.wire = wire({ artifact_kind: "project" })
  nav.params = new URLSearchParams("workspace_id=ws1&version=2")
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.read).toHaveBeenCalledWith("ws1", "@page-site")
  expect(nav.replace).toHaveBeenCalledWith("/pages/%40page-site?workspace_id=ws1&artifact=1&version=2")
})

// #5129: generated links name /artifacts/<id> for every kind; a build without
// the library opens each in its own Pages detail, never the project viewer.
it.each([
  ["a native Word", { artifact_kind: "word", has_document: true, native_craft_application: "wordcraft" }],
  ["a native Sheet", { artifact_kind: "sheet", has_workbook: true }],
  ["a panel Page", {}],
])("without the library opens %s at its Pages detail", (_name, extra) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.wire = wire(extra)
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.replace).toHaveBeenCalledWith("/pages/%40page-site?workspace_id=ws1")
})

it("without the library a refused read still lands on the Pages detail, which answers it", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.denied = true
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.replace).toHaveBeenCalledWith("/pages/%40page-site?workspace_id=ws1")
})

it("without the library waits for the page before choosing a Pages address", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.loading = true
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.replace).not.toHaveBeenCalled()
})

// Restricted member (#5132): /artifacts is their restricted catalog with or
// without the library. It never mounts the library, the detail or Design,
// reads no page and redirects nowhere. An ID address (#5135) is matched by ID
// against the same catalog; the catalog alone decides between actions and gate.
describe.each(["true", "false"])("a restricted member with the library %s", library => {
  it.each([
    ["the library", "/artifacts", "workspace_id=ws1", "slug", ""],
    ["a slug link", "/artifacts", "workspace_id=ws1&item=site", "slug", "site"],
    ["a shared Design", "/artifacts", "workspace_id=ws1&design=1&item=wireframe", "slug", ""],
    ["an artifact by ID", "/artifacts/page-site", "workspace_id=ws1", "id", "page-site"],
    ["a pinned artifact by ID", "/artifacts/page-site", "workspace_id=ws1&version=2", "id", "page-site"],
    ["the exported placeholder", "/artifacts/_", "workspace_id=ws1", "slug", ""],
  ])("at %s sees only the restricted catalog", (_name, path, query, by, focus) => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", library)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true")
    nav.access = "restricted"
    nav.params = new URLSearchParams(query)
    at(path)
    render(path === "/artifacts" ? <ArtifactsPage /> : <ArtifactRoute />)
    expect(screen.getByTestId("restricted-catalog").dataset).toMatchObject({ focus: focus || "none", by, open: "/artifacts?workspace_id=ws1&item=brief", home: "/artifacts?workspace_id=ws1" })
    expect(nav.catalog).toHaveBeenCalledWith("ws1", focus || null, by === "id")
    expect(screen.queryByTestId("library")).toBeNull()
    expect(screen.queryByTestId("design")).toBeNull()
    expect(nav.read).not.toHaveBeenCalled()
    expect(nav.replace).not.toHaveBeenCalled()
    expect(nav.push).not.toHaveBeenCalled()
  })
})

it("decides nothing and reads nothing before the access mode is known", () => {
  nav.access = "loading"
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(screen.queryByTestId("library")).toBeNull()
  expect(screen.queryByTestId("restricted-catalog")).toBeNull()
  expect(nav.read).not.toHaveBeenCalled()
  expect(nav.replace).not.toHaveBeenCalled()
})

it("without the library refuses an unavailable workspace link instead of moving it to Pages", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.linkUnavailable = true
  nav.params = new URLSearchParams("workspace_id=ws1&workspace_id=ws2")
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByRole("alert")).toHaveTextContent("Artifacts unavailable.")
})

describe("the editor inside the frame (?edit=1)", () => {
  it("opens the open artifact's editor from its address, never on the library itself", () => {
    at("/artifacts/page-site")
    nav.params = new URLSearchParams("workspace_id=ws1&edit=1")
    render(<ArtifactRoute />)
    expect(screen.getByTestId("library").dataset).toMatchObject({ item: "page-site", edit: "true" })
    cleanup()
    at("/artifacts")
    render(<ArtifactsPage />)
    expect(screen.getByTestId("library").dataset.edit).toBe("false")
  })

  it("keeps edit=1 in step with the frame, replacing the entry", () => {
    at("/artifacts/page-site")
    nav.params = new URLSearchParams("workspace_id=ws1&version=2")
    render(<ArtifactRoute />)
    fireEvent.click(screen.getByText("start editing"))
    expect(nav.replace).toHaveBeenLastCalledWith("/artifacts/page-site?workspace_id=ws1&version=2&edit=1")
    cleanup()
    nav.params = new URLSearchParams("workspace_id=ws1&version=2&edit=1")
    render(<ArtifactRoute />)
    fireEvent.click(screen.getByText("stop editing"))
    expect(nav.replace).toHaveBeenLastCalledWith("/artifacts/page-site?workspace_id=ws1&version=2")
    expect(nav.push).not.toHaveBeenCalled()
  })

  it("keeps edit=1 when a slug link moves to the ID address", () => {
    nav.params = new URLSearchParams("workspace_id=ws1&item=brief&edit=1")
    render(<ArtifactsPage />)
    fireEvent.click(screen.getByText("resolved"))
    expect(nav.replace).toHaveBeenLastCalledWith("/artifacts/page-brief?workspace_id=ws1&edit=1")
  })
})
