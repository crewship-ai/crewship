import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { ARTIFACT_KINDS, artifactKind, artifactKindOfPage, comingKinds, creatableKinds, NO_CAPABILITIES, type ArtifactCapabilities } from "../artifact-kinds"

const ALL: ArtifactCapabilities = { projects: true, dashboards: true, documents: true, sheets: true, pages: true }

describe("artifact kind registry", () => {
  it("is one ordered list with the library's keys and families", () => {
    expect(ARTIFACT_KINDS.map(k => [k.key, k.family])).toEqual([
      ["project", "web"], ["dashboard", "web"], ["page", "web"],
      ["word", "document"], ["sheet", "document"], ["slides", "document"],
    ])
    for (const k of ARTIFACT_KINDS) {
      expect(k.label && k.plural && k.description && k.viewer).toBeTruthy()
      expect(k.icon).toBeTruthy()
    }
    expect(artifactKind("sheet").uploadExtensions).toEqual([".xlsx", ".csv"])
    expect(artifactKind("word").uploadExtensions).toEqual([".docx"])
    expect(artifactKind("project").uploadExtensions).toEqual([])
    expect(artifactKind("project").make).toBe("agent")
    expect(artifactKind("word").make).toBe("edit")
    expect(artifactKind("page").make).toBe("panels")
  })

  it("creates only kinds that work today, in registry order", () => {
    expect(creatableKinds(ALL).map(k => k.key)).toEqual(["project", "dashboard", "page", "word", "sheet"])
    expect(creatableKinds(NO_CAPABILITIES)).toEqual([])
    expect(creatableKinds({ ...NO_CAPABILITIES, documents: true, pages: true }).map(k => k.key)).toEqual(["page", "word"])
  })

  it("never creates Slides, which waits for live acceptance", () => {
    expect(artifactKind("slides").live(ALL)).toBe(false)
    expect(creatableKinds(ALL).some(k => k.key === "slides")).toBe(false)
    expect(comingKinds(ALL).map(k => k.key)).toEqual(["slides"])
  })

  it("needs page creation for every direct kind and the server's project availability for agent kinds", () => {
    expect(artifactKind("word").live({ ...ALL, pages: false })).toBe(false)
    expect(artifactKind("sheet").live({ ...ALL, pages: false })).toBe(false)
    expect(artifactKind("project").live({ ...ALL, projects: false })).toBe(false)
    expect(artifactKind("dashboard").live({ ...ALL, projects: false })).toBe(false)
    expect(artifactKind("dashboard").live({ ...ALL, dashboards: false })).toBe(false)
  })

  it("reads a Page's kind from what the server says it stores", () => {
    const page = (over: Partial<WirePage>) => toPageView({ id: "p", slug: "p", name: "P", ...over } as WirePage)
    expect(artifactKindOfPage(page({ artifact_kind: "dashboard" }))).toBe("dashboard")
    expect(artifactKindOfPage(page({ has_workbook: true }))).toBe("sheet")
    expect(artifactKindOfPage(page({ has_document: true }))).toBe("word")
    expect(artifactKindOfPage(page({ has_slides: true }))).toBe("slides")
    expect(artifactKindOfPage(page({ panel_count: 2 } as Partial<WirePage>))).toBe("page")
  })
})
