import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

const state = vi.hoisted(() => ({ pages: [] as PageView[], loading: false, error: null as string | null, refresh: vi.fn(), projectsOn: true, item: null as string | null, select: vi.fn(), folders: [] as { id: string; slug: string; name: string; pageCount: number }[], reads: [] as { target: { pageId: string } | null; enabled: boolean; chatId: string }[], readStatus: "ready" as string, pagesArgs: [] as unknown[], mobile: false }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: (...args: unknown[]) => { state.pagesArgs = args; return { pages: state.pages, loading: state.loading, error: state.error, refresh: state.refresh } },
  usePageCatalogWindow: (_ws: string, params: URLSearchParams | null, offset: number, limit: number) => {
    windowState.calls.push({ params: params?.toString() ?? null, offset, limit })
    return { pages: params ? windowState.pages : [], total: params ? windowState.total : 0, loading: false, error: null }
  },
}))
const windowState = vi.hoisted(() => ({ calls: [] as { params: string | null; offset: number; limit: number }[], pages: [] as PageView[], total: 0 }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => state.projectsOn }))
vi.mock("@/components/features/chat/artifact-context/projects/artifact-viewer-context", () => ({
  ArtifactViewerProvider: ({ children, target }: { children: React.ReactNode; target: { slug: string; tab?: string } }) =>
    <div data-testid="viewer-provider" data-slug={target?.slug} data-tab={target?.tab}>{children}</div>,
}))
vi.mock("@/components/features/chat/artifact-context/viewer/artifact-viewer-panel", () => ({ ArtifactViewerPanel: () => <div data-testid="viewer-panel" /> }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => state.mobile }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "u1" } } }) }))
vi.mock("@/hooks/use-document-viewer-read", () => ({
  useDocumentViewerRead: (input: { target: { pageId: string } | null; enabled: boolean; chatId: string }) => {
    state.reads.push(input)
    return { scopeKey: "k", refresh: vi.fn(), read: input.enabled && state.readStatus !== "ready" ? { status: state.readStatus } : input.enabled ? { status: "ready", title: "Partner brief", content: { schema: 3, rev: 2, content: { type: "doc" } } } : { status: "unavailable" } }
  },
}))
vi.mock("@/components/features/pages/document-readonly/document-readonly", () => ({
  DocumentReadonly: ({ accessibleLabel }: { accessibleLabel: string }) => <div role="region" aria-label={accessibleLabel}>document body</div>,
}))
// Per-user preferences as plain state; the hook's server sync is its own test.
vi.mock("@/hooks/use-user-preference", async () => {
  const { useState } = await import("react")
  return { useUserPreference: <T,>(_key: string, initial: T) => { const [value, set] = useState<T>(initial); return [value, set, { ready: true }] as const } }
})
vi.mock("@/hooks/use-page-folders", () => ({ usePageFolders: () => ({ folders: state.folders, loading: false, error: null, supported: true, refresh: vi.fn(), reread: vi.fn() }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({
  useArtifactCapabilities: () => ({ projects: true, dashboards: false, slides: false, documents: true, sheets: false, pages: true }),
}))
vi.mock("../new-artifact-dialog", () => ({
  NewArtifactDialog: ({ defaultKind, defaultFolderSlug, onCreated }: { defaultKind?: string; defaultFolderSlug?: string; onCreated: (slug: string) => void }) =>
    <div role="dialog" aria-label="New artifact" data-kind={defaultKind ?? ""} data-folder={defaultFolderSlug ?? ""}>
      <button type="button" onClick={() => onCreated("fresh-design")}>Create</button>
    </div>,
}))
vi.mock("@/components/layout/secondary-panel", async importOriginal => ({ ...(await importOriginal<typeof import("@/components/layout/secondary-panel")>()), SecondaryPanel: ({ label, children }: { label: string; children: React.ReactNode }) => <aside aria-label={label}>{children}</aside> }))
vi.mock("@/components/features/chat/artifact-context/craft/craft-lock-bar", () => ({
  CraftLockBar: ({ slug }: { slug: string }) => <div data-testid="lock-bar" data-slug={slug} />,
  craftEditorHref: (ws: string, slug: string) => `/editor?${ws}&${slug}`,
}))

vi.mock("../page-content", () => ({ ArtifactPageContent: ({ pageId }: { pageId: string }) => <div data-testid="page-content" data-page={pageId} /> }))
vi.mock("../sharing/share-dialog", () => ({ ShareDialog: ({ item }: { item: { name: string } }) => <div role="dialog" aria-label={`Share ${item.name}`} /> }))
vi.mock("../sharing/access-tab", () => ({ AccessTab: ({ item }: { item: { name: string } }) => <section aria-label={`Access to ${item.name}`} /> }))
vi.mock("../sharing/folder-settings-dialog", () => ({ FolderSettingsDialog: ({ folder }: { folder: { name: string } }) => <div role="dialog" aria-label={`Settings for ${folder.name}`} /> }))
vi.mock("../sharing/bulk-actions", () => ({
  BulkBar: ({ selected, archivedView }: { selected: { slug: string }[]; archivedView?: boolean }) =>
    <div role="toolbar" aria-label="Bulk actions" data-slugs={selected.map(item => item.slug).join(",")} data-archived={String(Boolean(archivedView))} />,
  MoveToFolderDialog: ({ item }: { item: { name: string } }) => <div role="dialog" aria-label={`Move ${item.name}`} />,
}))

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ArtifactsLibrary } from "../artifacts-library"
let client = new QueryClient()
beforeEach(() => { client = new QueryClient({ defaultOptions: { queries: { retry: false } } }) })
const withQuery = (ui: React.ReactElement) => <QueryClientProvider client={client}>{ui}</QueryClientProvider>

const wire = (over: Partial<WirePage>): PageView => toPageView({ id: over.slug, owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)
beforeEach(() => {
  state.pages = [
    wire({ slug: "site", id: "page-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, updated_at: "2026-10-27T12:00:00Z" }),
    wire({ slug: "brief", id: "page-brief", name: "Partner brief", artifact_kind: "word", has_document: true }),
    wire({ slug: "model", id: "page-model", name: "Price model", has_workbook: true }),
    wire({ slug: "weekly", id: "page-weekly", name: "Weekly report", panel_count: 2 } as Partial<WirePage>),
    wire({ slug: "calc", id: "page-calc", name: "Calculator", has_application: true } as Partial<WirePage>),
  ]
  windowState.calls = []; windowState.pages = []; windowState.total = 0
  state.folders = []; state.reads = []; state.pagesArgs = []; state.mobile = false; state.readStatus = "ready"; state.loading = false; state.error = null; state.projectsOn = true; state.item = null; state.select.mockReset(); state.refresh.mockReset()
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

const renderLibrary = () => render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))

const openAll = () => fireEvent.click(screen.getByRole("tab", { name: /All artifacts/ }))

it("lists every kind with something in it, a panel Page as Page, and keeps empty items in their own view", () => {
  renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("radio", { name: "Gallery" }))
  const cards = within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem")
  expect(cards.map(card => within(card).getByRole("heading").textContent)).toEqual(["Pricing site", "Partner brief", "Price model", "Weekly report"])
  expect(within(cards[0]).getByText("Project")).toBeTruthy()
  expect(within(cards[0]).getByText("Shared v2")).toBeTruthy()
  expect(within(cards[1]).getByText("Not shared")).toBeTruthy()
  expect(within(cards[2]).queryByText(/shared/i)).toBeNull()
  expect(within(cards[3]).getByText("Page")).toBeTruthy()
  expect(screen.queryByText("Calculator")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: /Empty/ }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(item => item.textContent)).toEqual([expect.stringContaining("Calculator")])
})

it("without the server catalog flag, All filters the full list in the browser and never asks for a window", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  renderLibrary()
  openAll()
  expect(screen.getAllByText("Pricing site").length).toBeGreaterThan(0)
  expect(windowState.calls.length).toBeGreaterThan(0)
  expect(windowState.calls.every(call => call.params === null)).toBe(true)
  expect(screen.queryByRole("navigation", { name: "Artifact pages" })).toBeNull()
})

it("with the server catalog flag, All shows the server's window, its total and pages through it", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_SERVER_CATALOG", "true")
  windowState.pages = [wire({ slug: "server-only", id: "page-server-only", name: "Server window row", artifact_kind: "project" })]
  windowState.total = 120
  renderLibrary()
  openAll()
  expect(screen.getAllByText("Server window row").length).toBeGreaterThan(0)
  expect(screen.queryByText("Partner brief")).toBeNull()
  const last = () => windowState.calls[windowState.calls.length - 1]
  expect(new URLSearchParams(last().params!).get("empty")).toBe("exclude")
  expect(last()).toMatchObject({ offset: 0, limit: 50 })
  const pager = screen.getByRole("navigation", { name: "Artifact pages" })
  expect(within(pager).getByText("1–1 of 120")).toBeTruthy()
  fireEvent.click(within(pager).getByRole("button", { name: "Next" }))
  expect(last()).toMatchObject({ offset: 50, limit: 50 })
})

it("asks the server for archived and test-made items, which it leaves out by default", () => {
  renderLibrary()
  // Without them the Archive and Test items views stay empty against the real API (#4444).
  expect(state.pagesArgs).toEqual(["ws1", undefined, { archived: "include", testItems: "include" }])
})

it("Home offers what can be made, what is not shared yet and what changed last, each once", () => {
  renderLibrary()
  expect(screen.getByRole("heading", { level: 1, name: "Artifacts" })).toBeTruthy()
  expect(screen.getByRole("button", { name: "New Project" })).toBeTruthy()
  expect(screen.getByRole("button", { name: "New Word document" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: "New Slides" })).toBeNull()
  expect(screen.getAllByRole("button", { name: "Open Partner brief" })).toHaveLength(1)
  fireEvent.click(screen.getByRole("button", { name: "New Word document" }))
  expect(screen.getByRole("dialog", { name: "New artifact" }).dataset.kind).toBe("word")
})

it("New artifact in the bar opens the dialog in the folder being looked at", () => {
  state.folders = [{ id: "f1", slug: "sales", name: "Sales", pageCount: 1 }]
  state.pages = [wire({ slug: "site", name: "Pricing site", artifact_kind: "project", folder: { slug: "sales", name: "Sales" } } as Partial<WirePage>)]
  renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: /Sales/ }))
  fireEvent.click(screen.getAllByRole("button", { name: "New artifact" })[0])
  const dialog = screen.getByRole("dialog", { name: "New artifact" })
  expect([dialog.dataset.kind, dialog.dataset.folder]).toEqual(["", "sales"])
})

