import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { libraryItems, LIBRARY_KINDS } from "../library-items"
import { KIND_ORDER, KINDS, kindOf, kindText, uiKindCounts, type ContentType, type KindSource, type ResolvedKind } from "../kinds"

describe("kindOf", () => {
  // Every storage kind the library knows, with and without the flags that change it.
  const cases: [string, KindSource, ResolvedKind][] = [
    ["project without an application is a static App", { kind: "project" }, { kind: "app", badge: "Static" }],
    ["project with a published application is an App", { kind: "project", hasApplication: true }, { kind: "app" }],
    ["legacy Page with panels and an application (Page · ops) is an App", { kind: "page", hasApplication: true }, { kind: "app" }],
    ["legacy Page without an application (data panels) is a Dashboard", { kind: "page" }, { kind: "dashboard" }],
    ["dashboard", { kind: "dashboard" }, { kind: "dashboard" }],
    ["slides", { kind: "slides" }, { kind: "slides" }],
    ["design is a Canvas design", { kind: "design" }, { kind: "design", mode: "canvas" }],
    ["design system manifest is a Design Library", { kind: "project", designSystem: true }, { kind: "design", mode: "library", badge: "Library" }],
    ["native Word is Craft Word", { kind: "word", native: true }, { kind: "word" }],
    ["legacy doc (Word storage without a version head) is Docs", { kind: "word", native: false }, { kind: "docs", mode: "page" }],
    ["Sheet", { kind: "sheet", native: true }, { kind: "sheet" }],
    ["legacy workbook is a Sheet", { kind: "sheet" }, { kind: "sheet" }],
  ]
  it.each(cases)("%s", (_name, source, expected) => {
    expect(kindOf(source)).toEqual(expected)
  })

  const content: [ContentType, KindSource, ResolvedKind][] = [
    ["board", { kind: "page" }, { kind: "docs", mode: "canvas", badge: "Canvas" }],
    ["doc", { kind: "page" }, { kind: "docs", mode: "page" }],
    ["doc", { kind: "word", native: true }, { kind: "word" }],
    ["slides", { kind: "page" }, { kind: "slides" }],
    ["gridcraft-workbook", { kind: "page" }, { kind: "sheet" }],
    ["html", { kind: "page" }, { kind: "app", badge: "Static" }],
    ["application", { kind: "page" }, { kind: "app" }],
    ["mooring-app", { kind: "page" }, { kind: "app" }],
    ["panels", { kind: "page" }, { kind: "dashboard" }],
    ["panels", { kind: "page", hasApplication: true }, { kind: "app" }],
  ]
  it.each(content)("content type %s (%o)", (contentType, source, expected) => {
    expect(kindOf({ ...source, contentType })).toEqual(expected)
  })

  it("names every LibraryKind with a registry kind, never Page", () => {
    for (const { kind } of LIBRARY_KINDS) {
      for (const native of [true, false]) {
        const resolved = kindOf({ kind, native })
        expect(KINDS[resolved.kind]).toBeTruthy()
        expect(kindText(resolved)).not.toMatch(/\bPages?\b/)
      }
    }
    expect(Object.values(KINDS).map(entry => entry.label).join(" ")).not.toMatch(/\bPages?\b/)
  })

  it("gives every kind a lower-case noun for sentences, never Page", () => {
    for (const kind of KIND_ORDER) {
      expect(KINDS[kind].noun).toMatch(/^[a-z]+$/)
      expect(KINDS[kind].noun).not.toMatch(/^pages?$/)
    }
    expect(KINDS.docs.noun).toBe("document")
    expect(KINDS.word.noun).toBe("document")
    expect(KINDS.sheet.noun).toBe("sheet")
  })

  it("groups the registry into Artifact and Craft families", () => {
    expect(KIND_ORDER.filter(kind => KINDS[kind].family === "Artifact")).toEqual(["docs", "dashboard", "slides", "design", "app"])
    expect(KIND_ORDER.filter(kind => KINDS[kind].family === "Craft")).toEqual(["word", "sheet"])
  })

  it("maps the catalog the server sends (dev seed shapes)", () => {
    const wire = (over: Partial<WirePage>) => ({ id: over.slug, owner: "user/u1", reach: ["owner"], ...over }) as WirePage
    const items = libraryItems([
      wire({ slug: "landing-page", name: "Landing", artifact_kind: "design" }),
      wire({ slug: "deck", name: "Deck", artifact_kind: "slides" }),
      wire({ slug: "roi", name: "ROI", artifact_kind: "project" }),
      wire({ slug: "sales", name: "Sales", artifact_kind: "dashboard" }),
      wire({ slug: "demo-live", name: "Demo live", has_application: true, panel_count: 2 } as Partial<WirePage>),
      wire({ slug: "brief", name: "Brief", artifact_kind: "word" }),
      wire({ slug: "old-doc", name: "Old doc", has_document: true }),
    ].map(toPageView))
    const bySlug = Object.fromEntries(items.map(item => [item.slug, kindText(kindOf(item))]))
    expect(bySlug).toEqual({
      "landing-page": "Design", deck: "Slides", roi: "App · Static", sales: "Dashboard", "demo-live": "App", brief: "Word", "old-doc": "Docs",
    })
    expect(uiKindCounts(items)).toEqual([["docs", 1], ["dashboard", 1], ["slides", 1], ["design", 1], ["app", 2], ["word", 1]])
  })
})
