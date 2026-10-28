import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
const state = vi.hoisted(() => ({ kind: undefined as string | undefined, rawKind: undefined as string | undefined, error: null as string | null, loading: false, denied: false, query: "", read: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => "ordinary-demo-owner-page" }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "real-owner-workspace", loading: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "full" }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => null }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Legacy Page reader</p> }))
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: ({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) => <p data-testid="artifact-reader" data-workspace={workspaceId} data-page={slug} data-pin={initialVersion}>Versioned artifact reader</p> }))
vi.mock("@/hooks/use-pages", () => ({ usePage: (...args: unknown[]) => {
  state.read(...args)
  return { loading: state.loading, accessDenied: state.denied, error: state.denied ? "denied" : state.error, page: { artifactKind: state.kind }, raw: state.rawKind ? { artifact_kind: state.rawKind } : null }
} }))
import { PageDetailClient } from "../page-client"
beforeEach(() => { state.kind = undefined; state.rawKind = undefined; state.error = null; state.loading = false; state.denied = false; state.query = ""; state.read.mockClear(); vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })
it.each(["project", "dashboard", "slides"])("routes a normal owner Page URL for backend %s without an artifact query", kind => {
  state.kind = kind
  render(<PageDetailClient />)
  expect(screen.getByTestId("artifact-reader")).toHaveAttribute("data-workspace", "real-owner-workspace")
  expect(screen.getByTestId("artifact-reader")).toHaveAttribute("data-page", "ordinary-demo-owner-page")
  expect(screen.queryByText("Legacy Page reader")).toBeNull()
})
it.each(["word", "sheet", undefined])("preserves the existing native/legacy reader for %s", kind => {
  state.kind = kind
  render(<PageDetailClient />)
  expect(screen.getByText("Legacy Page reader")).toBeInTheDocument()
  expect(screen.queryByTestId("artifact-reader")).toBeNull()
})
it("does not flash panels while authoritative type metadata is loading", () => {
  state.loading = true
  render(<PageDetailClient />)
  expect(screen.queryByText("Legacy Page reader")).toBeNull()
  expect(screen.queryByTestId("artifact-reader")).toBeNull()
})
it("cannot use a cached artifact kind after current Page authority is denied", () => {
  state.kind = "dashboard"
  const view = render(<PageDetailClient />)
  expect(screen.getByTestId("artifact-reader")).toBeInTheDocument()
  state.denied = true
  view.rerender(<PageDetailClient />)
  expect(screen.queryByTestId("artifact-reader")).toBeNull()
  expect(screen.queryByText("Legacy Page reader")).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("Document unavailable.")
})
it("keeps the normal route unchanged when artifact UI is off", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "false"); state.kind = "dashboard"
  render(<PageDetailClient />)
  expect(screen.getByText("Legacy Page reader")).toBeInTheDocument()
  expect(state.read).not.toHaveBeenCalled()
})
it("retains an explicit selected code pin without a second Page-type request", () => {
  state.query = "artifact=1&version=2"
  render(<PageDetailClient />)
  expect(screen.getByTestId("artifact-reader")).toHaveAttribute("data-pin", "2")
  expect(state.read).not.toHaveBeenCalled()
})

it("recognizes a current backend kind even before the view normalizer knows that additive kind", () => {
  state.rawKind = "dashboard"
  render(<PageDetailClient />)
  expect(screen.getByTestId("artifact-reader")).toBeInTheDocument()
  expect(screen.queryByText("Legacy Page reader")).toBeNull()
})
it("shows a load error without either content reader after a current request fails", () => {
  state.kind = "dashboard"; state.error = "private backend error must not be reflected"
  render(<PageDetailClient />)
  expect(screen.queryByTestId("artifact-reader")).toBeNull()
  expect(screen.queryByText("Legacy Page reader")).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("Document could not be loaded.")
  expect(screen.queryByText(state.error)).toBeNull()
})