it("a just-created artifact opens by its slug, never as an ID, so it does not land on not available", () => {
  renderLibrary()
  fireEvent.click(screen.getAllByRole("button", { name: "New artifact" })[0])
  fireEvent.click(screen.getByRole("button", { name: "Create" }))
  // The new item is not in the list yet; the route opens it by slug and moves to its ID once it resolves.
  expect(state.select).toHaveBeenCalledWith(null, "fresh-design")
  expect(state.select).not.toHaveBeenCalledWith("fresh-design")
})

it("searches with operators, filters by kind with counts and switches to a table", () => {
  renderLibrary()
  fireEvent.change(screen.getByRole("textbox", { name: "Search artifacts" }), { target: { value: "kind:project" } })
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem")).toHaveLength(1)
  fireEvent.change(screen.getByRole("textbox", { name: "Search artifacts" }), { target: { value: "" } })
  fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
  fireEvent.click(screen.getByRole("button", { name: /^Kind/, expanded: false }))
  fireEvent.click(screen.getByRole("button", { name: /Sheets/ }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(item => item.textContent)).toEqual([expect.stringContaining("Price model")])
  fireEvent.click(screen.getByRole("button", { name: "Clear all filters" }))
  fireEvent.click(screen.getByRole("radio", { name: "Table" }))
  const rows = within(screen.getByRole("table")).getAllByRole("row")
  expect(rows).toHaveLength(5)
  expect(within(rows[1]).getByText("v2")).toBeTruthy()
})

const peek = () => screen.getByRole("complementary", { name: "Preview" })
const peekOpen = () => fireEvent.click(within(peek()).getByRole("button", { name: /^Open$/ }))

it("a click on desktop peeks at the item on the right and the list stays with its filter", () => {
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} filters={{ view: "all", q: "", kinds: ["word", "sheet"], access: [], version: [], owners: [], folder: null }} />))
  fireEvent.click(screen.getByRole("button", { name: "Open Partner brief" }))
  expect(state.select).not.toHaveBeenCalled()
  expect(within(peek()).getByRole("heading", { name: "Partner brief" })).toBeTruthy()
  // The facts the prototype lists: kind, owner, folder, version, edited, who can open.
  const facts = within(peek()).getAllByRole("term").map(term => term.textContent)
  expect(facts).toEqual(["Kind", "Version", "Folder", "Who can open", "Owner", "Edited"])
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(row => row.textContent)).toEqual([
    expect.stringContaining("Partner brief"), expect.stringContaining("Price model"),
  ])
  expect(screen.getByRole("button", { name: "Open Partner brief" }).getAttribute("aria-current")).toBe("true")
  peekOpen()
  expect(state.select).toHaveBeenCalledWith("page-brief")
})

