import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"

// #4995: before, the API answered create, share and restore with
// /pages/<slug>?artifact=1[&version=N]. Those links now move to the Artifacts
// detail of the same page, in the same workspace and at the same version, so a
// reader of a shared-version link never lands on Latest.
const state = vi.hoisted(() => ({
  query: "artifact=1&workspace_id=ws1", slug: "brief", replace: vi.fn(), read: vi.fn(),
  wire: null as Record<string, unknown> | null, loading: false, error: null as string | null, denied: false,
}))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query), useRouter: () => ({ replace: state.replace, push: vi.fn() }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => "trusted" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: "u1" } } }) }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => <p>Restricted pages</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Pages view</p> }))
vi.mock("@/components/features/pages/artifact-project", () => ({
  ArtifactProject: ({ slug, initialVersion }: { slug: string; initialVersion?: number }) => <p data-testid="legacy" data-page={slug} data-pin={initialVersion ?? ""}>Pages project viewer</p>,
}))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePage: (...args: unknown[]) => {
    state.read(...args)
    const page = state.wire && !state.denied && !state.error ? toPageView(state.wire as unknown as WirePage) : null
    return { page, raw: page ? state.wire : null, loading: state.loading, error: state.denied ? "denied" : state.error, accessDenied: state.denied, notFound: false, live: "live" }
  },
}))

import { PageDetailClient } from "../page-client"

const project = (extra: Record<string, unknown> = {}) => ({ id: "page-brief", slug: "brief", name: "Brief", owner: "user/u1", reach: ["owner"], artifact_kind: "project", ...extra })

beforeEach(() => {
  Object.assign(state, { query: "artifact=1&workspace_id=ws1", slug: "brief", wire: project(), loading: false, error: null, denied: false })
  state.replace.mockReset(); state.read.mockReset()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it.each([
  ["a latest link", "artifact=1&workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1"],
  ["a shared-version link", "artifact=1&version=1&workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1&version=1"],
  ["a link with an unusable pin", "artifact=1&version=bogus&workspace_id=ws1", "/artifacts/page-brief?workspace_id=ws1"],
])("moves %s to the Artifacts detail with the same page, workspace and version", (_name, query, target) => {
  state.query = query
  render(<PageDetailClient />)
  expect(state.read).toHaveBeenCalledWith("ws1", "brief")
  expect(state.replace).toHaveBeenCalledWith(target)
  expect(screen.queryByTestId("legacy")).toBeNull()
})

it("resolves an @<page_id> reference to the same page", () => {
  state.slug = "@page-brief"
  state.query = "artifact=1&version=2&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.read).toHaveBeenCalledWith("ws1", "@page-brief")
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1&version=2")
})

it("keeps the Pages project viewer, pin included, in a build without the library", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  state.query = "artifact=1&version=1&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("legacy").dataset).toMatchObject({ page: "brief", pin: "1" })
})

it.each([
  ["the read is refused", () => { state.denied = true }],
  ["the read fails", () => { state.error = "Pages unavailable" }],
  ["the page has no valid ID", () => { state.wire = project({ id: "not a valid id!" }) }],
  ["the Artifacts detail does not show this kind in its project viewer", () => { state.wire = project({ artifact_kind: "word", has_document: true }) }],
])("fails closed into the Pages project viewer with its pin when %s", (_name, setup) => {
  state.query = "artifact=1&version=1&workspace_id=ws1"
  setup()
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("legacy").dataset).toMatchObject({ page: "brief", pin: "1" })
})

it("waits for the page before deciding, showing neither view", () => {
  state.loading = true
  state.wire = null
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.queryByTestId("legacy")).toBeNull()
})

it("follows the address: a later link is resolved for its own page, never the first one", () => {
  state.loading = true
  state.wire = null
  const view = render(<PageDetailClient />)
  expect(state.read).toHaveBeenLastCalledWith("ws1", "brief")
  state.slug = "second"
  state.loading = false
  state.wire = project({ id: "page-second", slug: "second" })
  view.rerender(<PageDetailClient />)
  expect(state.read).toHaveBeenLastCalledWith("ws1", "second")
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-second?workspace_id=ws1")
  expect(state.replace).not.toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1")
})

it("stays on Pages when the build has no project viewer to land on", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "false")
  state.query = "artifact=1&version=1&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("legacy").dataset).toMatchObject({ page: "brief", pin: "1" })
})

it("keeps the source editor reachable from the Edit source address", () => {
  state.query = "artifact=1&edit=1&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("legacy")).toBeInTheDocument()
})
