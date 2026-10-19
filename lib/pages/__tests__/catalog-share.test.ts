import { describe, expect, it } from "vitest"
import { toPageView } from "@/hooks/use-pages"
import {
  EMPTY_CATALOG_FILTERS,
  catalogFacets,
  catalogItemType,
  filterCatalog,
  outcomeOf,
  shareConsequence,
  summariseBulk,
} from "../catalog-share"

const page = (id: string, extra: Record<string, unknown> = {}) =>
  toPageView({ id, slug: id, name: id, owner: "user/ada", has_document: true, panels: [], ...extra })

describe("catalog filters", () => {
  const pages = [
    page("a", { private_audience: true }),
    page("b", { private_audience: false, folder: { slug: "ops", name: "Ops" }, owner: "crew/ops", fresh_panels: 1, panels: undefined, panel_count: 1 }),
    page("c", { has_document: false, owner: "crew/ops", stale_panels: 1, panels: undefined, panel_count: 1 }),
    page("d"),
  ]

  it("classifies the type from what the server confirmed", () => {
    expect(catalogItemType(pages[0])).toBe("document")
    expect(catalogItemType(pages[2])).toBe("page")
  })

  it("filters by type, scope, owner and state together", () => {
    expect(filterCatalog(pages, { ...EMPTY_CATALOG_FILTERS, types: ["document"] }).map(p => p.id)).toEqual(["a", "b", "d"])
    expect(filterCatalog(pages, { ...EMPTY_CATALOG_FILTERS, scopes: ["private"] }).map(p => p.id)).toEqual(["a"])
    expect(filterCatalog(pages, { ...EMPTY_CATALOG_FILTERS, scopes: ["shared"] }).map(p => p.id)).toEqual(["b"])
    expect(filterCatalog(pages, { ...EMPTY_CATALOG_FILTERS, scopes: ["unknown"] }).map(p => p.id)).toEqual(["c", "d"])
    expect(filterCatalog(pages, { ...EMPTY_CATALOG_FILTERS, owners: ["crew/ops"], states: ["stale"] }).map(p => p.id)).toEqual(["c"])
  })

  it("derives facet counts only from the rows it was given", () => {
    const facets = catalogFacets([pages[0]])
    expect(facets.scopes.private).toBe(1)
    expect(facets.owners.map(o => o.ref)).toEqual(["user/ada"])
    expect(facets.types.document).toBe(1)
    expect(facets.types.page).toBe(0)
  })
})

describe("share consequence", () => {
  it("states who gains content and history and that the ID stays", () => {
    const text = shareConsequence({ name: "Plan", targetName: "Ops", audience: "Through the folder: visible to crew Ops.", versions: 4 })
    expect(text.audience).toContain("crew Ops")
    expect(text.history).toContain("4 versions")
    expect(text.identity).toMatch(/same ID/)
    expect(text.copy).toMatch(/Duplicate/)
  })
  it("does not invent a version count", () => {
    expect(shareConsequence({ name: "Plan", targetName: "Ops", audience: "x", versions: null }).history).toMatch(/full history/)
  })
})

describe("bulk results", () => {
  it("maps statuses to per-item outcomes", () => {
    expect(outcomeOf(null)).toBe("success")
    expect(outcomeOf({ status: 403 })).toBe("forbidden")
    expect(outcomeOf({ status: 404 })).toBe("not_found")
    expect(outcomeOf({ status: 409 })).toBe("conflict")
    expect(outcomeOf({ status: 500 })).toBe("failed")
  })
  it("summarises every item, never hiding a failure", () => {
    const s = summariseBulk([
      { slug: "a", name: "A", outcome: "success" },
      { slug: "b", name: "B", outcome: "forbidden" },
      { slug: "c", name: "C", outcome: "conflict" },
    ])
    expect(s).toEqual({ success: 1, forbidden: 1, conflict: 1, not_found: 0, failed: 0 })
  })
})
