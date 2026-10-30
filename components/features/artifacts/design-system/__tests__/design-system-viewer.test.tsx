import { render, screen } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
const mock = vi.hoisted(() => ({ snapshot: {} as Record<string, unknown> }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "owner" } } }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: vi.fn() }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ dispatch: vi.fn(), ready: false }) }))
vi.mock("@/components/features/chat/artifact-context/projects/use-artifact-project-data", () => ({
  useArtifactProjectsAvailable: () => true,
  useArtifactProjectSnapshot: () => ({ data: mock.snapshot, error: null, isLoading: false, refetch: vi.fn() }),
  useArtifactProjectMutations: () => ({ restore: { mutate: vi.fn() }, share: { mutate: vi.fn() }, comment: { mutate: vi.fn() } }),
}))
import { ArtifactViewerProvider } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { ArtifactViewerPanel } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
afterEach(() => vi.unstubAllEnvs())

const tokens = '{"schema_version":1,"name":"Harbor","colors":{"primary":"#1d4ed8"},"typography":{"body":{"size":16}},"spacing":{"md":16},"radii":{"sm":4}}'
mock.snapshot = {
  page_id: "page", name: "Harbor tokens", can_write: true, can_share: true, can_restore: true, can_send_to_agent: false, latest_version: 2, shared_version: 0,
  manifest: { version: 2, entrypoint: "index.html", files: [
    { path: "index.html", media_type: "text/html", content: '<h1 id="title">Harbor</h1>' },
    { path: "design-system.json", media_type: "application/json", content: tokens },
  ] },
  versions: [{ version: 2, created_at: "2026-10-29T00:00:00Z" }, { version: 1, created_at: "2026-10-28T00:00:00Z" }], comments: [],
}

const show = () => render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
  <ArtifactViewerProvider workspaceId="workspace" target={{ slug: "harbor" }} onTargetChange={() => {}}><ArtifactViewerPanel /></ArtifactViewerProvider>
</QueryClientProvider>)

it("opens a project artifact with design-system.json as a Design System token sheet when enabled", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "true")
  const { container } = show()
  expect(screen.getByText("Design System")).toBeTruthy()
  expect(container.querySelector('[data-token="color.primary"]')).not.toBeNull()
  expect(container.querySelector("iframe")).toBeNull()
})

it("keeps the existing project preview while the flag is off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "false")
  const { container } = show()
  expect(screen.queryByText("Design System")).toBeNull()
  expect(container.querySelector("[data-token]")).toBeNull()
})
