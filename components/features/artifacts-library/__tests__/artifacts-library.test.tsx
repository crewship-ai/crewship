import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

const state = vi.hoisted(() => ({ pages: [] as PageView[], loading: false, error: null as string | null, refresh: vi.fn(), projectsOn: true, item: null as string | null, select: vi.fn(), folders: [] as { id: string; slug: string; name: string; pageCount: number }[], reads: [] as { target: { pageId: string } | null; enabled: boolean; chatId: string }[], readStatus: "ready" as string, pagesArgs: [] as unknown[], mobile: false, caps: {} as Record<string, boolean>, access: "unknown" as string, accessRefs: [] as (string | null)[] }))
// Storage is a no-op in this suite's setup: the drafts this browser kept are listed here instead.
const drafts = vi.hoisted(() => new Set<string>())
vi.mock("@/components/features/chat/artifact-context/craft/local-draft", async importOriginal => ({
  ...(await importOriginal<typeof import("@/components/features/chat/artifact-context/craft/local-draft")>()),
  readLocalDraft: (userId: string, workspaceId: string, slug: string) => drafts.has([userId, workspaceId, slug].join(":")) ? { content: "x", baseVersion: 1, savedAt: "2026-10-27T10:00:00Z" } : null,
}))
vi.mock("../use-artifact-access", () => ({ useArtifactAccess: (_ws: string, ref: string | null, enabled: boolean) => { if (enabled) state.accessRefs.push(ref); return enabled ? state.access : "ok" } }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: (...args: unknown[]) => { state.pagesArgs = args; return { pages: state.pages, loading: state.loading, error: state.error, refresh: state.refresh } },
  usePageCatalogWindow: (_ws: string, params: URLSearchParams | null, offset: number, limit: number) => {
    windowState.calls.push({ params: params?.toString() ?? null, offset, limit })
    return { pages: params ? windowState.pages : [], total: params ? windowState.total : 0, loading: false, error: null }
  },
}))
const windowState = vi.hoisted(() => ({ calls: [] as { params: string | null; offset: number; limit: number }[], pages: [] as PageView[], total: 0 }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => state.projectsOn, useArtifactProjectSnapshot: () => ({ data: undefined, error: null }) }))
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
  useArtifactCapabilities: () => ({ projects: true, dashboards: false, slides: false, documents: true, sheets: false, pages: true, ...state.caps }),
}))
vi.mock("../new-artifact-dialog", () => ({
  NewArtifactDialog: ({ defaultKind, defaultFolderSlug, onCreated }: { defaultKind?: string; defaultFolderSlug?: string; onCreated: (slug: string, id?: string) => void }) =>
    <div role="dialog" aria-label="New artifact" data-kind={defaultKind ?? ""} data-folder={defaultFolderSlug ?? ""}>
      <button type="button" onClick={() => onCreated("fresh-design")}>Create</button>
      <button type="button" onClick={() => onCreated("fresh-sheet", "page-id-1")}>Create with ID</button>
    </div>,
}))
vi.mock("@/components/layout/secondary-panel", async importOriginal => ({ ...(await importOriginal<typeof import("@/components/layout/secondary-panel")>()), SecondaryPanel: ({ label, children }: { label: string; children: React.ReactNode }) => <aside aria-label={label}>{children}</aside> }))
vi.mock("@/components/features/chat/artifact-context/craft/craft-lock-bar", () => ({
  CraftLockBar: ({ slug }: { slug: string }) => <div data-testid="lock-bar" data-slug={slug} />,
  craftEditorHref: (ws: string, slug: string) => `/editor?${ws}&${slug}`,
}))

vi.mock("../page-content", () => ({ ArtifactPageContent: ({ pageId }: { pageId: string }) => <div data-testid="page-content" data-page={pageId} /> }))
// The bodies Pages used to draw, now hosted in the frame (owner, 8 Nov 2026): stand-ins that show what they were given.
const hosted = vi.hoisted(() => ({ canWrite: false }))
vi.mock("@/components/features/pages/artifact-project", async () => {
  const { useEffect } = await import("react")
  return { ArtifactProject: ({ slug, initialVersion, hosted: host }: { slug: string; initialVersion?: number; hosted?: { readOnly: boolean; versionHref: (v: number) => string; onCanWrite?: (w: boolean) => void } }) => {
    useEffect(() => { host?.onCanWrite?.(hosted.canWrite) }, [host])
    return <div data-testid="project-view" data-slug={slug} data-version={initialVersion ?? ""} data-read-only={String(host?.readOnly)} data-shared-href={host?.versionHref(2)} />
  } }
})
vi.mock("../edit/frame-page-editor", () => ({ FramePageEditor: ({ pageId, onLeave }: { pageId: string; onLeave: (reason: "done" | "deleted") => void }) =>
  <div data-testid="page-editor" data-page={pageId}><button type="button" onClick={() => onLeave("done")}>Back to view</button></div> }))