it("the peek shares and stars the item it shows, and closes", () => {
  renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("button", { name: "Open Price model" }))
  fireEvent.click(within(peek()).getByRole("button", { name: "Share" }))
  expect(screen.getByRole("dialog", { name: "Share Price model" })).toBeTruthy()
  fireEvent.click(within(peek()).getByRole("button", { name: "Add to favorites" }))
  expect(within(peek()).getByRole("button", { name: "Remove from favorites" })).toBeTruthy()
  fireEvent.click(within(peek()).getByRole("button", { name: "Close preview" }))
  expect(screen.queryByRole("complementary", { name: "Preview" })).toBeNull()
})

it("J and K move the peek, Enter opens the peeked item, Escape closes the peek", () => {
  renderLibrary()
  openAll()
  fireEvent.keyDown(window, { key: "j" })
  expect(within(peek()).getByRole("heading", { name: "Pricing site" })).toBeTruthy()
  fireEvent.keyDown(window, { key: "j" })
  expect(within(peek()).getByRole("heading", { name: "Partner brief" })).toBeTruthy()
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Open Partner brief")
  fireEvent.keyDown(window, { key: "k" })
  expect(within(peek()).getByRole("heading", { name: "Pricing site" })).toBeTruthy()
  fireEvent.keyDown(document.body, { key: "Escape" })
  expect(screen.queryByRole("complementary", { name: "Preview" })).toBeNull()
  expect(state.select).not.toHaveBeenCalled()
  expect(screen.getByRole("list", { name: "Artifacts" })).toBeTruthy()
  fireEvent.keyDown(window, { key: "j" })
  expect(within(peek()).getByRole("heading", { name: "Partner brief" })).toBeTruthy()
  fireEvent.keyDown(document.activeElement as HTMLElement, { key: "Enter" })
  expect(state.select).toHaveBeenLastCalledWith("page-brief")
})

