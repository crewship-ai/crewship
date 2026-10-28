import { fireEvent, render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/artifact-slides-projection.json"
const mock = vi.hoisted(() => ({ comment: vi.fn(), snapshot: {} as Record<string, unknown>, send: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "owner" } } }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: (...args: unknown[]) => mock.send(...args) }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: vi.fn(), ready: false }) }))
vi.mock("../../projects/use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => true,
  useArtifactProjectSnapshot: () => ({ data: mock.snapshot, error: null, isLoading: false, refetch: vi.fn() }),
  useArtifactProjectMutations: () => ({ restore: { mutate: vi.fn() }, share: { mutate: vi.fn() }, comment: { mutate: mock.comment } }),
}))
import { ArtifactViewerProvider } from "../../projects/artifact-viewer-context"
import { ArtifactViewerPanel } from "../artifact-viewer-panel"
afterEach(() => vi.unstubAllEnvs())

it("starts at deck order rather than manifest order and posts the first authored anchor without a thumbnail click", async () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", "true")
  const projection = JSON.parse(JSON.stringify(fixture))
  projection.deck.slide_order = ["alpha", "zeta"]
  projection.deck.slides[0].id = "alpha"; projection.deck.slides[1].id = "zeta"
  projection.anchors.title.slide_id = "alpha"; projection.anchors.title.path = "slides/alpha.html"
  projection.anchors.constructor.slide_id = "zeta"; projection.anchors.constructor.path = "slides/zeta.html"
  projection.deck.slides.reverse()
  mock.snapshot = {
    page_id: "page", artifact_kind: "slides", can_write: true, latest_version: 2, shared_version: 1,
    manifest: { version: 2, entrypoint: "deck.json", files: [
      { path: "slides/zeta.html", media_type: "text/html", content: '<section id="zeta">Second</section>' },
      { path: "slides/alpha.html", media_type: "text/html", content: '<section id="alpha"><p id="title">Revenue</p></section>' },
      { path: "deck.json", media_type: "application/json", content: '{"schema_version":1,"order":["alpha","zeta"]}' },
    ] }, versions: [], comments: [],
  }
  mock.send.mockResolvedValue(new Response(JSON.stringify(projection)))
  Object.defineProperty(document, "fonts", { configurable: true, value: { load: vi.fn().mockResolvedValue([{}]), ready: Promise.resolve() } })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(<QueryClientProvider client={client}><ArtifactViewerProvider workspaceId="workspace" target={{ slug: "deck" }} onTargetChange={() => {}}><ArtifactViewerPanel /></ArtifactViewerProvider></QueryClientProvider>)
  await screen.findByText("Slide 1 / 2")
  fireEvent.click(screen.getByRole("button", { name: "Select", exact: true }))
  fireEvent.pointerDown(screen.getByRole("button", { name: "text 1" }))
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Improve this title" } })
  fireEvent.click(screen.getByRole("button", { name: "Save comment" }))
  expect(mock.comment).toHaveBeenCalledWith({ version: 2, path: "slides/alpha.html", selector: "#title", quote: "Revenue", text: "Improve this title", send_to_agent: false }, expect.anything())
})