vi.mock("../kinds/legacy/legacy-views", () => ({
  LegacyDeckBody: ({ pageId }: { pageId: string }) => <div data-testid="legacy-deck" data-page={pageId} />,
  LegacyWorkbookBody: ({ pageId }: { pageId: string }) => <div data-testid="legacy-workbook" data-page={pageId} />,
}))
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
  state.folders = []; state.reads = []; state.pagesArgs = []; state.mobile = false; state.readStatus = "ready"; state.loading = false; state.error = null; state.projectsOn = true; state.item = null; state.caps = {}; state.access = "unknown"; state.accessRefs = []; state.select.mockReset(); state.refresh.mockReset()
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

const renderLibrary = () => render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))

/** The bar's New artifact opens a popover grouped Artifacts / Craft; each entry opens the existing dialog on its flow. */
const newFrom = (name: string) => {
  fireEvent.click(screen.getAllByRole("button", { name: "New artifact" })[0])
  fireEvent.click(screen.getByRole("button", { name }))
}
const openAll = () => fireEvent.click(screen.getByRole("tab", { name: /All artifacts/ }))

it("lists every kind with something in it, a panel Page as a Dashboard, and keeps empty items in their own view", () => {
  renderLibrary()
  openAll()
  fireEvent.click(screen.getByRole("radio", { name: "Gallery" }))
  const cards = within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem")
  expect(cards.map(card => within(card).getByRole("heading").textContent)).toEqual(["Pricing site", "Partner brief", "Price model", "Weekly report"])
  expect(within(cards[0]).getByText("App")).toBeTruthy()
  expect(within(cards[0]).getByText("Static")).toBeTruthy()
  expect(within(cards[0]).getByText("Shared v2")).toBeTruthy()
  expect(within(cards[1]).getByText("Not shared")).toBeTruthy()
  expect(within(cards[2]).queryByText(/shared/i)).toBeNull()
  // A Page of data panels is a Dashboard in the UI's words: "Page" is never a label (#5132).
  expect(within(cards[3]).getByText("Dashboard")).toBeTruthy()
  expect(screen.queryByText("Page")).toBeNull()
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

const serverOn = () => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_SERVER_CATALOG", "true") }
const listedNames = () => within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(row => within(row).getByRole("heading").textContent)

it.each([
  ["name", [wire({ slug: "alpha", id: "page-alpha", name: "Alpha", artifact_kind: "word", updated_at: "2026-10-20T10:00:00Z" }), wire({ slug: "zulu", id: "page-zulu", name: "Zulu", artifact_kind: "word", updated_at: "2026-10-30T10:00:00Z" })], ["Alpha", "Zulu"]],
] as const)("a server window keeps the server's %s order, never re-sorted by update time", (sort, pages, want) => {
  serverOn()
  windowState.pages = [...pages]
  windowState.total = 2
  renderLibrary()
  openAll()
  fireEvent.change(screen.getByRole("combobox", { name: "Sort" }), { target: { value: sort } })
  fireEvent.click(screen.getByRole("radio", { name: "Gallery" }))
  expect(new URLSearchParams(windowState.calls[windowState.calls.length - 1].params!).get("sort")).toBe(sort)
  expect(listedNames()).toEqual(want)
})

// #5152: the server sorts by storage kind; the UI kind (an interactive app is an App) is derived in the browser.
it("sorts by kind in the browser, in the UI kinds' order, even with the server catalog on", () => {
  serverOn()
  state.pages = [
    wire({ slug: "memo", id: "page-memo", name: "Newer word", artifact_kind: "word", updated_at: "2026-10-30T10:00:00Z" }),
    wire({ slug: "showcase", id: "page-showcase", name: "Showcase", artifact_kind: "project", artifact_profile: "interactive", updated_at: "2026-10-25T10:00:00Z" } as Partial<WirePage>),
    wire({ slug: "sales", id: "page-sales", name: "Sales", artifact_kind: "dashboard", updated_at: "2026-10-20T10:00:00Z" }),
  ]
  renderLibrary()
  openAll()
  fireEvent.change(screen.getByRole("combobox", { name: "Sort" }), { target: { value: "kind" } })
  fireEvent.click(screen.getByRole("radio", { name: "Gallery" }))
  expect(windowState.calls[windowState.calls.length - 1].params).toBeNull()
  expect(listedNames()).toEqual(["Sales", "Showcase", "Newer word"])
})

it("moves to the last page when the total shrinks under the current offset, so nothing becomes unreachable", () => {
  serverOn()
  windowState.pages = [wire({ slug: "last", id: "page-last", name: "Last one", artifact_kind: "project" })]
  windowState.total = 51
  const view = renderLibrary()
  openAll()
  fireEvent.click(within(screen.getByRole("navigation", { name: "Artifact pages" })).getByRole("button", { name: "Next" }))
  expect(windowState.calls[windowState.calls.length - 1]).toMatchObject({ offset: 50 })
  // The last item was archived: the same offset now reads nothing, and only 50 remain.
  windowState.pages = []
  windowState.total = 50
  view.rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))
  expect(windowState.calls[windowState.calls.length - 1]).toMatchObject({ offset: 0 })
})