it("on a phone a click opens the detail directly, with no peek", () => {
  state.mobile = true
  renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("button", { name: "Open Partner brief" }))
  expect(state.select).toHaveBeenCalledWith("page-brief")
  expect(screen.queryByRole("complementary", { name: "Preview" })).toBeNull()
})

it("phone rows show checkboxes only after Select", () => {
  state.mobile = true
  renderLibrary()
  openAll()
  expect(screen.queryByRole("checkbox", { name: "Select Partner brief" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: /^Select$/ }))
  fireEvent.click(screen.getByRole("checkbox", { name: "Select Partner brief" }))
  expect(screen.getByRole("toolbar", { name: "Bulk actions" })).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Done" }))
  expect(screen.queryByRole("checkbox", { name: "Select Partner brief" })).toBeNull()
  expect(screen.queryByRole("toolbar", { name: "Bulk actions" })).toBeNull()
})

it("desktop rows keep their checkboxes without a Select toggle", () => {
  renderLibrary()
  openAll()
  expect(screen.getByRole("checkbox", { name: "Select Partner brief" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: /^Select$/ })).toBeNull()
})

it("a project detail opens the project preview", () => {
  state.item = "page-site"
  renderLibrary()
  // The viewer names it; the library does not repeat the title above it.
  expect(screen.queryByRole("heading", { level: 1, name: "Pricing site" })).toBeNull()
  expect(screen.getByText(/^You · /)).toBeTruthy()
  expect(screen.getByTestId("viewer-provider").dataset).toMatchObject({ slug: "site", tab: "preview" })
  expect(screen.getByTestId("viewer-panel")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "All artifacts" }))
  expect(state.select).toHaveBeenCalledWith(null)
})

