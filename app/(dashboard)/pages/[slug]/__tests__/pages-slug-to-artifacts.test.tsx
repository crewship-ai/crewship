import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AccessMode } from "@/hooks/use-access-mode"
import { toPageView, type WirePage } from "@/hooks/use-pages"

// /pages/<slug> hands one page to its Artifacts detail whenever the library is
// on (#5129); its editor addresses open the editor inside that detail. Single-page
// reference links, restricted members and anything that cannot be resolved to
// an ID stay on Pages.
const state = vi.hoisted(() => ({
  query: "workspace_id=ws1", slug: "brief", access: "trusted" as AccessMode, replace: vi.fn(), read: vi.fn(),
  wire: null as Record<string, unknown> | null, loading: false, error: null as string | null, denied: false, session: "authenticated" as string,
  workspace: "ws1",
}))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query), useRouter: () => ({ replace: state.replace, push: vi.fn() }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: state.workspace, loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => state.access }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: state.session, data: state.session === "authenticated" ? { user: { id: "u1" } } : null }) }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => <p>Restricted pages</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: ({ slug }: { slug: string }) => <p>Pages view {slug}</p> }))
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: () => <p>Versioned artifact reader</p> }))
vi.mock("@/hooks/use-pages", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-pages")>()),
  usePage: (...args: unknown[]) => {
    state.read(...args)
    const page = state.wire && !state.denied && !state.error ? toPageView(state.wire as unknown as WirePage) : null
    return { page, raw: page ? state.wire : null, loading: state.loading, error: state.denied ? "denied" : state.error, accessDenied: state.denied, notFound: false, live: "live" }
  },
}))

import { PageDetailClient } from "../page-client"

const wire = (extra: Record<string, unknown> = {}) => ({ id: "page-brief", slug: "brief", name: "Brief", owner: "user/u1", reach: ["owner"], ...extra })

beforeEach(() => {
  Object.assign(state, { workspace: "ws1", query: "workspace_id=ws1", slug: "brief", access: "trusted", wire: wire({ artifact_kind: "word", has_document: true }), loading: false, error: null, denied: false, session: "authenticated" })
  state.replace.mockReset(); state.read.mockReset()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it.each([
  ["a native Word", { artifact_kind: "word", has_document: true }],
  ["a native Sheet", { artifact_kind: "sheet", has_workbook: true }],
  ["a panel Page", {}],
  ["a versioned project", { artifact_kind: "project" }],
  ["an older workbook", { has_workbook: true }],
  ["an older deck", { has_slides: true }],
])("replaces /pages/<slug> with the Artifacts detail of %s, keeping the workspace", (_name, extra) => {
  state.wire = wire(extra)
  render(<PageDetailClient />)
  expect(state.read).toHaveBeenCalledWith("ws1", "brief")
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1")
  expect(screen.queryByText(/Pages view/)).toBeNull()
})

it("stays on Pages without reading the page when the library is off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it.each([
  ["the panel editor", "mode=edit", "/artifacts/page-brief?workspace_id=ws1&edit=1"],
  ["the panel editor on a section", "mode=edit&section=access", "/artifacts/page-brief?workspace_id=ws1&edit=1"],
  ["edit=1", "edit=1", "/artifacts/page-brief?workspace_id=ws1&edit=1"],
])("opens %s inside the Artifacts frame", (_name, param, target) => {
  state.wire = wire()
  state.query = `workspace_id=ws1&${param}`
  render(<PageDetailClient />)
  expect(state.replace).toHaveBeenCalledWith(target)
  expect(screen.queryByText(/Pages view/)).toBeNull()
})

it.each(["ref=abc", "mode=preview"])("keeps an address with %s on Pages", param => {
  state.query = `workspace_id=ws1&${param}`
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it.each(["workspace_id=ws1", "artifact=1&version=2&workspace_id=ws1", "version=2&workspace_id=ws1"])("keeps restricted members on their Pages catalog without reading the page (%s)", query => {
  state.access = "restricted"
  state.query = query
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Restricted pages")).toBeTruthy()
})

it("moves a pinned project address without artifact=1 to the pinned Artifacts detail", () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", "true")
  state.wire = wire({ artifact_kind: "project" })
  state.query = "version=2&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1&version=2")
})

it("keeps a pinned address of a kind without a project viewer on Pages", () => {
  state.query = "version=2&workspace_id=ws1"
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it.each([
  ["the read is refused", () => { state.denied = true }],
  ["the read fails", () => { state.error = "Pages unavailable" }],
  ["the page has no valid ID", () => { state.wire = wire({ id: "not a valid id!" }) }],
  ["nobody is signed in", () => { state.session = "unauthenticated"; state.loading = true }],
])("fails closed and stays on Pages when %s", (_name, setup) => {
  setup()
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it("waits for the page before deciding, without showing Pages", () => {
  state.loading = true
  state.wire = null
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.queryByText(/Pages view/)).toBeNull()
})

it("decides once: navigating to another page inside Pages does not redirect", () => {
  state.denied = true
  const view = render(<PageDetailClient />)
  expect(screen.getByText("Pages view brief")).toBeTruthy()
  state.denied = false
  state.slug = "other"
  view.rerender(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view other")).toBeTruthy()
})

it("follows a workspace switch while the page read is pending", () => {
  state.loading = true
  state.wire = null
  const view = render(<PageDetailClient />)
  expect(state.read).toHaveBeenLastCalledWith("ws1", "brief")
  state.workspace = "ws2"
  state.loading = false
  state.wire = wire({ artifact_kind: "word", has_document: true })
  view.rerender(<PageDetailClient />)
  expect(state.read).toHaveBeenLastCalledWith("ws2", "brief")
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws2")
  expect(state.replace).not.toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1")
})