it("asks the server for archived and test-made items, which it leaves out by default", () => {
  renderLibrary()
  // Without them the Archive and Test items views stay empty against the real API (#4444).
  expect(state.pagesArgs).toEqual(["ws1", undefined, { archived: "include", testItems: "include" }])
})

it("Home lists each artifact once, filters by kind chips, and offers what can be made grouped Artifact and Craft", () => {
  renderLibrary()
  expect(screen.getByRole("heading", { level: 1, name: "Artifacts" })).toBeTruthy()
  expect(screen.getAllByRole("button", { name: "Open Partner brief" })).toHaveLength(1)
  // A fresh library with nothing failed, stale or recovered has nothing that needs attention: unshared is not a reason.
  expect(screen.queryByRole("heading", { name: /Needs attention/ })).toBeNull()
  fireEvent.click(within(screen.getByRole("group", { name: "Kind" })).getByRole("button", { name: /Sheets/ }))
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(item => item.textContent)).toEqual([expect.stringContaining("Price model")])
  fireEvent.click(screen.getByRole("radio", { name: "Table" }))
  expect(screen.getByRole("table")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  expect(within(screen.getByRole("region", { name: "Artifacts" })).getByRole("button", { name: "New App" })).toBeTruthy()
  expect(within(screen.getByRole("region", { name: "Craft · one editor at a time" })).getByRole("button", { name: "New Word" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: "New Slides" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "New Word" }))
  expect(screen.getByRole("dialog", { name: "New artifact" }).dataset.kind).toBe("word")
})

it("Needs attention lists a failed refresh and a recovered draft, never an unshared item", () => {
  state.pages = [
    ...state.pages,
    wire({ slug: "ops", id: "page-ops", name: "Ops board", panel_count: 3, has_application: true, failed_panels: 1 } as Partial<WirePage>),
    wire({ slug: "memo", id: "page-memo", name: "Memo", artifact_kind: "word", has_document: true, native_craft_application: "wordcraft" } as Partial<WirePage>),
  ]
  // A draft this person's browser kept for the native Word (u1, ws1, by Page reference).
  drafts.add("u1:ws1:@page-memo")
  try {
    renderLibrary()
    const section = screen.getByRole("region", { name: /Needs attention/ })
    expect(within(section).getAllByRole("button").map(button => button.getAttribute("aria-label"))).toEqual(["Your recovered draft: Memo", "Refresh failed: Ops board"])
  } finally { drafts.clear() }
})

it("New artifact in the bar opens the dialog in the folder being looked at", () => {
  state.folders = [{ id: "f1", slug: "sales", name: "Sales", pageCount: 1 }]
  state.pages = [wire({ slug: "site", name: "Pricing site", artifact_kind: "project", folder: { slug: "sales", name: "Sales" } } as Partial<WirePage>)]
  renderLibrary()
  fireEvent.click(screen.getByRole("button", { name: /Sales/ }))
  newFrom("New App")
  const dialog = screen.getByRole("dialog", { name: "New artifact" })
  expect([dialog.dataset.kind, dialog.dataset.folder]).toEqual(["project", "sales"])
})

