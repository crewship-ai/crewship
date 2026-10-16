import { describe, expect, it } from "vitest"
import fixtures from "../../../testdata/page-content-derivation.json"
import { deriveContentTypes, resolvePageContent } from "../content-types"
describe("compiled Page content contract", () => {
 for (const content_descriptor of [undefined, {type_id:"slides",schema_version:1,supported:false,reason:"viewer-unavailable"}, {type_id:"slides",schema_version:1,supported:true}]) it(`keeps Slides foundation readonly ${JSON.stringify(content_descriptor)}`, () => {
  const result=resolvePageContent({human_owned:true,has_slides:true,...(content_descriptor===undefined?{}:{content_descriptor})});expect(result.supported).toBe(false);expect(result.canInitializeContent).toBe(false)
 })
 for (const fixture of fixtures) it(fixture.name, () => expect(deriveContentTypes(fixture.facts)).toEqual(fixture.expected))
 it("keeps empty human Page attach eligibility", () => expect(resolvePageContent({human_owned:true}).canInitializeContent).toBe(true))
 for (const descriptor of [null, {type_id:"future",schema_version:1,supported:true}, {type_id:"doc",schema_version:2,supported:true}, {type_id:"html",schema_version:1,supported:true}]) it(`fails closed for inconsistent primary ${JSON.stringify(descriptor)}`, () => {
  const result=resolvePageContent({human_owned:true,content_descriptor:descriptor});expect(result.supported).toBe(false);expect(result.canInitializeContent).toBe(false)
 })
 it("fails closed for hidden application source", () => {const result=resolvePageContent({human_owned:true,application_descriptor:{type_id:"application",schema_version:1,supported:true}});expect(result.supported).toBe(false);expect(result.canInitializeContent).toBe(false)})
 it("fails closed for omitted application descriptor despite source", () => expect(resolvePageContent({has_project:true,application_descriptor:null}).supported).toBe(false))
 it("retains schema 1 document engine path", () => expect(resolvePageContent({has_document:true,content_descriptor:{type_id:"doc",schema_version:1,supported:true}}).supported).toBe(true))
 it("rejects another registered type in application slot", () => expect(resolvePageContent({has_project:true,application_descriptor:{type_id:"doc",schema_version:2,supported:true}}).supported).toBe(false))
 it("requires application reference with explicit primary", () => expect(resolvePageContent({has_project:true,content_descriptor:{type_id:"panels",schema_version:1,supported:true}}).supported).toBe(false))
 it("keeps draft source without live publication", () => expect(resolvePageContent({has_project:true,has_application:false}).application?.type_id).toBe("application"))
 it("keeps panels with a published application", () => expect(resolvePageContent({has_project:true,has_application:true,has_panels:true}).supported).toBe(true))
})