it("without the project surface the detail links to the Page instead of a dead viewer", () => {
  state.projectsOn = false
  state.item = "page-site"
  renderLibrary()
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
  expect(screen.getByRole("link", { name: "Open in Pages" }).getAttribute("href")).toBe("/pages/%40page-site?workspace_id=ws1&artifact=1")
})

it("a native Word detail shows its edit lock by Page reference", () => {
  state.item = "page-brief"
  renderLibrary()
  expect(screen.getByTestId("lock-bar").dataset.slug).toBe("@page-brief")
})

it("a Word detail shows the saved document read-only, never an editor", () => {
  state.item = "page-brief"
  renderLibrary()
  expect(screen.getByRole("region", { name: "Partner brief, saved version" })).toBeTruthy()
  expect(state.reads.at(-1)).toMatchObject({ enabled: true, target: { pageId: "page-brief", type: "document", mode: "latest", workspaceId: "ws1" } })
})

it.each(["unavailable", "error"])("an old-editor Word that answers %s says so plainly, and the list still works", status => {
  state.pages.push(wire({ slug: "old", id: "page-old", name: "Old contract", has_document: true }))
  state.readStatus = status
  state.item = "page-old"
  renderLibrary()
  expect(screen.getByRole("status")).toHaveTextContent("This document is from an older version of the editor and does not open here.")
  expect(screen.getByRole("status").textContent).not.toMatch(/engine|503|service/i)
  // Nothing next to it sends the reader to an editor that cannot open it.
  expect(screen.queryByText(/Edit it in its editor/)).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: /All artifacts/ }))
  expect(state.select).toHaveBeenCalledWith(null)
})

it("a native Word whose service is down says the service is down", () => {
  state.readStatus = "unavailable"
  state.item = "page-brief"
  renderLibrary()
  expect(screen.getByRole("status")).toHaveTextContent("The document service is not answering right now")
})

it("other kinds do not read a document", () => {
  state.item = "page-model"
  renderLibrary()
  expect(screen.queryByRole("region", { name: /saved version/ })).toBeNull()
  expect(state.reads.every(read => !read.enabled)).toBe(true)
})

it("a panel Page shows its content in the detail, never a way out to Pages", () => {
  state.item = "page-weekly"
  renderLibrary()
  expect(screen.queryByTestId("lock-bar")).toBeNull()
  expect(screen.getByTestId("page-content").dataset.page).toBe("page-weekly")
  expect(screen.queryByRole("link", { name: /Open page|Open in Pages/ })).toBeNull()
})

