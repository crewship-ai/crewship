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
