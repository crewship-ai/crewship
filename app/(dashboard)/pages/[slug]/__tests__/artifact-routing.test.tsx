import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
const state = vi.hoisted(() => ({ kind: undefined as string | undefined, rawKind: undefined as string | undefined, error: null as string | null, loading: false, denied: false, query: "", workspace: "real-owner-workspace", slug: "ordinary-demo-owner-page", read: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query), useRouter: () => ({ replace: vi.fn() }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: state.workspace, loading: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "full" }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => null }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: ({initialArtifactVersion}: {initialArtifactVersion?: number}) => <p data-pin={initialArtifactVersion}>Legacy Page reader</p> }))
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: ({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) => <p data-testid="artifact-reader" data-workspace={workspaceId} data-page={slug} data-pin={initialVersion}>Versioned artifact reader</p> }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => {
  state.read(...args)
  return { loading: state.loading, accessDenied: state.denied, error: state.denied ? "denied" : state.error, page: { artifactKind: state.kind }, raw: state.rawKind ? { artifact_kind: state.rawKind } : null }
} }))
import { PageDetailClient } from "../page-client"
beforeEach(() => { state.kind = undefined; state.rawKind = undefined; state.error = null; state.loading = false; state.denied = false; state.query = ""; state.workspace = "real-owner-workspace"; state.slug = "ordinary-demo-owner-page"; state.read.mockClear(); vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
it.each(["project", "dashboard", "slides", "word", "sheet", undefined])("keeps the guarded PagesLayout mounted for normal backend %s URLs", kind => {
  state.kind = kind
  const view = render(<PageDetailClient />)
  const shell = screen.getByText("Legacy Page reader")
  state.slug = "next-url-page"
  view.rerender(<PageDetailClient />)
  expect(screen.getByText("Legacy Page reader")).toBe(shell)
  expect(state.read).not.toHaveBeenCalled()
})
it("retains an explicit selected code pin without a second Page-type request", () => {
  state.query = "artifact=1&version=2"
  render(<PageDetailClient />)
  expect(screen.getByTestId("artifact-reader")).toHaveAttribute("data-pin", "2")
  expect(state.read).not.toHaveBeenCalled()
})
it.each([["version=2", "2"], ["version=0", null], ["version=-1", null], ["version=bogus", null], ["version=1.5", null], ["", null]] as const)("passes only positive safe code pins for normal guarded route %s", (query, pin) => {
  state.query = query
  render(<PageDetailClient />)
  expect(screen.getByText("Legacy Page reader").getAttribute("data-pin")).toBe(pin)
})
