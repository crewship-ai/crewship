import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

const state = vi.hoisted(() => ({ pages: [] as PageView[], loading: false, error: null as string | null, refresh: vi.fn(), projectsOn: true, item: null as string | null, select: vi.fn() }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: () => ({ pages: state.pages, loading: state.loading, error: state.error, refresh: state.refresh }),
}))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => state.projectsOn }))
vi.mock("@/components/features/chat/artifact-context/projects/artifact-viewer-context", () => ({
  ArtifactViewerProvider: ({ children, target }: { children: React.ReactNode; target: { slug: string; tab?: string } }) =>
    <div data-testid="viewer-provider" data-slug={target?.slug} data-tab={target?.tab}>{children}</div>,
}))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-viewer-panel", () => ({ ArtifactViewerPanel: () => <div data-testid="viewer-panel" /> }))
vi.mock("@/components/features/chat/artifact-context/craft/craft-lock-bar", () => ({
  CraftLockBar: ({ slug }: { slug: string }) => <div data-testid="lock-bar" data-slug={slug} />,
  craftEditorHref: (ws: string, slug: string) => `/editor?${ws}&${slug}`,
}))

import { ArtifactsLibrary } from "../artifacts-library"

const wire = (over: Partial<WirePage>): PageView => toPageView({ id: over.slug, owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)
beforeEach(() => {
  state.pages = [
    wire({ slug: "site", id: "page-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, updated_at: "2026-10-27T12:00:00Z" }),
    wire({ slug: "brief", id: "page-brief", name: "Partner brief", artifact_kind: "word", has_document: true }),
    wire({ slug: "model", id: "page-model", name: "Price model", has_workbook: true }),
    wire({ slug: "weekly", id: "page-weekly", name: "Weekly report", panel_count: 2 } as Partial<WirePage>),
    wire({ slug: "calc", id: "page-calc", name: "Calculator", has_application: true } as Partial<WirePage>),
  ]
  state.loading = false; state.error = null; state.projectsOn = true; state.item = null; state.select.mockReset(); state.refresh.mockReset()
})
afterEach(cleanup)

const renderLibrary = () => render(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />)

it("shows a gallery of kinds with a backend only, with kind and shared state", () => {
  renderLibrary()
  const gallery = screen.getByRole("list", { name: "Artifacts" })
  const cards = within(gallery).getAllByRole("listitem")
  expect(cards.map(card => within(card).getByRole("heading").textContent)).toEqual(["Pricing site", "Partner brief", "Price model", "Weekly report"])
  expect(within(cards[0]).getByText("Project")).toBeTruthy()
  expect(within(cards[0]).getByText("Shared v2")).toBeTruthy()
  expect(within(cards[1]).getByText("Not shared")).toBeTruthy()
  expect(within(cards[2]).queryByText(/shared/i)).toBeNull()
  expect(screen.queryByText("Calculator")).toBeNull()
})

it("filters by kind with counts and switches to a table", () => {
  renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: "Projects 1" }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem")).toHaveLength(1)
  fireEvent.click(screen.getByRole("button", { name: "All 4" }))
  fireEvent.click(screen.getByRole("radio", { name: "Table" }))
  const rows = within(screen.getByRole("table")).getAllByRole("row")
  expect(rows).toHaveLength(5)
  expect(within(rows[1]).getByText("v2")).toBeTruthy()
})

it("opening a card selects it by slug", () => {
  renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: /Open Partner brief/ }))
  expect(state.select).toHaveBeenCalledWith("brief")
})

it("a project detail opens the project preview", () => {
  state.item = "site"
  renderLibrary()
  expect(screen.getByRole("heading", { level: 1, name: "Pricing site" })).toBeTruthy()
  expect(screen.getByTestId("viewer-provider").dataset).toMatchObject({ slug: "site", tab: "preview" })
  expect(screen.getByTestId("viewer-panel")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "All artifacts" }))
  expect(state.select).toHaveBeenCalledWith(null)
})

it("without the project surface the detail links to the Page instead of a dead viewer", () => {
  state.projectsOn = false
  state.item = "site"
  renderLibrary()
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
  expect(screen.getByRole("link", { name: "Open in Pages" }).getAttribute("href")).toBe("/pages/%40page-site?workspace_id=ws1&artifact=1")
})

it("a native Word detail shows its edit lock by Page reference", () => {
  state.item = "brief"
  renderLibrary()
  expect(screen.getByTestId("lock-bar").dataset.slug).toBe("@page-brief")
})

it("other kinds open in Pages", () => {
  state.item = "weekly"
  renderLibrary()
  expect(screen.queryByTestId("lock-bar")).toBeNull()
  expect(screen.getByRole("link", { name: "Open in Pages" }).getAttribute("href")).toBe("/pages/weekly?workspace_id=ws1")
})

it("an unknown or removed item falls back to the gallery", () => {
  state.item = "gone"
  renderLibrary()
  expect(screen.getByText("This artifact is not available.")).toBeTruthy()
})

it("empty, loading and error states say what to do", () => {
  state.pages = []
  const { rerender } = renderLibrary()
  expect(screen.getByText(/No artifacts yet/)).toBeTruthy()
  state.loading = true
  rerender(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} />)
  expect(screen.getByLabelText("Loading artifacts")).toBeTruthy()
  state.loading = false; state.error = "boom"
  rerender(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} />)
  fireEvent.click(screen.getByRole("button", { name: "Try again" }))
  expect(state.refresh).toHaveBeenCalled()
})

// Availability is false while loading and after a denied capability probe. It
// must never turn a server-typed artifact into a legacy panels link.
it.each(["project", "dashboard", "slides"] as const)("keeps %s routed through its backend across gallery, table and fallback detail", artifact_kind => {
  state.pages = [wire({ id: "owner-page", slug: "ordinary-owner-page", name: "Owner artifact", artifact_kind })]
  state.projectsOn = false
  const view = renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: "Open Owner artifact" }))
  expect(state.select).toHaveBeenLastCalledWith("ordinary-owner-page")
  fireEvent.click(screen.getByRole("radio", { name: "Table" }))
  fireEvent.click(screen.getByRole("button", { name: "Owner artifact" }))
  expect(state.select).toHaveBeenLastCalledWith("ordinary-owner-page")
  state.item = "ordinary-owner-page"
  view.rerender(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />)
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/%40owner-page?workspace_id=ws1&artifact=1")
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
})

it.each(["word", "sheet"] as const)("preserves the existing reader link for native %s", artifact_kind => {
  state.pages = [wire({ id: "native-page", slug: "saved-native", name: "Native document", artifact_kind })]
  state.projectsOn = false; state.item = "saved-native"
  renderLibrary()
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/saved-native?workspace_id=ws1")
})

it("keeps legacy Slides in their existing Page reader", () => {
  state.pages = [wire({ id: "legacy-page", slug: "legacy-slides", name: "Legacy deck", has_slides: true })]
  state.item = "legacy-slides"
  renderLibrary()
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/legacy-slides?workspace_id=ws1")
})
