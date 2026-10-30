import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AccessMode } from "@/hooks/use-access-mode"
import { toPageView, type WirePage } from "@/hooks/use-pages"

// /pages/<slug> hands one page to its Artifacts detail behind the same switch
// as the overview; reference links, pins, edit addresses, restricted members
// and anything that cannot be resolved to an ID stay on Pages.
const state = vi.hoisted(() => ({
  query: "workspace_id=ws1", slug: "brief", access: "trusted" as AccessMode, replace: vi.fn(), read: vi.fn(),
  wire: null as Record<string, unknown> | null, loading: false, error: null as string | null, denied: false, session: "authenticated" as string,
}))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.query), useRouter: () => ({ replace: state.replace, push: vi.fn() }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
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
  Object.assign(state, { query: "workspace_id=ws1", slug: "brief", access: "trusted", wire: wire({ artifact_kind: "word", has_document: true }), loading: false, error: null, denied: false, session: "authenticated" })
  state.replace.mockReset(); state.read.mockReset()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it.each([
  ["a native Word", { artifact_kind: "word", has_document: true }],
  ["a native Sheet", { artifact_kind: "sheet", has_workbook: true }],
  ["a panel Page", {}],
  ["a versioned project", { artifact_kind: "project" }],
])("replaces /pages/<slug> with the Artifacts detail of %s, keeping the workspace", (_name, extra) => {
  state.wire = wire(extra)
  render(<PageDetailClient />)
  expect(state.read).toHaveBeenCalledWith("ws1", "brief")
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-brief?workspace_id=ws1")
  expect(screen.queryByText(/Pages view/)).toBeNull()
})

it.each([
  ["the switch is off", () => vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "false")],
  ["the library itself is off", () => vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")],
])("stays on Pages without reading the page when %s", (_name, setup) => {
  setup()
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it.each(["ref=abc", "version=2", "mode=edit"])("keeps an address with %s on Pages", param => {
  state.query = `workspace_id=ws1&${param}`
  render(<PageDetailClient />)
  expect(state.read).not.toHaveBeenCalled()
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages view brief")).toBeTruthy()
})

it("keeps restricted members on their Pages catalog", () => {
  state.access = "restricted"
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Restricted pages")).toBeTruthy()
})

it.each([
  ["the read is refused", () => { state.denied = true }],
  ["the read fails", () => { state.error = "Pages unavailable" }],
  ["the page has no valid ID", () => { state.wire = wire({ id: "not a valid id!" }) }],
  ["the page is an older Craft the detail cannot open", () => { state.wire = wire({ has_workbook: true }) }],
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
