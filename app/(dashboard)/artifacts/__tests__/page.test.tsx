import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ linkUnavailable: false, params: new URLSearchParams("workspace_id=ws1"), replace: vi.fn(), push: vi.fn() }))
// Like the static export: every ID is served the "_" shell and useParams() keeps returning it.
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useParams: () => ({ id: "_" }), usePathname: () => window.location.pathname, useRouter: () => ({ replace: nav.replace, push: nav.push }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: nav.linkUnavailable }) }))
vi.mock("@/components/features/artifacts-library/artifacts-library", () => ({
  ArtifactsLibrary: ({ workspaceId, item, version, bySlug, onSelect, onCanonical, filters, onFiltersChange }: {
    workspaceId: string; item: string | null; version?: number; bySlug?: boolean; onSelect: (id: string | null, slug?: string) => void
    onCanonical: (id: string) => void; filters: { view: string; q: string }; onFiltersChange: (next: unknown) => void
  }) =>
    <div data-testid="library" data-ws={workspaceId} data-item={item ?? ""} data-version={version ?? ""} data-by-slug={String(Boolean(bySlug))} data-view={filters.view} data-q={filters.q}>
      <button onClick={() => onSelect("page-site")}>pick</button><button onClick={() => onSelect(null)}>back</button>
      <button onClick={() => onSelect(null, "no-id")}>pick without ID</button><button onClick={() => onCanonical("page-brief")}>resolved</button>
      <button onClick={() => onFiltersChange({ ...filters, view: "all", q: "kind:word" })}>search</button>
    </div>,
}))

vi.mock("next/dynamic", () => ({ default: () => ({ slug, initialVersion }: { slug: string; initialVersion?: number }) => <div data-testid="design" data-slug={slug} data-version={initialVersion ?? ""} /> }))

import ArtifactsPage from "../page"
import ArtifactRoute from "../[id]/page"

const at = (path: string) => window.history.replaceState(null, "", path)
beforeEach(() => { nav.linkUnavailable = false; at("/artifacts"); nav.params = new URLSearchParams("workspace_id=ws1"); nav.replace.mockReset(); nav.push.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true") })
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

it("without the library keeps a project link's identity, workspace and pin on Pages", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.params = new URLSearchParams("workspace_id=ws1&version=2")
  at("/artifacts/page-site")
  render(<ArtifactRoute />)
  expect(nav.replace).toHaveBeenCalledWith("/pages/%40page-site?workspace_id=ws1&artifact=1&version=2")
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
