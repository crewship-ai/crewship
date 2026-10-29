import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"

// A native Word whose Craft lock was lost while edits were unsaved: the detail
// offers the browser's local draft back, never saves it over the holder's work.
const state = vi.hoisted(() => ({ pages: [] as PageView[], holder: null as null | { actor_id: string; display_name: string; kind: "human" }, userId: "u1" }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePages: () => ({ pages: state.pages, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({ useArtifactProjectsAvailable: () => true }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.userId } } }) }))
vi.mock("@/hooks/use-document-viewer-read", () => ({
  useDocumentViewerRead: () => ({ scopeKey: "k", refresh: vi.fn(), read: { status: "ready", title: "Partner brief", content: { schema: 3, rev: 2, content: { type: "doc" } } } }),
}))
vi.mock("@/components/features/pages/document-readonly/document-readonly", () => ({ DocumentReadonly: () => <div>document body</div> }))
vi.mock("@/hooks/use-user-preference", async () => {
  const { useState } = await import("react")
  return { useUserPreference: <T,>(_key: string, initial: T) => { const [value, set] = useState<T>(initial); return [value, set, { ready: true }] as const } }
})
vi.mock("@/hooks/use-page-folders", () => ({ usePageFolders: () => ({ folders: [], loading: false, error: null, supported: true, refresh: vi.fn(), reread: vi.fn() }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({
  useArtifactCapabilities: () => ({ projects: true, dashboards: false, slides: false, documents: true, sheets: false, pages: true }),
}))
vi.mock("@/components/layout/secondary-panel", async importOriginal => ({ ...(await importOriginal<typeof import("@/components/layout/secondary-panel")>()), SecondaryPanel: ({ label, children }: { label: string; children: React.ReactNode }) => <aside aria-label={label}>{children}</aside> }))
vi.mock("@/components/features/chat/artifact-context/craft/craft-lock-bar", () => ({
  CraftLockBar: ({ slug }: { slug: string }) => <div data-testid="lock-bar" data-slug={slug} />,
  craftEditorHref: (ws: string, slug: string) => `/editor?${ws}&${slug}`,
}))
vi.mock("@/components/features/chat/artifact-context/craft/use-craft-lease", () => ({
  useCraftLeaseState: () => ({ status: { holder: state.holder, mine: false, generation: "2", can_write: false, requests: [] }, heldByMe: false, requestedByMe: false, requestedGeneration: null }),
}))

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { saveLocalDraft } from "@/components/features/chat/artifact-context/craft/local-draft"
import { ArtifactsLibrary } from "../artifacts-library"

// vitest.setup.ts replaces localStorage with inert vi.fn stubs; back them with a real map here.
const store = new Map<string, string>()
beforeEach(() => {
  store.clear()
  vi.mocked(localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, String(v)) })
  vi.mocked(localStorage.removeItem).mockImplementation((k: string) => { store.delete(k) })
  state.userId = "u1"
  state.holder = { actor_id: "user:u2", display_name: "Pavel Srba", kind: "human" }
  state.pages = [toPageView({ id: "page-brief", slug: "brief", name: "Partner brief", owner: "user/u1", artifact_kind: "word", artifact_shared_version: 2, has_document: true, reach: ["owner"] } as WirePage)]
})
afterEach(cleanup)

const client = () => new QueryClient()
const tree = (queries: QueryClient) => <QueryClientProvider client={queries}><ArtifactsLibrary workspaceId="ws1" item="brief" onSelect={vi.fn()} /></QueryClientProvider>
const open = () => render(tree(client()))

it("offers a recovered local draft when the lock is held by someone else", () => {
  saveLocalDraft("u1", "ws1", "@page-brief", { content: "My unsaved paragraph", baseVersion: 2, savedAt: "2026-10-28T11:14:00Z" })
  open()
  expect(screen.getByTestId("lock-bar")).toBeInTheDocument()
  expect(screen.getByText("Recovered local draft")).toBeInTheDocument()
  expect(screen.getByText(/Pavel Srba edits now, so it is not saved over their work\./)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "Compare" }))
  expect(screen.getByRole("region", { name: "Your local draft" })).toHaveTextContent("My unsaved paragraph")
})

it("shows nothing extra without a local draft", () => {
  open()
  expect(screen.queryByText("Recovered local draft")).toBeNull()
})

it("drops the previous person's open draft comparison when the signed-in person changes", () => {
  saveLocalDraft("u1", "ws1", "@page-brief", { content: "ALICE_PRIVATE_SECRET", baseVersion: 2, savedAt: "2026-10-28T11:14:00Z" })
  saveLocalDraft("u2", "ws1", "@page-brief", { content: "Bob's own paragraph", baseVersion: 2, savedAt: "2026-10-28T11:20:00Z" })
  const queries = client()
  const { rerender } = render(tree(queries))
  fireEvent.click(screen.getByRole("button", { name: "Compare" }))
  expect(screen.getByRole("region", { name: "Your local draft" })).toHaveTextContent("ALICE_PRIVATE_SECRET")
  // Same visible Word, another person signed in on this browser.
  state.userId = "u2"
  rerender(tree(queries))
  expect(screen.queryByText(/ALICE_PRIVATE_SECRET/)).toBeNull()
  expect(screen.queryByRole("region", { name: "Your local draft" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Compare" }))
  expect(screen.getByRole("region", { name: "Your local draft" })).toHaveTextContent("Bob's own paragraph")
})
