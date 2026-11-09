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

  // #4898: the library's own dialog makes a blank Page and opens it; the person
  // who made it must still find it in the default views and by search.
  it("keeps a blank item the caller created and owns in the default views, never another person's", () => {
    const blanks = libraryItems([
      page({ slug: "mine-new", name: "UX walkthrough page" }),
      page({ slug: "mine-old", name: "Old blank" }),
      page({ slug: "theirs", name: "UX walkthrough other", owner: "user/u2", reach: ["role"] }),
    ].map(toPageView))
    const personal = { created: new Set(["mine-new", "theirs"]), opened: new Map([["mine-new", 2], ["mine-old", 1]]) }
    const view = (over: Partial<LibraryFilters>) => applyFilters(blanks, { ...EMPTY_FILTERS, view: "all", ...over }, personal).map(item => item.slug)
    expect(view({})).toEqual(["mine-new"])
    expect(view({ q: "walkthrough" })).toEqual(["mine-new"])
    expect(view({ view: "mine" })).toEqual(["mine-new"])
    expect(view({ view: "opened" })).toEqual(["mine-new"])
    expect(view({ view: "empty" }).sort()).toEqual(["mine-new", "mine-old", "theirs"])
    expect(applyFilters(blanks, { ...EMPTY_FILTERS, view: "all" }).map(item => item.slug)).toEqual([])
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
    expect(filter({ kinds: ["dashboard"] })).toEqual(["weekly"])
    expect(filter({ access: ["private"] })).toEqual(["draft"])
    expect(filter({ version: ["shared"], folder: "sales" })).toEqual(["site"])
    expect(filter({ folder: "", kinds: ["word"] })).toEqual(["brief"])
  })

  // #5132: Needs attention is a real signal, never "not shared yet".
  it("Needs attention lists only items with a real signal, Clean up the series", () => {
    expect(filter({ view: "attention" })).toEqual([])
    const signalled = libraryItems([
      page({ slug: "failed", name: "Ops board", owner: "crew/ops", reach: ["crew:ops"], panel_count: 3, failed_panels: 1 } as Partial<WirePage>),
      page({ slug: "stale-mine", name: "My board", panel_count: 2, stale_panels: 2 } as Partial<WirePage>),
      page({ slug: "stale-theirs", name: "Their board", owner: "crew/ops", reach: ["crew:ops"], panel_count: 2, stale_panels: 2 } as Partial<WirePage>),
      page({ slug: "unshared", name: "Draft site", artifact_kind: "project" }),
    ].map(toPageView))
    const view = (personal = {}) => applyFilters(signalled, { ...EMPTY_FILTERS, view: "attention" }, personal).map(item => item.slug).sort()
    expect(view()).toEqual(["failed", "stale-mine"])
    expect(view({ drafts: new Set(["unshared"]) })).toEqual(["failed", "stale-mine", "unshared"])
    expect(filter({ view: "series" })).toEqual(["smoke-3", "smoke-2", "smoke-1"])
  })

  it("counts a facet's values with the other facets applied", () => {
    const counts = facetCounts(items, { ...EMPTY_FILTERS, view: "all", kinds: ["app"] })
    expect(counts.kinds).toEqual([["dashboard", 1], ["app", 5], ["word", 1]])
    expect(counts.access).toEqual([["open", 4], ["private", 1]])
  })

  it("sorts and groups", () => {
    expect(sortItems(items.filter(item => !item.empty), "name").map(item => item.slug).slice(0, 3)).toEqual(["brief", "draft", "site"])
    expect(groupItems(items.filter(item => item.slug === "site" || item.slug === "brief"), "folder").map(group => [group.label, group.items.length])).toEqual([["Sales", 1], ["Unfiled", 1]])
  })
})