it("does not offer New artifact to a member who cannot create Pages, whatever the server serves (#4534)", () => {
  // A Viewer: projects are available to read, Dashboards and Slides are built in, but no role or page.create.
  state.caps = { projects: true, dashboards: true, slides: true, documents: true, designs: true, pages: false }
  const view = renderLibrary()
  expect(screen.queryByRole("button", { name: "New artifact" })).toBeNull()
  expect(screen.queryByRole("button", { name: /^New / })).toBeNull()
  view.unmount()
  state.pages = []
  renderLibrary()
  expect(screen.getByText("No artifacts yet")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "New artifact" })).toBeNull()
})

it("offers New artifact in the bar, on Home and in the empty state to a member who can create", () => {
  state.caps = { projects: true, dashboards: true, slides: true, documents: true, designs: true, pages: true }
  const view = renderLibrary()
  expect(screen.getAllByRole("button", { name: "New artifact" })).toHaveLength(1)
  fireEvent.click(screen.getByRole("button", { name: "New artifact" }))
  for (const name of ["New App", "New Dashboard", "New Slides"]) expect(screen.getByRole("button", { name })).toBeTruthy()
  view.unmount()
  state.pages = []
  renderLibrary()
  expect(screen.getAllByRole("button", { name: "New artifact" })).toHaveLength(2)
})

it("a just-created artifact opens by its slug, never as an ID, so it does not land on not available", () => {
  renderLibrary()
  newFrom("New App")
  fireEvent.click(screen.getByRole("button", { name: "Create" }))
  // The new item is not in the list yet; the route opens it by slug and moves to its ID once it resolves.
  expect(state.select).toHaveBeenCalledWith(null, "fresh-design")
  expect(state.select).not.toHaveBeenCalledWith("fresh-design")
})

it("opens a just-created artifact with a confirmed ID at its own address, never by slug first (#4902)", () => {
  // By slug, the library mounted the native editor (whose admission refreshes
  // the session) and the exported build then reloaded the page as
  // /artifacts/<id> mid-refresh, which signed the person out.
  renderLibrary()
  newFrom("New App")
  fireEvent.click(screen.getByRole("button", { name: "Create with ID" }))
  expect(state.select).toHaveBeenCalledWith("page-id-1")
  expect(state.select).not.toHaveBeenCalledWith(null, "fresh-sheet")
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
  // The frame names it once, in the header's title menu; the body is the viewer.
  expect(screen.queryByRole("heading", { level: 1, name: "Pricing site" })).toBeNull()
  expect(screen.getByRole("button", { name: "Pricing site, artifact menu" })).toBeTruthy()
  expect(document.querySelector("[data-artifact-frame]")?.getAttribute("data-artifact-frame")).toBe("app")
  expect(screen.getByTestId("viewer-provider").dataset).toMatchObject({ slug: "site", tab: "preview" })
  expect(screen.getByTestId("viewer-panel")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "All artifacts" }))
  expect(state.select).toHaveBeenCalledWith(null)
})

it("without the project surface the project opens read-only in the frame, never as a link to Pages", () => {
  state.projectsOn = false
  state.item = "page-site"
  const { container } = renderLibrary()
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
  expect(screen.getByTestId("project-view").dataset).toMatchObject({ slug: "@page-site", readOnly: "true", sharedHref: "/artifacts/page-site?workspace_id=ws1&version=2" })
  expect(screen.queryByRole("link", { name: /Open full view|Open in Pages|Open page/ })).toBeNull()
  expect(container.querySelector('a[href^="/pages"]')).toBeNull()
  expect(screen.getByRole("button", { name: "Status: Read-only" })).toBeTruthy()
})

it("offers Edit source on a read-only project only to someone who may write, and opens the source editor in the frame", async () => {
  state.projectsOn = false
  state.item = "page-site"
  hosted.canWrite = true
  const onEdit = vi.fn()
  try {
    render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} onEditChange={onEdit} />))
    await openTitleMenu("Pricing site")
    fireEvent.click(await screen.findByRole("menuitem", { name: /Edit source/ }))
    expect(onEdit).toHaveBeenCalledWith(true)
    expect(screen.getByTestId("project-view").dataset.readOnly).toBe("false")
  } finally { hosted.canWrite = false }
  cleanup()
  renderLibrary()
  await openTitleMenu("Pricing site")
  await screen.findByRole("menuitem", { name: /Move to folder/ })
  expect(screen.queryByRole("menuitem", { name: /Edit source/ })).toBeNull()
})

