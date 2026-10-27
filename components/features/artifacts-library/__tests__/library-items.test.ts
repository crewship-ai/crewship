import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { kindCounts, libraryItems } from "../library-items"

const page = (over: Partial<WirePage>): WirePage => ({ id: over.slug ?? "p", slug: "p", name: "P", owner: "user/u1", updated_at: "2026-10-27T10:00:00Z", reach: ["owner"], ...over } as WirePage)

describe("library items", () => {
  it("lists only kinds that have a backend, newest first, with the shared version", () => {
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
    expect(items.map(i => [i.slug, i.kind])).toEqual([
      ["site", "project"], ["brief", "word"], ["model", "sheet"], ["deck", "slides"], ["weekly", "dashboard"], ["notes", "word"],
    ])
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

  it("counts kinds for the filter, leaving out kinds with no items", () => {
    const items = libraryItems([page({ slug: "a", artifact_kind: "project" }), page({ slug: "b", artifact_kind: "project" }), page({ slug: "c", has_workbook: true })].map(toPageView))
    expect(kindCounts(items)).toEqual([["project", 2], ["sheet", 1]])
  })
})