// #5152: one derivation of the real profile for list, counts and filters.
describe("kinds from the selected version's profile", () => {
  const profiled = libraryItems([
    page({ slug: "showcase", name: "Showcase", artifact_kind: "project", artifact_profile: "interactive" } as Partial<WirePage>),
    page({ slug: "tokens", name: "Design system", artifact_kind: "project", artifact_profile: "design_system" } as Partial<WirePage>),
    page({ slug: "sales", name: "Sales", artifact_kind: "dashboard" }),
    page({ slug: "site", name: "Site", artifact_kind: "project" }),
  ].map(toPageView))
  const only = (kinds: LibraryFilters["kinds"]) => applyFilters(profiled, { ...EMPTY_FILTERS, view: "all", kinds }).map(item => item.slug).sort()

  it("files an interactive app under Apps and a design system under Designs, never Dashboards", () => {
    expect(only(["app"])).toEqual(["showcase", "site"])
    expect(only(["design"])).toEqual(["tokens"])
    expect(only(["dashboard"])).toEqual(["sales"])
    expect(facetCounts(profiled, { ...EMPTY_FILTERS, view: "all" }).kinds).toEqual([["dashboard", 1], ["design", 1], ["app", 2]])
    expect(applyFilters(profiled, { ...EMPTY_FILTERS, view: "all", q: "kind:designs" }).map(item => item.slug)).toEqual(["tokens"])
    expect(groupItems(profiled, "kind").map(group => [group.label, group.items.map(item => item.slug).sort()])).toEqual([["Apps", ["showcase", "site"]], ["Designs", ["tokens"]], ["Dashboards", ["sales"]]])
  })

  it("reads older storage words in a saved link as the kind they show as", () => {
    expect(filtersFromParams(new URLSearchParams("kind=project,page,dashboard")).kinds).toEqual(["app", "dashboard"])
    expect(filtersFromParams(new URLSearchParams("kind=apps,design")).kinds).toEqual(["app", "design"])
    // A Page with panels and a Dashboard both show as Dashboards: one kind, not two.
    expect(filtersFromParams(new URLSearchParams("kind=page,dashboard")).kinds).toEqual(["dashboard"])
  })
})

describe("filters in the URL", () => {
  it("round-trips every filter and leaves defaults and other parameters alone", () => {
    const filters: LibraryFilters = { view: "all", q: "kind:word plan", kinds: ["word", "dashboard"], access: ["private"], version: ["unshared"], owners: ["You"], folder: "" }
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
    expect(asObject(serverCatalogParams({ ...all, q: "  Pricing   Site ", access: ["private"], version: ["unshared"], folder: "" }, "name", { design: true })))
      .toEqual({ empty: "exclude", sort: "name", q: "pricing site", access: "private", version: "unshared", folder: "~unfiled" })
    expect(asObject(serverCatalogParams(all, "updated", { design: true }))).toEqual({ empty: "exclude", sort: "updated" })
  })

  it("keeps Design out of the server request while Design is off, as the browser list does", () => {
    expect(serverCatalogParams(all, "updated", { design: false })?.get("kind")).toBe("project,dashboard,page,word,sheet,slides")
  })

  // #5152: the server's kind is storage; UI kinds are derived once, in the browser.
  it("leaves a kind filter and the kind sort to the browser", () => {
    expect(serverCatalogParams({ ...all, kinds: ["app"] }, "updated", { design: true })).toBeNull()
    expect(serverCatalogParams({ ...all, kinds: ["word"] }, "updated", { design: false })).toBeNull()
    expect(serverCatalogParams(all, "kind", { design: true })).toBeNull()
  })

  it("leaves what only the browser can express to the browser", () => {
    expect(serverCatalogParams(EMPTY_FILTERS, "updated", { design: true })).toBeNull()
    for (const view of ["attention", "opened", "mine", "favorites", "empty", "series", "test", "archive"] as const) expect(serverCatalogParams({ ...all, view }, "updated", { design: true })).toBeNull()
    expect(serverCatalogParams({ ...all, owners: ["You"] }, "updated", { design: true })).toBeNull()
    for (const q of ["kind:word", "is:private", "owner:you plan"]) expect(serverCatalogParams({ ...all, q }, "updated", { design: true })).toBeNull()
  })
})
