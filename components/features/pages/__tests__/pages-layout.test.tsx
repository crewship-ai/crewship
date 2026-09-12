/**
 * The /pages shell — PRD §9b.1 (three zones) and §9b.2 (the header line).
 *
 * The header line is the Routines/Credentials idiom: icon + name + `·` + a
 * dense count summary — `38 routines · 0 runs`, `12 secrets · 2 waiting on a
 * tool`, `12 pages · 3 stale`. The second clause is the one worth testing:
 * claiming "all fresh" over an index that reported no freshness at all is the
 * silent-old-numbers failure §4 exists to prevent, so it is dropped rather
 * than guessed.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

// The four editor sections are five other files' worth of behaviour and are
// tested there. This suite is about the shell around them: which door opens,
// what the address says, and that the rail never blinks.
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: () => <div data-testid="section-content">Content</div>,
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data">Data & actions</div>,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access">Access</div>,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history">History</div>,
}))
// A stand-in application host: reports "an application is on screen" for a
// Page that has one, and follows the header's switch between the frame and
// the panels — which is exactly the contract the real view keeps.
vi.mock("@/components/features/pages/page-application", () => ({
  PageApplicationView: ({
    page,
    panels,
    fallback,
    onAvailableChange,
  }: {
    page: { has_application?: boolean } | null
    panels?: boolean
    fallback: React.ReactNode
    onAvailableChange?: (available: boolean) => void
  }) => {
    const available = page?.has_application === true
    React.useEffect(() => {
      onAvailableChange?.(available)
      return () => onAvailableChange?.(false)
    }, [available, onAvailableChange])
    if (!available || panels) return <>{fallback}</>
    return <div data-testid="application">Running application</div>
  },
}))

import { PagesLayout } from "@/components/features/pages/pages-layout"
import { PAGE_STATE_ORDER } from "@/components/features/pages/page-state"
import { PANEL_STATES } from "@/components/features/pages/panels/types"
import type { WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

const FLEET: WirePage = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  owner: "crew/lookout",
  panels: [
    { id: "a", schema: "status.v1", title: "Jede to?", span: 8, state: "stale", data: { items: [] } },
  ],
}
const CLOSE: WirePage = {
  id: "cpage2",
  slug: "nightly-close",
  name: "Nightly close",
  owner: "crew/finance",
  panels: [{ id: "a", schema: "metric.v1", span: 4, state: "fresh", data: { value: 1 } }],
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

function newQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
}

function renderLayout(list: WirePage[], slug?: string) {
  const qc = newQueryClient()
  const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/api/v1/pages/")) {
      const wanted = decodeURIComponent(url.split("/api/v1/pages/")[1].split("?")[0])
      return okJSON(list.find((p) => p.slug === wanted) ?? null)
    }
    return okJSON(list)
  })
  vi.stubGlobal("fetch", mockFetch)
  render(
    <QueryClientProvider client={qc}>
      <PagesLayout workspaceId="ws-1" slug={slug} now={NOW} />
    </QueryClientProvider>,
  )
  return { qc, mockFetch }
}

describe("PagesLayout", () => {
  beforeEach(() => {
    cleanup()
    push.mockReset()
    // The editor's mode and section live in the address now, so a test that
    // left `?mode=edit` there would hand it to the next one. (resets the
    // address between tests.)
    window.history.replaceState(null, "", "/pages")
  })
  afterEach(() => vi.unstubAllGlobals())

  it("writes the header line as '<n> pages · <n> stale'", async () => {
    renderLayout([FLEET, CLOSE])
    const header = screen.getByLabelText("Pages")
    await waitFor(() => expect(header.textContent).toContain("2 pages"))
    expect(header.textContent).toContain("· 1 stale")
  })

  it("says 'all fresh' rather than '0 stale' when nothing has gone quiet", async () => {
    renderLayout([CLOSE])
    const header = screen.getByLabelText("Pages")
    await waitFor(() => expect(header.textContent).toContain("· all fresh"))
  })

  it("claims neither when the index carried no freshness at all", async () => {
    // A count is not a verdict. "all fresh" over an index that reported
    // nothing is exactly the lie §4 exists to prevent.
    renderLayout([{ id: "p", slug: "p", name: "P", panels: 2 }])
    const header = screen.getByLabelText("Pages")
    await waitFor(() => expect(header.textContent).toContain("1 page"))
    const text = header.textContent ?? ""
    expect(text).toContain("1 page")
    expect(text).not.toContain("all fresh")
    expect(text).not.toContain("stale")
  })

  // Picking a page rewrites the address bar WITHOUT navigating, and the
  // difference is the whole reason this is not `router.push`.
  //
  // Routing to /pages/<slug> made Next unmount the shell and rebuild it, so the
  // rail — which has nothing to do with which page is open — blinked out and
  // came back on every click, losing its scroll position and its filters with
  // it. The URL still has to change, because /pages/[slug] is a real route that
  // a refresh, a bookmark and a shared link all arrive through; it just must
  // not be a navigation.
  it("rewrites the URL on a pick without navigating", async () => {
    const pushState = vi.spyOn(window.history, "pushState")
    try {
      renderLayout([FLEET, CLOSE])
      await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())
      fireEvent.click(screen.getByText("Flotila .201"))

      expect(pushState).toHaveBeenCalledWith(null, "", "/pages/fleet-201")
      // The router is the thing that would have remounted everything.
      expect(push).not.toHaveBeenCalled()
    } finally {
      pushState.mockRestore()
    }
  })

  // The failure this guards is the one a reader reports as "the sidebar
  // flickers": if the rail is torn down and rebuilt on selection, its DOM node
  // identity changes. Holding the node across the click is the strongest
  // assertion available here that nothing above it remounted.
  it("keeps the rail mounted across a selection", async () => {
    renderLayout([FLEET, CLOSE])
    await waitFor(() => expect(screen.getByText("Flotila .201")).toBeTruthy())

    const railBefore = document.querySelector('[data-slot="pages-rail"]')
    expect(railBefore).toBeTruthy()

    fireEvent.click(screen.getByText("Flotila .201"))
    await waitFor(() => {
      const railAfter = document.querySelector('[data-slot="pages-rail"]')
      expect(railAfter).toBe(railBefore)
    })
  })

  // #2515: Edit hands the whole column to the editor. The rail must not be
  // torn down for that — its scroll and filters have to be there on return —
  // so it steps off-screen instead, and comes back with the same node.
  it("keeps the rail mounted but out of the way while editing, and brings it back", async () => {
    renderLayout([FLEET, CLOSE], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    const rail = document.querySelector('[data-slot="pages-rail"]')
    const aside = rail?.closest("aside")
    expect(aside).toBeTruthy()
    expect(aside?.hasAttribute("inert")).toBe(false)

    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeTruthy())
    expect(document.querySelector('[data-slot="pages-rail"]')).toBe(rail)
    expect(aside?.hasAttribute("inert")).toBe(true)
    expect(aside?.getAttribute("aria-hidden")).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: /back to page/i }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeNull())
    expect(document.querySelector('[data-slot="pages-rail"]')).toBe(rail)
    expect(aside?.hasAttribute("inert")).toBe(false)
  })

  // The Application | Panels switch replaces the "Stop application / show
  // panels" bar. It sits in the page header, only while an application is
  // on screen, and Panels closes the frame in this tab.
  it("offers Application | Panels in the header only for a running application, and Panels shows the grid", async () => {
    const APP: WirePage = { ...CLOSE, slug: "ops-lab", name: "Operations Lab", has_application: true, publication_version: 4 }
    renderLayout([FLEET, APP], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    expect(screen.queryByRole("group", { name: "Show" })).toBeNull()

    cleanup()
    renderLayout([FLEET, APP], "ops-lab")
    await waitFor(() => expect(screen.getByTestId("application")).toBeTruthy())
    // The view reports availability from an effect, one commit after the
    // frame appears, so the header's switch is awaited on its own.
    const group = await screen.findByRole("group", { name: "Show" })
    expect(screen.getByRole("button", { name: "Application" }).getAttribute("aria-pressed")).toBe("true")
    expect(group.textContent).not.toContain("Stop")

    fireEvent.click(screen.getByRole("button", { name: "Panels" }))
    await waitFor(() => expect(screen.queryByTestId("application")).toBeNull())
    expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Panels" }).getAttribute("aria-pressed")).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: "Application" }))
    await waitFor(() => expect(screen.getByTestId("application")).toBeTruthy())

    // Editing takes the whole column; the switch is not a thing to do while editing.
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    await waitFor(() => expect(document.querySelector("[data-slot='page-editor']")).toBeTruthy())
    expect(screen.queryByRole("group", { name: "Show" })).toBeNull()
  })

  it("renders the overview with no slug, and the page's grid with one", async () => {
    renderLayout([FLEET, CLOSE])
    await waitFor(() => expect(screen.getByText("Overview")).toBeTruthy())
    expect(document.querySelector("[data-slot='panel-grid']")).toBeNull()

    cleanup()
    renderLayout([FLEET, CLOSE], "fleet-201")
    await waitFor(() => expect(document.querySelector("[data-slot='panel-grid']")).toBeTruthy())
    expect(screen.getByRole("button", { name: /back to pages/i })).toBeTruthy()
    // The rail stays: opening a page must not cost the search and the facets.
    expect(screen.getByLabelText("Search pages")).toBeTruthy()
  })
})

describe("the STATUS facet covers the closed vocabulary", () => {
  it("offers an option for every panel state", () => {
    // A facet silently missing an option is a filter that hides rows, and the
    // failure is invisible at runtime.
    expect([...PAGE_STATE_ORDER].sort()).toEqual([...PANEL_STATES].sort())
  })
})
