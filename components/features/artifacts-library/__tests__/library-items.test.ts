import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { kindCounts, libraryItems, repeatedSeries, seriesStem, visibleItems } from "../library-items"

const page = (over: Partial<WirePage>): WirePage => ({ id: over.slug ?? "p", slug: "p", name: "P", owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)

describe("library items", () => {
  it("routes only explicit VersionStore Slides through the artifact viewer", () => {
    const items = libraryItems([
      page({ slug: "typed", artifact_kind: "slides", artifact_shared_version: 2, has_slides: false }),
      page({ slug: "legacy", has_slides: true }),
    ].map(toPageView))
    expect(items.map(item => [item.kind, item.projectBacked, item.native, item.sharedVersion])).toEqual([
      ["slides", true, false, 2], ["slides", false, false, null],
    ])
  })
  it("lists every kind newest first, a panel Page as Page, and hides only empty items", () => {
    const items = libraryItems([
      page({ slug: "site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, updated_at: "2026-10-27T12:00:00Z" }),
      page({ slug: "brief", name: "Partner brief", artifact_kind: "word", has_document: true }),
      page({ slug: "model", name: "Price model", has_workbook: true, artifact_kind: "sheet", artifact_shared_version: 3 }),
      page({ slug: "deck", name: "Board deck", has_slides: true }),
      page({ slug: "weekly", name: "Weekly report", panel_count: 2 } as Partial<WirePage>),
      page({ slug: "notes", name: "Old notes", has_document: true }),
      page({ slug: "app", name: "Calculator", has_application: true } as Partial<WirePage>),
      page({ slug: "empty", name: "Nothing yet" }),
    ].map(toPageView))
    expect(items.map(i => [i.slug, i.kind, i.empty])).toEqual([
      ["site", "project", false], ["brief", "word", false], ["model", "sheet", false], ["deck", "slides", false],
      ["weekly", "page", false], ["notes", "word", false], ["app", "page", true], ["empty", "page", true],
    ])
    expect(visibleItems(items).map(i => i.slug)).toEqual(["site", "brief", "model", "deck", "weekly", "notes"])
    expect(visibleItems(items, "empty").map(i => i.slug)).toEqual(["app", "empty"])
    expect(items.find(i => i.slug === "weekly")?.family).toBe("web")
    expect(items.find(i => i.slug === "model")?.family).toBe("document")
    expect(items[0].sharedVersion).toBe(2)
    expect(items[1].sharedVersion).toBeNull()
    expect(items[1].native).toBe(true)
    expect(items[5].native).toBe(false)
  })

  it("says private for a private audience and never guesses it from ownership", () => {
    const [mine, priv] = libraryItems([page({ slug: "a", artifact_kind: "project" }), page({ slug: "b", artifact_kind: "project", private_audience: true })].map(toPageView))
    expect(mine.private).toBe(false)
    expect(priv.private).toBe(true)
  })

  it("names a crew owner and the caller, never a raw account id", () => {
    const [crew, mine, other] = libraryItems([
      page({ slug: "a", artifact_kind: "project", owner: "crew/research", owner_crew_name: "Research crew", reach: ["crew:research"], updated_at: "2026-10-27T12:00:00Z" } as Partial<WirePage>),
      page({ slug: "b", artifact_kind: "project", owner: "user/cmuxw95k60027db59c6f2", reach: ["owner"], updated_at: "2026-10-27T11:00:00Z" }),
      page({ slug: "c", artifact_kind: "project", owner: "user/cmuxother0000000000000", reach: ["grant"], updated_at: "2026-10-27T10:00:00Z" }),
    ].map(toPageView))
    expect([crew.ownerLabel, mine.ownerLabel, other.ownerLabel]).toEqual(["Research crew", "You", null])
  })

  it("counts kinds for the filter, leaving out kinds with no items", () => {
    const items = libraryItems([page({ slug: "a", artifact_kind: "project" }), page({ slug: "b", artifact_kind: "project" }), page({ slug: "c", has_workbook: true })].map(toPageView))
    expect(kindCounts(items)).toEqual([["project", 2], ["sheet", 1]])
  })
})

it.each(["project", "dashboard", "slides"])("preserves the explicit backend %s as a VersionStore route, never a native lease", artifact_kind => {
  const [item] = libraryItems([toPageView(page({ id: "stable-page", slug: "current-owner", artifact_kind }))])
  expect(item).toMatchObject({ kind: artifact_kind, pageId: "stable-page", projectBacked: true, native: false })
})
it("does not classify legacy panel dashboards or native Slides as VersionStore projects", () => {
  const items = libraryItems([toPageView(page({ panel_count: 1 })), toPageView(page({ has_slides: true }))])
  expect(items.every(item => item.projectBacked === false)).toBe(true)
})

describe("legacy panels", () => {
  it("never calls a panel Page a Dashboard; only the server's kind does", () => {
    const [legacy, typed] = libraryItems([
      page({ slug: "weekly", panel_count: 3, updated_at: "2026-10-27T12:00:00Z" } as Partial<WirePage>),
      page({ slug: "ops", artifact_kind: "dashboard" }),
    ].map(toPageView))
    expect(legacy.kind).toBe("page")
    expect(legacy.projectBacked).toBe(false)
    expect(typed.kind).toBe("dashboard")
  })
})

describe("repeated series", () => {
  it("takes run markers off the name", () => {
    expect(["Smoke test 12", "Smoke test #3", "Smoke test run 4", "Smoke test (2)", "Smoke test 2026-10-27", "Smoke test-2026-10-27T10:00Z", "Board deck v3", "Board deck"].map(seriesStem))
      .toEqual(["Smoke test", "Smoke test", "Smoke test", "Smoke test", "Smoke test", "Smoke test", "Board deck", "Board deck"])
  })

  it("suggests three or more runs of one kind and never touches the rest", () => {
    const items = libraryItems([
      ...[1, 2, 3, 4].map(n => page({ slug: `s${n}`, name: `Smoke test ${n}`, artifact_kind: "project" })),
      page({ slug: "w1", name: "Smoke test 5", artifact_kind: "word" }),
      page({ slug: "d1", name: "Board deck v1", artifact_kind: "slides" }),
      page({ slug: "d2", name: "Board deck v2", artifact_kind: "slides" }),
      page({ slug: "plain", name: "Pricing", artifact_kind: "project" }),
    ].map(toPageView))
    const series = repeatedSeries(items)
    expect(series.map(s => [s.stem, s.kind, s.items.length])).toEqual([["Smoke test", "project", 4]])
    expect(items).toHaveLength(8)
  })
})
