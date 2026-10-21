import { describe, expect, it } from "vitest"
import fixtures from "../../internal/pages/content_types_cases.json"
import { contentTypes, lookupContentType, resolveContentReference, deriveContentTypes, resolvePageContent } from "./content-types"
describe("compiled Page content contract", () => {
 for (const content_descriptor of [undefined, {type_id:"slides",schema_version:1,supported:true}]) it(`resolves known Slides format ${JSON.stringify(content_descriptor)}`, () => {
  const result=resolvePageContent({human_owned:true,has_slides:true,...(content_descriptor===undefined?{}:{content_descriptor})});expect(result.supported).toBe(true);expect(result.primary?.viewer).toBe("page-slides");expect(result.canInitializeContent).toBe(false)
 })
 for (const content_descriptor of [{type_id:"slides",schema_version:1,supported:false,reason:"viewer-unavailable"},{type_id:"slides",schema_version:99,supported:true}]) it(`rejects unavailable Slides ${JSON.stringify(content_descriptor)}`, () => expect(resolvePageContent({has_slides:true,content_descriptor}).supported).toBe(false))
 for (const fixture of fixtures) it(fixture.name, () => expect(deriveContentTypes(fixture.facts)).toEqual(fixture.expected))
 it("keeps empty human Page attach eligibility", () => expect(resolvePageContent({human_owned:true}).canInitializeContent).toBe(true))
 for (const descriptor of [null, {type_id:"future",schema_version:1,supported:true}, {type_id:"doc",schema_version:2,supported:true}, {type_id:"html",schema_version:1,supported:true}]) it(`fails closed for inconsistent primary ${JSON.stringify(descriptor)}`, () => {
  const result=resolvePageContent({human_owned:true,content_descriptor:descriptor});expect(result.supported).toBe(false);expect(result.canInitializeContent).toBe(false)
 })
 it("fails closed for hidden application source", () => {const result=resolvePageContent({human_owned:true,application_descriptor:{type_id:"application",schema_version:1,supported:true}});expect(result.supported).toBe(false);expect(result.canInitializeContent).toBe(false)})
 it("fails closed for omitted application descriptor despite source", () => expect(resolvePageContent({has_project:true,application_descriptor:null}).supported).toBe(false))
 it("retains schema 1 document format", () => expect(resolvePageContent({has_document:true,content_descriptor:{type_id:"doc",schema_version:1,supported:true}}).supported).toBe(true))
 it("rejects another registered type in application slot", () => expect(resolvePageContent({has_project:true,application_descriptor:{type_id:"doc",schema_version:2,supported:true}}).supported).toBe(false))
 it("requires application reference with explicit primary", () => expect(resolvePageContent({has_project:true,content_descriptor:{type_id:"panels",schema_version:1,supported:true}}).supported).toBe(false))
 it("keeps draft source without live publication", () => expect(resolvePageContent({has_project:true,has_application:false}).application?.type_id).toBe("application"))
 it("keeps panels with a published application", () => expect(resolvePageContent({has_project:true,has_application:true,has_panels:true}).supported).toBe(true))
})


describe("content registry isolation and trust boundaries", () => {
  it("returns owned descriptor arrays from lists and lookup", () => {
    const original = lookupContentType("panels", 1)!
    const listed = contentTypes()
    listed[0].anchor_kinds[0] = "mutated"
    listed[0].export_formats[0] = "mutated"
    listed[0].viewer = "mutated"
    const found = lookupContentType("panels", 1)!
    found.anchor_kinds[0] = "also-mutated"
    expect(lookupContentType("panels", 1)).toEqual(original)
  })

  it.each([
    undefined, null, false, "panels", [], {},
    { type_id: "panels", schema_version: 1, supported: false },
    { type_id: "panels", schema_version: 1, supported: "true" },
    { type_id: "panels", schema_version: "1", supported: true },
    { type_id: "future", schema_version: 1, supported: true },
    { type_id: "doc", schema_version: 99, supported: true },
  ])("rejects untrusted reference %j", value => {
    expect(resolveContentReference(value)).toBeUndefined()
  })

  it.each(["doc", "board", "slides", "html", "panels", "application"])(
    "rejects unknown versions of %s", type => {
      for (const version of [-1, 0, 99, 1.5, "1", undefined]) {
        expect(lookupContentType(type, version)).toBeUndefined()
      }
    },
  )

  it("honors an explicit unavailable viewer despite legacy presence", () => {
    const result = resolvePageContent({
      has_document: true,
      human_owned: true,
      content_descriptor: {
        type_id: "doc", schema_version: 3, supported: false,
        reason: "viewer-unavailable",
      },
    })
    expect(result.supported).toBe(false)
    expect(result.canInitializeContent).toBe(false)
  })
})
