import { cleanup, render, screen, fireEvent } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
const state = vi.hoisted(() => ({ kind: undefined as string | undefined, rawKind: undefined as string | undefined, error: null as string | null, loading: false, denied: false, query: "", workspace: "real-owner-workspace", slug: "ordinary-demo-owner-page", read: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query), useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: state.workspace, loading: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "full" }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => null }))
vi.mock("@/components/features/pages/pages-layout", async () => {
  const { useEditorRoute } = await import("@/components/features/pages/editor/use-editor-route")
  return { PagesLayout: ({ slug }: { slug: string }) => {
    const nav = useEditorRoute(slug)
    return <><p>Legacy Page reader</p><p data-testid="selected-editor">{nav.slug}</p><button onClick={() => nav.setDirty(true)}>Make dirty</button>{nav.pending && <p role="dialog">Unsaved changes</p>}</>
  } }
})
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: ({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) => <p data-testid="artifact-reader" data-workspace={workspaceId} data-page={slug} data-pin={initialVersion}>Versioned artifact reader</p> }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => {
  state.read(...args)
  return { loading: state.loading, accessDenied: state.denied, error: state.denied ? "denied" : state.error, page: { artifactKind: state.kind }, raw: state.rawKind ? { artifact_kind: state.rawKind } : null }
} }))
import { PageDetailClient } from "../page-client"
beforeEach(() => { state.kind = undefined; state.rawKind = undefined; state.error = null; state.loading = false; state.denied = false; state.query = ""; state.workspace = "real-owner-workspace"; state.slug = "ordinary-demo-owner-page"; state.read.mockClear(); vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
it.each(["false", "true"])("flag=%s Back URL change retains the original dirty editor until its existing guard asks", flag => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", flag)
  window.history.replaceState(null, "", "/pages/ordinary-demo-owner-page?mode=edit")
  const view = render(<PageDetailClient />)
  fireEvent.click(screen.getByRole("button", { name: "Make dirty" }))
  state.slug = "other-existing-page"
  window.history.replaceState(null, "", "/pages/other-existing-page")
  view.rerender(<PageDetailClient />)
  expect(screen.getByTestId("selected-editor")).toHaveTextContent("ordinary-demo-owner-page")
  expect(screen.getByRole("dialog")).toHaveTextContent("Unsaved changes")
})