it("an unknown or removed item falls back to the gallery", () => {
  state.item = "page-gone"
  renderLibrary()
  expect(screen.getByText("This artifact is not available.")).toBeTruthy()
})

it("an ID address fails closed: another artifact now holding that slug never opens in its place", () => {
  state.pages = [wire({ id: "different-page-id", slug: "page-site", name: "Replacement", artifact_kind: "project" })]
  state.item = "page-site"
  renderLibrary()
  expect(screen.getByText("This artifact is not available.")).toBeTruthy()
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
})

it("a link from before IDs opens by slug and hands the route the artifact's ID", () => {
  const canonical = vi.fn()
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item="site" bySlug onSelect={state.select} onCanonical={canonical} />))
  expect(screen.getByTestId("viewer-provider").dataset.slug).toBe("site")
  expect(canonical).toHaveBeenCalledWith("page-site")
  cleanup()
  // A slug never stands in for an ID, and an ID never matches as a slug.
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item="page-site" bySlug onSelect={state.select} onCanonical={canonical} />))
  expect(screen.getByText("This artifact is not available.")).toBeTruthy()
})

it("empty, loading and error states say what to do", () => {
  state.pages = []
  const { rerender } = renderLibrary()
  expect(screen.getByText(/No artifacts yet/)).toBeTruthy()
  state.loading = true
  rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} />))
  expect(screen.getByLabelText("Loading artifacts")).toBeTruthy()
  state.loading = false; state.error = "boom"
  rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} />))
  fireEvent.click(screen.getByRole("button", { name: "Try again" }))
  expect(state.refresh).toHaveBeenCalled()
})

it("an explicitly typed Dashboard opens the versioned viewer without a Craft lease", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "true")
  // The backend-owned Page projection adds this typed kind; legacy panel dashboards stay in Pages.
  state.pages = [{ ...wire({ slug: "sales", name: "Sales", artifact_shared_version: 2 }), artifactKind: "dashboard" } as unknown as PageView]
  state.item = "sales"
  renderLibrary()
  expect(screen.getByTestId("viewer-panel")).toBeTruthy()
  // The viewer's Latest / Shared switch says which version is shared; the library does not repeat it.
  expect(screen.queryByText("Shared v2")).toBeNull()
  expect(screen.queryByTestId("lock-bar")).toBeNull()
})

// Availability is false while loading and after a denied capability probe. It
// must never turn a server-typed artifact into a legacy panels link.
it.each(["project", "dashboard", "slides"] as const)("keeps %s routed through its backend across gallery, table and fallback detail", artifact_kind => {
  state.pages = [wire({ id: "owner-page", slug: "ordinary-owner-page", name: "Owner artifact", artifact_kind })]
  state.projectsOn = false
  const view = renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("button", { name: "Open Owner artifact" }))
  peekOpen()
  expect(state.select).toHaveBeenLastCalledWith("owner-page")
  state.select.mockReset()
  fireEvent.click(screen.getByRole("radio", { name: "Table" }))
  fireEvent.keyDown(document.body, { key: "Escape" })
  fireEvent.click(screen.getByRole("button", { name: "Owner artifact" }))
  peekOpen()
  expect(state.select).toHaveBeenLastCalledWith("owner-page")
  state.item = "owner-page"
  view.rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/%40owner-page?workspace_id=ws1&artifact=1")
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
})

it.each(["word", "sheet"] as const)("preserves the existing reader link for native %s", artifact_kind => {
  state.pages = [wire({ id: "native-page", slug: "saved-native", name: "Native document", artifact_kind })]
  state.projectsOn = false; state.item = "native-page"
  renderLibrary()
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/saved-native?workspace_id=ws1")
})

