import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

// A native Word or Sheet opens in the one Craft editor inside its Artifacts
// detail, behind the single-home switch and the server's availability answer;
// every other kind, and every refusal, keeps the detail it had.
const state = vi.hoisted(() => ({ pages: [] as unknown[], userId: "u1", availability: { enabled: true, can_edit: true } as unknown, status: 200, fetch: vi.fn(), read: "ready" }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: () => ({ pages: state.pages, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/lib/api-fetch", async importOriginal => ({ ...(await importOriginal<typeof import("@/lib/api-fetch")>()), apiFetch: (url: string) => {
  state.fetch(url)
  return Promise.resolve({ ok: state.status === 200, json: () => Promise.resolve(state.availability) })
} }))
vi.mock("@/components/features/pages/native-craft-editor-host", () => ({
  NativeCraftEditorHost: (props: { workspaceId: string; pageId: string; humanId: string; enabled?: boolean }) =>
    <div data-testid="native-host" data-workspace={props.workspaceId} data-page={props.pageId} data-human={props.humanId} data-enabled={String(props.enabled)} />,
}))
vi.mock("@/components/features/pages/native-craft-preview", () => ({ NativeCraftPreview: () => <div data-testid="native-preview" /> }))
vi.mock("../page-content", () => ({ ArtifactPageContent: () => <div data-testid="page-content" /> }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => false }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: state.userId } } }) }))
vi.mock("@/hooks/use-document-viewer-read", () => ({
  useDocumentViewerRead: () => ({ scopeKey: "k", refresh: vi.fn(), read: state.read === "ready" ? { status: "ready", title: "Brief", content: { schema: 3, rev: 2, content: { type: "doc" } } } : { status: state.read } }),
}))
vi.mock("@/components/features/pages/document-readonly/document-readonly", () => ({ DocumentReadonly: () => <div>saved preview</div> }))
vi.mock("@/hooks/use-user-preference", async () => {
  const { useState } = await import("react")
  return { useUserPreference: <T,>(_key: string, initial: T) => { const [value, set] = useState<T>(initial); return [value, set, { ready: true }] as const } }
})
vi.mock("@/hooks/use-page-folders", () => ({ usePageFolders: () => ({ folders: [], loading: false, error: null, supported: true, refresh: vi.fn(), reread: vi.fn() }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({
  useArtifactCapabilities: () => ({ projects: true, dashboards: false, slides: false, documents: true, sheets: true, pages: true }),
}))
vi.mock("@/components/layout/secondary-panel", async importOriginal => ({ ...(await importOriginal<typeof import("@/components/layout/secondary-panel")>()), SecondaryPanel: ({ label, children }: { label: string; children: React.ReactNode }) => <aside aria-label={label}>{children}</aside> }))
vi.mock("@/components/features/chat/artifact-context/craft/craft-lock-bar", () => ({
  CraftLockBar: ({ slug }: { slug: string }) => <div data-testid="lock-bar" data-slug={slug} />,
  craftEditorHref: (ws: string, slug: string) => `/editor?${ws}&${slug}`,
}))
vi.mock("@/components/features/chat/artifact-context/craft/use-craft-lease", () => ({
  useCraftLeaseState: () => ({ status: null, heldByMe: false, requestedByMe: false, requestedGeneration: null }),
}))

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ArtifactsLibrary } from "../artifacts-library"

const page = (extra: Record<string, unknown>): PageView => toPageView({ id: "page-brief", slug: "brief", name: "Brief", owner: "user/u1", reach: ["owner"], ...extra } as WirePage)
const open = () => render(<QueryClientProvider client={new QueryClient()}><ArtifactsLibrary workspaceId="ws1" item="page-brief" onSelect={vi.fn()} /></QueryClientProvider>)

beforeEach(() => {
  state.userId = "u1"
  state.availability = { enabled: true, can_edit: true }
  state.status = 200
  state.read = "ready"
  state.fetch.mockReset()
  state.pages = [page({ artifact_kind: "word", has_document: true, artifact_shared_version: 2, native_craft_application: "wordcraft" })]
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it.each([
  ["Word", { artifact_kind: "word", has_document: true, native_craft_application: "wordcraft" }],
  ["Sheet", { artifact_kind: "sheet", has_workbook: true, native_craft_application: "gridcraft" }],
])("edits a native %s in the single Craft editor inside the detail, without a link back to Pages", async (_kind, extra) => {
  state.pages = [page(extra)]
  open()
  const host = await screen.findByTestId("native-host")
  expect(state.fetch).toHaveBeenCalledWith("/api/v1/pages/%40page-brief/craft/availability?workspace_id=ws1")
  expect(host.dataset).toMatchObject({ workspace: "ws1", page: "page-brief", human: "u1", enabled: "true" })
  expect(screen.queryByRole("link", { name: /Open in Pages/ })).toBeNull()
  expect(screen.queryByTestId("lock-bar")).toBeNull()
  expect(screen.queryByText("saved preview")).toBeNull()
})

it("shows the server's read-only preview when only the preview is enabled", async () => {
  state.availability = { enabled: false, can_edit: false, preview_enabled: true }
  open()
  expect(await screen.findByTestId("native-preview")).toBeInTheDocument()
  expect(screen.queryByTestId("native-host")).toBeNull()
})

it.each([
  ["the editor is off", () => { state.availability = { enabled: false } }],
  ["this person may not edit", () => { state.availability = { enabled: true, can_edit: false } }],
  ["availability is not confirmed", () => { state.status = 503 }],
])("keeps the saved preview, and no Edit that opens the Pages editor, when %s", async (_name, setup) => {
  setup()
  open()
  // /pages/craft/editor makes the same availability decision, so it would open nothing either.
  expect(await screen.findByText("Editing is not available here right now.")).toBeInTheDocument()
  expect(screen.getByText("saved preview")).toBeInTheDocument()
  expect(screen.queryByTestId("lock-bar")).toBeNull()
  expect(screen.queryByRole("link", { name: /Open in Pages/ })).toBeNull()
  expect(screen.queryByText(/Open in Pages/)).toBeNull()
  expect(screen.queryByTestId("native-host")).toBeNull()
})

it("a native Sheet whose editor is off says so instead of an empty detail", async () => {
  state.pages = [page({ artifact_kind: "sheet", has_workbook: true, native_craft_application: "gridcraft" })]
  state.availability = { enabled: false }
  open()
  expect(await screen.findByText("Editing is not available here right now.")).toBeInTheDocument()
  expect(screen.queryByText(/Open in Pages/)).toBeNull()
})

it("a native Word whose saved preview fails does not point to Pages, which sends it back here", async () => {
  state.availability = { enabled: false }
  state.read = "error"
  open()
  expect(await screen.findByText(/The document preview could not be loaded\./)).toBeInTheDocument()
  expect(screen.queryByText(/Open in Pages/)).toBeNull()
})

it.each(["unavailable", "error"])("an old-editor Word whose document answers %s has no Open in Pages: Pages reads the same document", status => {
  state.pages = [page({ has_document: true })]
  state.read = status
  open()
  expect(screen.getByText(/older version of the editor/)).toBeInTheDocument()
  expect(screen.queryByRole("link", { name: /Open in Pages/ })).toBeNull()
})

it("an old-editor Word that still reads keeps Open in Pages, where its editor opens it", () => {
  state.pages = [page({ has_document: true })]
  open()
  expect(screen.getByText("saved preview")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: /Open in Pages/ })).toHaveAttribute("href", "/pages/brief?workspace_id=ws1")
})

it("with the switch off an old-editor Word keeps Open in Pages as before", () => {
  vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "false")
  state.pages = [page({ has_document: true })]
  state.read = "unavailable"
  open()
  expect(screen.getByRole("link", { name: /Open in Pages/ })).toBeInTheDocument()
})

it("is unchanged with the single-home switch off: lock bar, preview and Open in Pages", async () => {
  vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "false")
  open()
  expect(screen.getByTestId("lock-bar")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: /Open in Pages/ })).toBeInTheDocument()
  await waitFor(() => expect(screen.getByText("saved preview")).toBeInTheDocument())
  expect(state.fetch).not.toHaveBeenCalled()
  expect(screen.queryByTestId("native-host")).toBeNull()
})

it.each([
  ["a panel Page", { }],
  ["an older-editor Word", { has_document: true }],
  ["an older Sheet", { has_workbook: true }],
  ["a project", { artifact_kind: "project" }],
])("never mounts the native editor for %s", (_name, extra) => {
  state.pages = [page(extra)]
  open()
  expect(state.fetch).not.toHaveBeenCalled()
  expect(screen.queryByTestId("native-host")).toBeNull()
})
