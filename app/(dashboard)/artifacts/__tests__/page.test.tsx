import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ params: new URLSearchParams("workspace_id=ws1"), id: undefined as string | undefined, replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useParams: () => ({ id: nav.id }), useRouter: () => ({ replace: nav.replace, push: nav.push }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/components/features/artifacts-library/artifacts-library", () => ({
  ArtifactsLibrary: ({ workspaceId, item, onSelect, filters, onFiltersChange }: {
    workspaceId: string; item: string | null; onSelect: (slug: string | null) => void
    filters: { view: string; q: string }; onFiltersChange: (next: unknown) => void
  }) =>
    <div data-testid="library" data-ws={workspaceId} data-item={item ?? ""} data-view={filters.view} data-q={filters.q}>
      <button onClick={() => onSelect("page-site")}>pick</button><button onClick={() => onSelect(null)}>back</button>
      <button onClick={() => onFiltersChange({ ...filters, view: "all", q: "kind:word" })}>search</button>
    </div>,
}))

import ArtifactsPage from "../page"
import ArtifactRoute from "../[id]/page"

beforeEach(() => { nav.params = new URLSearchParams("workspace_id=ws1"); nav.id = undefined; nav.replace.mockReset(); nav.push.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true") })
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
  nav.id = "page-site"
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ ws: "ws1", item: "page-site" })
  fireEvent.click(screen.getByText("back"))
  expect(nav.push).toHaveBeenLastCalledWith("/artifacts?workspace_id=ws1")
})

it("reads the exported placeholder as the library and an encoded ID as itself", () => {
  nav.id = "_"
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset.item).toBe("")
  cleanup()
  nav.id = "page%20a"
  render(<ArtifactRoute />)
  expect(screen.getByTestId("library").dataset.item).toBe("page a")
})

it("moves a link from before IDs, /artifacts?item=<slug>, to the artifact's address", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&view=all&item=brief")
  render(<ArtifactsPage />)
  expect(screen.queryByTestId("library")).toBeNull()
  expect(nav.replace).toHaveBeenCalledWith("/artifacts/brief?workspace_id=ws1&view=all")
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