it("keeps legacy Slides in their existing Page reader", () => {
  state.pages = [wire({ id: "legacy-page", slug: "legacy-slides", name: "Legacy deck", has_slides: true })]
  state.item = "legacy-page"
  renderLibrary()
  expect(screen.getByRole("link", { name: "Open in Pages" })).toHaveAttribute("href", "/pages/legacy-slides?workspace_id=ws1")
})

it("J and K move between items, Enter opens, Escape leaves the detail, never from a field", () => {
  const view = renderLibrary()
  openAll()
  fireEvent.keyDown(window, { key: "j" })
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Open Pricing site")
  fireEvent.keyDown(window, { key: "j" })
  fireEvent.keyDown(window, { key: "j" })
  fireEvent.keyDown(window, { key: "k" })
  expect(document.activeElement?.getAttribute("aria-label")).toBe("Open Partner brief")
  // The peeked row's second click opens it, as Enter does.
  fireEvent.click(document.activeElement as HTMLElement)
  expect(state.select).toHaveBeenLastCalledWith("page-brief")
  const search = screen.getByRole("textbox", { name: "Search artifacts" })
  search.focus()
  fireEvent.keyDown(search, { key: "j" })
  expect(document.activeElement).toBe(search)
  state.item = "page-brief"
  view.rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))
  fireEvent.keyDown(document.body, { key: "Escape" })
  expect(state.select).toHaveBeenLastCalledWith(null)
})

it("remembers what was opened and starred, per person, in Recently opened and Favorites", () => {
  state.item = "page-brief"
  const view = renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }))
  expect(screen.getByRole("button", { name: "Remove from favorites" }).getAttribute("aria-pressed")).toBe("true")
  state.item = null
  view.rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))
  expect(screen.getByRole("heading", { name: /Recently opened/ })).toBeTruthy()
  fireEvent.click(screen.getByRole("tab", { name: /Recently opened/ }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(item => item.textContent)).toEqual([expect.stringContaining("Partner brief")])
  fireEvent.click(screen.getByRole("button", { name: /^Favorites/ }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(item => item.textContent)).toEqual([expect.stringContaining("Partner brief")])
})

it("each kind wears its colour and the gallery draws a thumbnail per kind", () => {
  renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("radio", { name: "Gallery" }))
  const thumbs = [...document.querySelectorAll("[data-thumb]")].map(node => node.getAttribute("data-thumb"))
  expect(thumbs).toEqual(["project", "word", "sheet", "page"])
})

it("selecting rows in the list opens the bulk bar with exactly those items", () => {
  state.item = null
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} filters={{ view: "all", q: "", kinds: [], access: [], version: [], owners: [], folder: null }} />))
  expect(screen.queryByRole("toolbar", { name: "Bulk actions" })).toBeNull()
  fireEvent.click(screen.getByRole("checkbox", { name: "Select Partner brief" }))
  fireEvent.click(screen.getByRole("checkbox", { name: "Select Pricing site" }))
  expect(screen.getByRole("toolbar", { name: "Bulk actions" }).dataset.slugs).toBe("site,brief")
  fireEvent.click(screen.getByRole("checkbox", { name: "Select Pricing site" }))
  expect(screen.getByRole("toolbar", { name: "Bulk actions" }).dataset.slugs).toBe("brief")
})

it("a detail offers Share, Move and an Access tab", () => {
  state.item = "page-model"
  renderLibrary()
  fireEvent.click(screen.getByRole("radio", { name: "Access" }))
  expect(screen.getByRole("region", { name: "Access to Price model" })).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  expect(screen.getByRole("dialog", { name: "Share Price model" })).toBeTruthy()
})

it("a chosen folder has its settings", () => {
  state.folders = [{ id: "f1", slug: "sales", name: "Sales", pageCount: 1 }]
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} filters={{ view: "all", q: "", kinds: [], access: [], version: [], owners: [], folder: "sales" }} />))
  fireEvent.click(screen.getByRole("button", { name: "Folder settings" }))
  expect(screen.getByRole("dialog", { name: "Settings for Sales" })).toBeTruthy()
})
