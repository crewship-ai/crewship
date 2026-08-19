/**
 * The Pages data layer (PRD `docs/prd/pages.md` §4, §9b.2, §11, §11b).
 *
 * The load-bearing assertions are the honest-arithmetic ones. A dashboard that
 * reports "0 stale" because the server told it nothing about freshness is the
 * Pushgateway behaviour §4 exists to reject, and the em-dash rule (§9b.4) is
 * how the product already separates "we looked and there was nothing" from "we
 * have nothing to look at". These tests pin that a state this build could not
 * read never becomes `fresh`, and never becomes a measured zero.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import {
  EMPTY_PAGE_FILTERS,
  matchesPageFilters,
  normalizePage,
  normalizePageList,
  ownerFacets,
  pageFilterCount,
  pagesKeys,
  stateFacetCounts,
  summarisePages,
  togglePageFilter,
  toPageView,
  toPanelState,
  toPanelView,
  usePage,
  usePages,
  worstPanelState,
  type WirePage,
} from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

function wirePage(over: Partial<WirePage> = {}): WirePage {
  return {
    id: "cpage00000000000000001",
    slug: "fleet-201",
    name: "Flotila .201",
    owner: "crew/lookout",
    panels: [
      {
        id: "sluzby",
        schema: "status.v1",
        title: "Jede to?",
        owner: "crew/lookout",
        producer: "script/watch-services.sh",
        sla_seconds: 30,
        span: 8,
        state: "fresh",
        data: { items: [{ name: "api", state: "ok", label: "200 OK" }] },
        provenance: {
          producer: "script/watch-services.sh",
          run_id: "crun0000000000000000001",
          produced_at: "2026-08-12T11:59:40Z",
        },
      },
    ],
    ...over,
  }
}

describe("panel normalising (§11b)", () => {
  it("keeps the schema untrusted and splits spec from payload", () => {
    const view = toPanelView({
      id: "cpu",
      schema: "metric.v1",
      title: "CPU",
      owner: "crew/lookout",
      sla_seconds: 60,
      span: 4,
      state: "fresh",
      data: { value: 0 },
      provenance: { producer: "routine/nightly", run_id: "r1", produced_at: "2026-08-12T11:00:00Z" },
    })
    expect(view.spec).toMatchObject({ id: "cpu", schema: "metric.v1", span: 4, sla_seconds: 60 })
    // A measured zero survives as a zero — it is not falsy-collapsed into "no data".
    expect(view.snapshot.payload).toEqual({ value: 0 })
    expect(view.snapshot.state).toBe("fresh")
    expect(view.snapshot.provenance).toEqual({
      producer: "routine/nightly",
      run_id: "r1",
      produced_at: "2026-08-12T11:00:00Z",
    })
  })

  it("reads the payload under either key the API may use", () => {
    expect(toPanelView({ id: "a", schema: "metric.v1", payload: { value: 3 } }).snapshot.payload)
      .toEqual({ value: 3 })
    expect(toPanelView({ id: "a", schema: "metric.v1", data: { value: 4 } }).snapshot.payload)
      .toEqual({ value: 4 })
  })

  it("defaults a missing span to the full 12-column width", () => {
    // internal/pages/spec.go: DefaultSpan = 12. Zero would render a panel with
    // no width at all.
    expect(toPanelView({ id: "a", schema: "metric.v1" }).spec.span).toBe(12)
    expect(toPanelView({ id: "a", schema: "metric.v1", span: 0 }).spec.span).toBe(12)
    expect(toPanelView({ id: "a", schema: "metric.v1", span: 40 }).spec.span).toBe(12)
    expect(toPanelView({ id: "a", schema: "metric.v1", span: 3 }).spec.span).toBe(3)
  })

  it("never invents freshness — an unreadable state is not fresh", () => {
    expect(toPanelState("fresh")).toBe("fresh")
    expect(toPanelState("never_produced")).toBe("never_produced")
    expect(toPanelState("recent")).toBeNull()
    expect(toPanelState(undefined)).toBeNull()

    const view = toPanelView({ id: "a", schema: "metric.v1", state: "recent", data: { value: 1 } })
    expect(view.state).toBeNull()
    expect(view.snapshot.state).not.toBe("fresh")
    expect(view.snapshot.state).toBe("never_produced")
  })

  it("ranks the worst state, with failed above stale", () => {
    expect(worstPanelState(["fresh", "stale", "failed"])).toBe("failed")
    expect(worstPanelState(["fresh", "stale"])).toBe("stale")
    expect(worstPanelState(["fresh", "never_produced"])).toBe("never_produced")
    expect(worstPanelState(["fresh", "fresh"])).toBe("fresh")
    expect(worstPanelState([null, null])).toBeNull()
  })
})

describe("page normalising", () => {
  it("tallies panel states and takes the newest payload as the page's own", () => {
    const page = toPageView(
      wirePage({
        panels: [
          { id: "a", schema: "metric.v1", state: "fresh", data: {}, provenance: { produced_at: "2026-08-12T09:00:00Z" } },
          { id: "b", schema: "status.v1", state: "stale", data: {}, provenance: { produced_at: "2026-08-12T11:30:00Z" } },
          { id: "c", schema: "table.v1", state: "never_produced" },
        ],
      }),
    )
    expect(page.tally).toMatchObject({ fresh: 1, stale: 1, never_produced: 1, failed: 0, total: 3, unknown: 0 })
    expect(page.state).toBe("stale")
    expect(page.lastProducedAt?.toISOString()).toBe("2026-08-12T11:30:00.000Z")
  })

  it("reads an index that sends a panel COUNT rather than panels", () => {
    // The CLI's acceptance fixture uses `"panels": 1`. The count is not a
    // freshness claim, so every panel stays unknown and the page reports no
    // state at all rather than a cheerful one.
    const page = toPageView({ id: "p", slug: "s", name: "S", panels: 3 })
    expect(page.panels).toBeNull()
    expect(page.tally.total).toBe(3)
    expect(page.tally.unknown).toBe(3)
    expect(page.state).toBeNull()
  })

  it("reads the stale count the index returns as a flat field (§10b.5d)", () => {
    // The Dashboard strip is documented as a read-only view over data the
    // page index ALREADY returns, "with the stale count as the right-hand
    // status word" — so a flat count is a shape the handler may well send.
    const page = toPageView({
      id: "p",
      slug: "s",
      name: "S",
      panel_count: 5,
      stale_panels: 2,
      fresh_panels: 3,
      last_produced_at: "2026-08-12T09:00:00Z",
    })
    expect(page.tally).toMatchObject({ stale: 2, fresh: 3, unknown: 0, total: 5 })
    expect(page.state).toBe("stale")
    expect(page.lastProducedAt?.toISOString()).toBe("2026-08-12T09:00:00.000Z")
  })

  it("reads a per-state rollup when the index sends one", () => {
    const page = toPageView({
      id: "p",
      slug: "s",
      name: "S",
      panel_count: 4,
      panel_states: { fresh: 2, stale: 1 },
    })
    expect(page.tally).toMatchObject({ fresh: 2, stale: 1, unknown: 1, total: 4 })
    expect(page.state).toBe("stale")
  })

  it("prints the owner without its prefix but keeps the ref as the facet key", () => {
    const page = toPageView(wirePage({ owner: "crew/lookout" }))
    expect(page.ownerRef).toBe("crew/lookout")
    expect(page.ownerLabel).toBe("lookout")
  })

  it("reads a list envelope in any of the shapes this repo uses", () => {
    const row = { slug: "a" }
    expect(normalizePageList([row])).toEqual([row])
    expect(normalizePageList({ pages: [row] })).toEqual([row])
    expect(normalizePageList({ rows: [row] })).toEqual([row])
    expect(normalizePageList({ items: [row] })).toEqual([row])
    expect(normalizePageList(null)).toEqual([])
    expect(normalizePageList("nope")).toEqual([])
  })

  it("unwraps a detail record whether or not it is wrapped", () => {
    expect(normalizePage({ slug: "a" })).toEqual({ slug: "a" })
    expect(normalizePage({ page: { slug: "a" } })).toEqual({ slug: "a" })
    expect(normalizePage(null)).toBeNull()
  })
})

describe("the four tiles (§9b.2) and the em-dash rule (§9b.4)", () => {
  it("counts stale pages, stale panels, today's pushes and what needs attention", () => {
    const pages = [
      toPageView(
        wirePage({
          slug: "a",
          panels: [
            { id: "1", schema: "metric.v1", state: "stale", data: {}, provenance: { produced_at: "2026-08-12T06:00:00Z" } },
            { id: "2", schema: "metric.v1", state: "fresh", data: {}, provenance: { produced_at: "2026-08-12T11:00:00Z" } },
          ],
        }),
      ),
      toPageView(
        wirePage({
          slug: "b",
          panels: [
            { id: "1", schema: "metric.v1", state: "failed", data: {}, provenance: { produced_at: "2026-08-10T11:00:00Z" } },
          ],
        }),
      ),
      toPageView(wirePage({ slug: "c", panels: [{ id: "1", schema: "metric.v1", state: "never_produced" }] })),
    ]
    const s = summarisePages(pages, NOW)
    expect(s.total).toBe(3)
    expect(s.stalePages).toBe(1)
    expect(s.stalePanels).toBe(1)
    expect(s.updatedToday).toBe(1) // page "a" only — "b" pushed two days ago
    expect(s.needsAttention).toBe(2) // failed, and never produced
    expect(s.hasFreshnessBasis).toBe(true)
  })

  it("reports NO basis when nothing on the wire carried a state", () => {
    // This is what makes the tile render `—` instead of `0`: a measured zero
    // and "we were told nothing" must not look the same.
    const s = summarisePages([toPageView({ slug: "a", name: "A", panels: 2 })], NOW)
    expect(s.hasFreshnessBasis).toBe(false)
    expect(s.stalePages).toBe(0)
  })
})

describe("facets (§9b.1)", () => {
  const pages = [
    toPageView(
      wirePage({
        slug: "fleet",
        name: "Fleet",
        owner: "crew/lookout",
        panels: [
          { id: "1", schema: "metric.v1", state: "stale" },
          { id: "2", schema: "metric.v1", state: "fresh" },
        ],
      }),
    ),
    toPageView(
      wirePage({
        slug: "close",
        name: "Nightly close",
        owner: "crew/finance",
        panels: [{ id: "1", schema: "metric.v1", state: "fresh" }],
      }),
    ),
  ]

  it("matches a STATUS pick on ANY panel, not on the page's worst state", () => {
    // "Fleet" is ranked stale, but a filter looking for fresh panels must
    // still find it — otherwise the facet hides the row it was asked for.
    expect(matchesPageFilters(pages[0], { states: ["fresh"], owners: [] }, "")).toBe(true)
    expect(matchesPageFilters(pages[0], { states: ["stale"], owners: [] }, "")).toBe(true)
    expect(matchesPageFilters(pages[0], { states: ["failed"], owners: [] }, "")).toBe(false)
  })

  it("is multi-select: two states are a union, not a replacement", () => {
    expect(matchesPageFilters(pages[1], { states: ["stale", "fresh"], owners: [] }, "")).toBe(true)
    expect(togglePageFilter(["stale"], "fresh")).toEqual(["stale", "fresh"])
    expect(togglePageFilter(["stale", "fresh"], "stale")).toEqual(["fresh"])
  })

  it("combines STATUS and OWNER instead of letting one clear the other (#1776)", () => {
    const f = { states: ["fresh"] as const, owners: ["crew/finance"] }
    expect(matchesPageFilters(pages[1], { states: [...f.states], owners: f.owners }, "")).toBe(true)
    expect(matchesPageFilters(pages[0], { states: [...f.states], owners: f.owners }, "")).toBe(false)
    expect(pageFilterCount({ states: ["fresh", "stale"], owners: ["crew/finance"] })).toBe(3)
    expect(pageFilterCount(EMPTY_PAGE_FILTERS)).toBe(0)
  })

  it("searches name, slug and owner", () => {
    expect(matchesPageFilters(pages[1], EMPTY_PAGE_FILTERS, "nightly")).toBe(true)
    expect(matchesPageFilters(pages[1], EMPTY_PAGE_FILTERS, "close")).toBe(true)
    expect(matchesPageFilters(pages[1], EMPTY_PAGE_FILTERS, "finance")).toBe(true)
    expect(matchesPageFilters(pages[1], EMPTY_PAGE_FILTERS, "lookout")).toBe(false)
  })

  it("counts facets over the whole set, so an unpicked option never reads 0", () => {
    expect(stateFacetCounts(pages)).toEqual({ fresh: 2, stale: 1, failed: 0, never_produced: 0 })
    expect(ownerFacets(pages)).toEqual([
      { ref: "crew/finance", label: "finance", count: 1 },
      { ref: "crew/lookout", label: "lookout", count: 1 },
    ])
  })
})

// ── the hooks ───────────────────────────────────────────────────────────────

function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

function errStatus(status: number): Response {
  return { ok: false, status, json: async () => ({}), text: async () => "" } as unknown as Response
}

describe("usePages / usePage", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  it("keys queries as [resource, workspaceId, params?]", () => {
    expect(pagesKeys.all("ws-1")).toEqual(["pages", "ws-1"])
    expect(pagesKeys.list("ws-1")).toEqual(["pages", "ws-1", { view: "list" }])
    expect(pagesKeys.detail("ws-1", "fleet")).toEqual(["pages", "ws-1", { slug: "fleet" }])
  })

  it("fires nothing without a workspace", async () => {
    renderHook(() => usePages(null), { wrapper: makeWrapper(qc) })
    await act(async () => { await Promise.resolve() })
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("reads the index off the workspace-unscoped route (§11b.1)", async () => {
    mockFetch.mockResolvedValue(okJSON([wirePage()]))
    const { result } = renderHook(() => usePages("ws-1"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.pages).toHaveLength(1))

    const url = mockFetch.mock.calls[0][0] as string
    expect(url).toContain("/api/v1/pages")
    expect(url).toContain("workspace_id=ws-1")
    expect(result.current.pages[0].slug).toBe("fleet-201")
    expect(result.current.pages[0].state).toBe("fresh")
  })

  it("reads one page and hands the renderer a spec plus a snapshot", async () => {
    mockFetch.mockResolvedValue(okJSON(wirePage()))
    const { result } = renderHook(() => usePage("ws-1", "fleet-201"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.page).not.toBeNull())

    expect(mockFetch.mock.calls[0][0]).toContain("/api/v1/pages/fleet-201")
    const panel = result.current.page!.panels![0]
    expect(panel.spec).toMatchObject({ id: "sluzby", schema: "status.v1", span: 8 })
    expect(panel.snapshot.state).toBe("fresh")
    expect(panel.snapshot.provenance?.run_id).toBe("crun0000000000000000001")
  })

  it("tells a 404 apart from any other failure, so the slug gets its own empty state", async () => {
    mockFetch.mockResolvedValue(errStatus(404))
    const { result } = renderHook(() => usePage("ws-1", "gone"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.error).not.toBeNull())
    expect(result.current.notFound).toBe(true)

    qc.clear()
    mockFetch.mockResolvedValue(errStatus(500))
    const other = renderHook(() => usePage("ws-1", "boom"), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(other.result.current.error).not.toBeNull())
    expect(other.result.current.notFound).toBe(false)
  })
})
