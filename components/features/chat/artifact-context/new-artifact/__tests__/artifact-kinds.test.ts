import { describe, expect, it } from "vitest"
import { toPageView, type WirePage } from "@/hooks/use-pages"
import { ARTIFACT_KINDS, artifactKind, artifactKindOfPage, comingKinds, creatableKinds, NO_CAPABILITIES, type ArtifactCapabilities } from "../artifact-kinds"

const ALL: ArtifactCapabilities = { projects: true, dashboards: true, slides: true, documents: true, sheets: true, pages: true }

describe("artifact kind registry", () => {
  it("is one ordered list with the library's keys and families", () => {
    expect(ARTIFACT_KINDS.map(k => [k.key, k.family])).toEqual([
      ["project", "web"], ["dashboard", "web"], ["page", "web"],
      ["docs", "document"], ["word", "document"], ["sheet", "document"], ["slides", "document"], ["design", "document"],
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
    expect(creatableKinds(ALL).map(k => k.key)).toEqual(["project", "dashboard", "page", "word", "sheet", "slides"])
    expect(creatableKinds(NO_CAPABILITIES)).toEqual([])
    expect(creatableKinds({ ...NO_CAPABILITIES, documents: true, pages: true }).map(k => k.key)).toEqual(["page", "word"])
  })

  // G03 (#5153): Docs are the collaborative Doc format, created blank by a person.
  it("offers Docs only when the server confirms the Doc format and the caller may create Pages", () => {
    expect(artifactKind("docs").make).toBe("edit")
    expect(artifactKind("docs").label).toBe("Docs")
    expect(artifactKind("docs").live(ALL)).toBe(false)
    expect(artifactKind("docs").live({ ...ALL, docs: true })).toBe(true)
    expect(artifactKind("docs").live({ ...ALL, docs: true, pages: false })).toBe(false)
    expect(comingKinds(ALL).map(k => k.key)).not.toContain("docs")
    expect(creatableKinds({ ...ALL, docs: true }).map(k => k.key)).toEqual(["project", "dashboard", "page", "docs", "word", "sheet", "slides"])
  })

  it("offers Word only for a Word editor, never for the Doc format that opens as Docs", () => {
    expect(artifactKind("word").live({ ...ALL, docs: true, words: false })).toBe(false)
    expect(artifactKind("word").live({ ...ALL, docs: true, words: true })).toBe(true)
    // An older capability source without the split keeps offering Word.
    expect(artifactKind("word").live(ALL)).toBe(true)
  })

  it("creates Slides through an agent only with projects and the Slides flag", () => {
    expect(artifactKind("slides").make).toBe("agent")
    expect(artifactKind("slides").live(ALL)).toBe(true)
    expect(artifactKind("slides").live({ ...ALL, slides: false })).toBe(false)
    expect(artifactKind("slides").live({ ...ALL, projects: false })).toBe(false)
    expect(comingKinds({ ...ALL, slides: false }).map(k => k.key)).toEqual(["slides"])
  })

  it("needs page creation for every direct kind and the server's project availability for agent kinds", () => {
    expect(artifactKind("word").live({ ...ALL, pages: false })).toBe(false)
    expect(artifactKind("sheet").live({ ...ALL, pages: false })).toBe(false)
    expect(artifactKind("project").live({ ...ALL, projects: false })).toBe(false)
    expect(artifactKind("dashboard").live({ ...ALL, projects: false })).toBe(false)
    expect(artifactKind("dashboard").live({ ...ALL, dashboards: false })).toBe(false)
  })

  it("needs page creation for agent-made kinds too, as the server's create check does (#4534)", () => {
    // A Viewer's availability read says projects are enabled; only the role or page.create may create.
    const viewer: ArtifactCapabilities = { ...ALL, designs: true, pages: false }
    expect(artifactKind("project").live(viewer)).toBe(false)
    expect(artifactKind("dashboard").live(viewer)).toBe(false)
    expect(artifactKind("slides").live(viewer)).toBe(false)
    expect(creatableKinds(viewer)).toEqual([])
    expect(creatableKinds({ ...ALL, designs: true }).map(k => k.key)).toEqual(["project", "dashboard", "page", "word", "sheet", "slides", "design"])
  })

  it("offers Design only with its build capability and Page creation access", () => {
    expect(artifactKind("design").make).toBe("canvas")
    expect(artifactKind("design").live(ALL)).toBe(false)
    expect(artifactKind("design").live({ ...ALL, designs: true })).toBe(true)
    expect(artifactKind("design").live({ ...ALL, designs: true, pages: false })).toBe(false)
    expect(comingKinds(NO_CAPABILITIES).map(k => k.key)).not.toContain("design")
    expect(artifactKindOfPage(toPageView({ id: "canvas", slug: "canvas", name: "Canvas", artifact_kind: "design" } as WirePage))).toBe("design")
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
