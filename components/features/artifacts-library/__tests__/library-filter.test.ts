import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { libraryItems } from "../library-items"
import { applyFilters, EMPTY_FILTERS, facetCounts, groupItems, parseQuery, sortItems, type LibraryFilters } from "../library-filter"

const page = (over: Partial<WirePage>): WirePage => ({ id: over.slug ?? "p", slug: "p", name: "P", owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)

const items = libraryItems([
  page({ slug: "site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, updated_at: "2026-10-27T12:00:00Z", folder: { slug: "sales", name: "Sales" } }),
  page({ slug: "draft", name: "Pricing draft", artifact_kind: "project", updated_at: "2026-10-27T11:00:00Z", private_audience: true }),
  page({ slug: "brief", name: "Partner brief", artifact_kind: "word", owner: "crew/research", owner_crew_name: "Research crew", reach: ["crew:research"] } as Partial<WirePage>),
  page({ slug: "weekly", name: "Weekly report", panel_count: 2 } as Partial<WirePage>),
  page({ slug: "blank", name: "Untitled" }),
  ...[1, 2, 3].map(n => page({ slug: `smoke-${n}`, name: `Smoke test ${n}`, artifact_kind: "project", artifact_shared_version: 1, updated_at: `2026-10-2${n}T10:00:00Z` })),
].map(toPageView))

const filter = (over: Partial<LibraryFilters>) => applyFilters(items, { ...EMPTY_FILTERS, view: "all", ...over }).map(item => item.slug)

describe("library filters", () => {
  it("hides empty items everywhere but the Empty view", () => {
    expect(filter({})).not.toContain("blank")
    expect(filter({ view: "empty" })).toEqual(["blank"])
  })

  it("parses kind:, is: and owner: operators and leaves the rest as text", () => {
    expect(parseQuery("kind:Word is:private owner:you budget plan")).toEqual({ text: "budget plan", kinds: ["word"], is: ["private"], owner: ["you"] })
    expect(parseQuery("kind:nonsense")).toMatchObject({ text: "kind:nonsense", kinds: [] })
  })

  it("searches by name and operators", () => {
    expect(filter({ q: "pricing" })).toEqual(["site", "draft"])
    expect(filter({ q: "is:private" })).toEqual(["draft"])
    expect(filter({ q: "kind:projects is:unshared" })).toEqual(["draft"])
    expect(filter({ q: "owner:research" })).toEqual(["brief"])
  })

  it("filters by facets and folder, Unfiled as an empty slug", () => {
    expect(filter({ kinds: ["page"] })).toEqual(["weekly"])
    expect(filter({ access: ["private"] })).toEqual(["draft"])
    expect(filter({ version: ["shared"], folder: "sales" })).toEqual(["site"])
    expect(filter({ folder: "", kinds: ["word"] })).toEqual(["brief"])
  })

  it("Needs attention lists made-but-unshared versioned items, Repeated runs the series", () => {
    expect(filter({ view: "attention" })).toEqual(["draft", "brief"])
    expect(filter({ view: "series" })).toEqual(["smoke-3", "smoke-2", "smoke-1"])
  })

  it("counts a facet's values with the other facets applied", () => {
    const counts = facetCounts(items, { ...EMPTY_FILTERS, view: "all", kinds: ["project"] })
    expect(counts.kinds).toEqual([["project", 5], ["page", 1], ["word", 1]])
    expect(counts.access).toEqual([["open", 4], ["private", 1]])
  })

  it("sorts and groups", () => {
    expect(sortItems(items.filter(item => !item.empty), "name").map(item => item.slug).slice(0, 3)).toEqual(["brief", "draft", "site"])
    expect(groupItems(items.filter(item => item.slug === "site" || item.slug === "brief"), "folder").map(group => [group.label, group.items.length])).toEqual([["Sales", 1], ["Unfiled", 1]])
  })
})
