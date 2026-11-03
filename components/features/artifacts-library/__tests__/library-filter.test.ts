import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { libraryItems, withLibraryState } from "../library-items"
import { applyFilters, EMPTY_FILTERS, facetCounts, filtersFromParams, filtersToParams, groupItems, parseQuery, serverCatalogParams, sortItems, type LibraryFilters } from "../library-filter"

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

  it("shows archived items only in Archive and test items only in Test items, deleting nothing", () => {
    const state = withLibraryState(items, { archived: new Set(["site", "blank"]), test: new Set(["smoke-1", "smoke-2"]), moved: new Map([["brief", { slug: "sales", name: "Sales" }]]) })
    const view = (v: LibraryFilters["view"]) => applyFilters(state, { ...EMPTY_FILTERS, view: v }).map(item => item.slug)
    expect(view("all")).toEqual(["draft", "brief", "weekly", "smoke-3"])
    expect(view("archive")).toEqual(["site", "blank"])
    expect(view("test")).toEqual(["smoke-2", "smoke-1"])
    expect(view("empty")).toEqual([])
    expect(state).toHaveLength(items.length)
    expect(state.find(item => item.slug === "brief")?.folder).toMatchObject({ slug: "sales", name: "Sales" })
    expect(filtersFromParams(new URLSearchParams("view=test")).view).toBe("test")
    expect(filtersFromParams(new URLSearchParams("view=archive")).view).toBe("archive")
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

  it("Needs attention lists made-but-unshared versioned items, Clean up the series", () => {
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

describe("filters in the URL", () => {
  it("round-trips every filter and leaves defaults and other parameters alone", () => {
    const filters: LibraryFilters = { view: "all", q: "kind:word plan", kinds: ["word", "page"], access: ["private"], version: ["unshared"], owners: ["You"], folder: "" }
    const params = filtersToParams(filters, new URLSearchParams("workspace_id=ws1&item=brief"))
    expect(params.get("workspace_id")).toBe("ws1")
    expect(params.get("item")).toBe("brief")
    expect(filtersFromParams(params)).toEqual(filters)
    expect(filtersToParams(EMPTY_FILTERS, new URLSearchParams("workspace_id=ws1&view=all&q=x")).toString()).toBe("workspace_id=ws1")
  })

  it("drops unknown values instead of guessing", () => {
    expect(filtersFromParams(new URLSearchParams("view=trash&kind=word,bogus&access=public&version=old"))).toEqual({ ...EMPTY_FILTERS, kinds: ["word"] })
  })
})

describe("personal views", () => {
  const personal = { opened: new Map([["brief", 3000], ["site", 2000], ["blank", 9000]]), favorites: new Set(["weekly", "gone"]) }
  const view = (v: LibraryFilters["view"]) => applyFilters(items, { ...EMPTY_FILTERS, view: v }, personal).map(item => item.slug)

  it("Recently opened lists what this person opened, newest open first, never an empty item", () => {
    expect(view("opened")).toEqual(["brief", "site"])
  })

  it("Mine lists what the person owns; Favorites what they starred and can still read", () => {
    expect(view("mine")).not.toContain("brief")
    expect(view("mine")).toContain("site")
    expect(view("favorites")).toEqual(["weekly"])
  })

  it("personal views are empty without a record, never everything", () => {
    expect(applyFilters(items, { ...EMPTY_FILTERS, view: "opened" }).length).toBe(0)
    expect(applyFilters(items, { ...EMPTY_FILTERS, view: "favorites" }).length).toBe(0)
  })

  it("the opened and favorite views survive the URL", () => {
    for (const v of ["opened", "mine", "favorites"] as const) expect(filtersFromParams(filtersToParams({ ...EMPTY_FILTERS, view: v })).view).toBe(v)
  })
})

describe("serverCatalogParams", () => {
  const all: LibraryFilters = { ...EMPTY_FILTERS, view: "all" }
  const asObject = (params: URLSearchParams | null) => params && Object.fromEntries(params)

  it("maps the All view's plain filters to the server catalog, leaving empty items out as the view does", () => {
    expect(asObject(serverCatalogParams({ ...all, q: "  Pricing   Site ", kinds: ["project", "word"], access: ["private"], version: ["unshared"], folder: "" }, "name", { design: true })))
      .toEqual({ empty: "exclude", sort: "name", q: "pricing site", kind: "project,word", access: "private", version: "unshared", folder: "~unfiled" })
    expect(asObject(serverCatalogParams(all, "updated", { design: true }))).toEqual({ empty: "exclude", sort: "updated" })
  })

  it("keeps Design out of the server request while Design is off, as the browser list does", () => {
    expect(serverCatalogParams(all, "kind", { design: false })?.get("kind")).toBe("project,dashboard,page,word,sheet,slides")
    expect(serverCatalogParams({ ...all, kinds: ["design", "word"] }, "kind", { design: false })?.get("kind")).toBe("word")
    expect(serverCatalogParams({ ...all, kinds: ["design"] }, "kind", { design: false })).toBeNull()
  })

  it("leaves what only the browser can express to the browser", () => {
    expect(serverCatalogParams(EMPTY_FILTERS, "updated", { design: true })).toBeNull()
    for (const view of ["attention", "opened", "mine", "favorites", "empty", "series", "test", "archive"] as const) expect(serverCatalogParams({ ...all, view }, "updated", { design: true })).toBeNull()
    expect(serverCatalogParams({ ...all, owners: ["You"] }, "updated", { design: true })).toBeNull()
    for (const q of ["kind:word", "is:private", "owner:you plan"]) expect(serverCatalogParams({ ...all, q }, "updated", { design: true })).toBeNull()
  })
})
