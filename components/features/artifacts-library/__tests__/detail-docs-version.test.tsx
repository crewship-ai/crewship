import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

// A version link (`/artifacts/<id>?version=N`, #5168) reaches the Docs view
// with its pin, so History can open at revision N.
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
vi.mock("../kinds/docs/docs-view", async importOriginal => ({
  ...(await importOriginal<typeof import("../kinds/docs/docs-view")>()),
  DocsBody: ({ version }: { version?: number }) => <div data-testid="docs-body" data-version={String(version)} />,
}))
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
const open = (version?: number) => render(<QueryClientProvider client={new QueryClient()}><ArtifactsLibrary workspaceId="ws1" item="page-brief" version={version} onSelect={vi.fn()} /></QueryClientProvider>)

beforeEach(() => {
  state.pages = [page({ has_document: true })]
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("hands a version link's pin to the Docs view (#5168)", async () => {
  open(2)
  expect((await screen.findByTestId("docs-body")).dataset.version).toBe("2")
})

it("opens the Docs view without a pin on a plain address", async () => {
  open()
  expect((await screen.findByTestId("docs-body")).dataset.version).toBe("undefined")
})