it.each([true, false])("?edit=1 opens the App source editor inside the frame (project viewer %s)", projectsOn => {
  state.projectsOn = projectsOn
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item="page-site" version={3} onSelect={state.select} edit />))
  expect(screen.getByTestId("project-view").dataset).toMatchObject({ slug: "@page-site", readOnly: "false", version: "3" })
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
})

it("?edit=1 opens the panel editor inside the frame, and Done editing returns to the view", async () => {
  const onEdit = vi.fn()
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item="page-weekly" onSelect={state.select} edit onEditChange={onEdit} />))
  expect(screen.getByTestId("page-editor").dataset.page).toBe("page-weekly")
  expect(screen.queryByTestId("page-content")).toBeNull()
  await openTitleMenu("Weekly report")
  fireEvent.click(await screen.findByRole("menuitem", { name: /Done editing/ }))
  expect(onEdit).toHaveBeenLastCalledWith(false)
  expect(screen.getByTestId("page-content").dataset.page).toBe("page-weekly")
})

it("a panel Page offers Edit, which opens its editor in place", async () => {
  state.item = "page-weekly"
  renderLibrary()
  await openTitleMenu("Weekly report")
  fireEvent.click(await screen.findByRole("menuitem", { name: /^Edit$/ }))
  expect(screen.getByTestId("page-editor").dataset.page).toBe("page-weekly")
  fireEvent.click(screen.getByRole("button", { name: "Back to view" }))
  expect(screen.getByTestId("page-content")).toBeTruthy()
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

it("other kinds do not read a document; an older workbook shows its own preview in the frame", () => {
  state.item = "page-model"
  renderLibrary()
  expect(screen.getByTestId("legacy-workbook").dataset.page).toBe("page-model")
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
  expect(state.accessRefs.at(-1)).toBe("@page-gone")
})

it.each([
  ["signedout", "Sign in to open this artifact"],
  ["denied", "You don’t have access"],
  ["missing", "This artifact doesn’t exist"],
])("an address the catalog lacks shows the %s gate the server answers", (access, heading) => {
  state.access = access
  state.item = "page-gone"
  renderLibrary()
  expect(screen.getByRole("heading", { name: heading })).toBeTruthy()
  fireEvent.click(screen.getAllByRole("button", { name: /Back to Artifacts|All artifacts/ })[0])
  expect(state.select).toHaveBeenCalledWith(null)
})

it("the folder panel steps aside while an artifact is open, and a pin keeps it open", () => {
  state.item = "page-brief"
  renderLibrary()
  const pin = screen.getByRole("button", { name: "Keep the panel open beside artifacts" })
  expect(pin.getAttribute("aria-pressed")).toBe("false")
  fireEvent.click(pin)
  expect(screen.getByRole("button", { name: "Let the panel close when an artifact opens" }).getAttribute("aria-pressed")).toBe("true")
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
  // The viewer's Latest / Shared switch says which version is shared; the stage does not repeat it (Info in the drawer may).
  expect(within(document.querySelector<HTMLElement>("[data-artifact-stage]")!).queryByText("Shared v2")).toBeNull()
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
  // Its own project view, read-only, inside the frame: never the old panels and never a link out.
  expect(screen.getByTestId("project-view").dataset).toMatchObject({ slug: "@owner-page", readOnly: "true" })
  expect(screen.queryByRole("link", { name: /Open full view/ })).toBeNull()
  expect(screen.queryByTestId("viewer-panel")).toBeNull()
})

it.each(["dashboard", "slides"] as const)("opens a %s in the generic project viewer while its own view is switched off", artifact_kind => {
  state.pages = [wire({ id: "typed-page", slug: "typed", name: "Typed artifact", artifact_kind })]
  state.item = "typed-page"
  renderLibrary()
  expect(screen.getByTestId("viewer-panel")).toBeTruthy()
  expect(screen.queryByRole("link", { name: /Open full view/ })).toBeNull()
})

it.each(["word", "sheet"] as const)("never links a native %s out to Pages", artifact_kind => {
  state.pages = [wire({ id: "native-page", slug: "saved-native", name: "Native document", artifact_kind })]
  state.projectsOn = false; state.item = "native-page"
  const { container } = renderLibrary()
  expect(screen.queryByRole("link", { name: /Open full view/ })).toBeNull()
  expect(container.querySelector('a[href^="/pages"]')).toBeNull()
})

it("shows a legacy deck in the frame, never in its old Page reader", () => {
  state.pages = [wire({ id: "legacy-page", slug: "legacy-slides", name: "Legacy deck", has_slides: true })]
  state.item = "legacy-page"
  const { container } = renderLibrary()
  expect(screen.getByTestId("legacy-deck").dataset.page).toBe("legacy-page")
  expect(screen.queryByRole("link", { name: /Open full view/ })).toBeNull()
  expect(container.querySelector('a[href^="/pages"]')).toBeNull()
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

const openTitleMenu = (name: string) => act(async () => {
  fireEvent.pointerDown(screen.getByRole("button", { name: `${name}, artifact menu` }), { button: 0, ctrlKey: false })
})

it("remembers what was opened and starred, per person, in Recently opened and Favorites", async () => {
  state.item = "page-brief"
  const view = renderLibrary()
  await openTitleMenu("Partner brief")
  fireEvent.click(await screen.findByRole("menuitem", { name: /Add to favorites/ }))
  await openTitleMenu("Partner brief")
  expect(await screen.findByRole("menuitem", { name: /Remove from favorites/ })).toBeTruthy()
  fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
  state.item = null
  view.rerender(withQuery(<ArtifactsLibrary workspaceId="ws1" item={state.item} onSelect={state.select} />))
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
  expect(thumbs).toEqual(["app", "word", "sheet", "dashboard"])
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

it("a detail offers Share, Move and access in its Info drawer", async () => {
  state.item = "page-model"
  renderLibrary()
  // Access is read only once Info opens: an open artifact never asks for its grants unasked.
  expect(screen.queryByRole("region", { name: "Access to Price model" })).toBeNull()
  await openTitleMenu("Price model")
  expect(await screen.findByRole("menuitem", { name: /Move to folder/ })).toBeTruthy()
  fireEvent.click(screen.getByRole("menuitem", { name: /^Info/ }))
  expect(within(screen.getByRole("complementary", { name: "Info" })).getByRole("region", { name: "Access to Price model" })).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  expect(screen.getByRole("dialog", { name: "Share Price model" })).toBeTruthy()
})

it("a chosen folder has its settings", () => {
  state.folders = [{ id: "f1", slug: "sales", name: "Sales", pageCount: 1 }]
  render(withQuery(<ArtifactsLibrary workspaceId="ws1" item={null} onSelect={state.select} filters={{ view: "all", q: "", kinds: [], access: [], version: [], owners: [], folder: "sales" }} />))
  fireEvent.click(screen.getByRole("button", { name: "Folder settings" }))
  expect(screen.getByRole("dialog", { name: "Settings for Sales" })).toBeTruthy()
})

// #4898: New artifact → Page with panels makes a blank Page; it must not vanish
// from the library the person made it in, and hidden blanks are pointed to.
it("keeps a blank Page the person just created in the library and points to hidden blank items", () => {
  state.pages = [wire({ slug: "fresh-design", id: "page-fresh", name: "UX walkthrough page" })]
  renderLibrary()
  expect(screen.getByText("No artifacts yet")).toBeTruthy()
  expect(screen.getByText(/1 blank item is in Empty/)).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Show empty (1)" }))
  expect(screen.getAllByText("UX walkthrough page").length).toBeGreaterThan(0)
  cleanup()

  renderLibrary()
  newFrom("New App")
  fireEvent.click(within(screen.getByRole("dialog", { name: "New artifact" })).getByRole("button", { name: "Create" }))
  expect(state.select).toHaveBeenCalledWith(null, "fresh-design")
  expect(screen.queryByText("No artifacts yet")).toBeNull()
  expect(screen.getAllByText("UX walkthrough page").length).toBeGreaterThan(0)
})

// #4471 review: the server's empty=exclude cannot see what this person made
// here, so with the server catalog on, All must still keep their blank Page.
it("with the server catalog flag, All keeps a blank Page the person just created", () => {
  serverOn()
  state.pages = [wire({ slug: "fresh-design", id: "page-fresh", name: "UX walkthrough page" })]
  windowState.pages = []
  windowState.total = 0
  renderLibrary()
  newFrom("New App")
  fireEvent.click(within(screen.getByRole("dialog", { name: "New artifact" })).getByRole("button", { name: "Create" }))
  openAll()
  expect(windowState.calls[windowState.calls.length - 1].params).toBeNull()
  expect(within(screen.getByRole("list", { name: "Artifacts" })).getAllByRole("listitem").map(row => row.textContent)).toEqual([expect.stringContaining("UX walkthrough page")])
})
