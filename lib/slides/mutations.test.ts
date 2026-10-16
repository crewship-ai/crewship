import { describe, expect, it, vi } from "vitest"
import { webcrypto } from "node:crypto"
import fixture from "@/testdata/slides-schema-v1.json"
import { planSlidesMutation, type SlidesState } from "./mutations"
import type { SlideDeck } from "./model"
import { insertSlide } from "@/components/features/slides/native-model"
vi.stubGlobal("crypto", webcrypto)
const base = (): SlidesState => ({ schema_version: 1, revision: 7, order_version: 3, content_hash: "a".repeat(64), deck: structuredClone(fixture.deck) as SlideDeck })
describe("immutable Slides mutation planning", () => {
 it("preserves original Page/revision and real canonical element hash", async () => {
  const saved = base(), draft = structuredClone(saved.deck), element = draft.slides[0].elements[0]
  if (element.type !== "text") throw new Error("text fixture missing")
  element.text.value = "Changed"
  const request = await planSlidesMutation(saved, draft, "mutation_a")
  expect(request.path).toBe("/patch"); expect(request.body).toMatchObject({page_id:saved.deck.deck_id,base_rev:7,if_order_version:3,mutation_id:"mutation_a"})
  expect(request.body.operations).toHaveLength(1)
  expect(request.body.operations?.[0]).toMatchObject({op:"set_element",slide_id:"slide_b",element_id:"title_b",if_hash:'7829f42a3ecfdb29493b2c0ffc713ca466549e30204c15f6fdb7d3096e682ba5'})
  expect(saved.deck.slides[0].elements[0]).not.toEqual(element)
 })
 it("advances slide hashes between multiple notes/insert operations", async () => {
  const saved=base(), draft=structuredClone(saved.deck), slide=draft.slides[0]
  slide.notes="New notes"; const element=structuredClone(slide.elements[0]); element.id="new_element"; slide.elements.push(element)
  const request=await planSlidesMutation(saved,draft,"mutation_b")
  expect(request.body.operations?.map(op=>op.op)).toEqual(["set_notes","insert_element"])
  expect(request.body.operations?.[0].if_hash).not.toEqual(request.body.operations?.[1].if_hash)
 })
 it("uses a single reorder request and keeps stable IDs",async()=>{
  const saved=base(), draft=structuredClone(saved.deck);draft.slide_order.reverse()
  const request=await planSlidesMutation(saved,draft,"reorder")
  expect(request).toEqual({path:"/reorder",body:{page_id:saved.deck.deck_id,schema_version:1,base_rev:7,mutation_id:"reorder",if_order_version:3,slide_order:draft.slide_order}})
 })
 it("rejects mixed reorder/content and foreign Page without changing saved state",async()=>{
  const saved=base(), draft=structuredClone(saved.deck);draft.slide_order.reverse();draft.slides[0].notes="new"
  await expect(planSlidesMutation(saved,draft,"mixed")).rejects.toThrow("before reordering")
  draft.deck_id="foreign";await expect(planSlidesMutation(saved,draft,"foreign")).rejects.toThrow("identity")
  expect(saved.deck.deck_id).toBe("fixture_page");expect(saved.revision).toBe(7)
 })
 it("batches duplicate slide into a structural patch with order precondition",async()=>{
  const saved=base(), draft=insertSlide(saved.deck,"slide_b",true).deck
  const request=await planSlidesMutation(saved,draft,"duplicate")
  expect(request.body.operations).toHaveLength(1);expect(request.body.operations?.[0]).toMatchObject({op:"insert_slide",position:1});expect(request.body.if_order_version).toBe(3)
 })
})

describe("schema-aware private image plans", () => {
 const image = () => ({id:"image_one",type:"image" as const,x:24,y:24,width:160,height:100,image:{asset_id:"asset_one",expected_sha256:"a".repeat(64),alt:"Native image"}})
 it("upgrades only explicit first image using the original whole-head hash and schema",async()=>{const saved=base(),draft=structuredClone(saved.deck);draft.schema_version=2;draft.slides[0].elements.push(image());const planned=await planSlidesMutation(saved,draft,"first_image");expect(planned.body).toMatchObject({schema_version:1,base_rev:7,page_id:saved.deck.deck_id,target_schema_version:2,if_content_hash:saved.content_hash});expect(saved.deck.schema_version).toBe(1)})
 it("retains schema2 and uses image-element v2 hash for edit/delete",async()=>{const saved=base();saved.schema_version=2;saved.deck.schema_version=2;saved.deck.slides[0].elements.push(image());const draft=structuredClone(saved.deck),element=draft.slides[0].elements.at(-1)!;if(element.type!=="image")throw new Error("fixture");element.image.alt="Updated";const request=await planSlidesMutation(saved,draft,"image_edit");expect(request.body.schema_version).toBe(2);expect(request.body.target_schema_version).toBeUndefined();expect(request.body.operations?.[0].op).toBe("set_element");const removed=structuredClone(saved.deck);removed.slides[0].elements.pop();const deletion=await planSlidesMutation(saved,removed,"image_delete");expect(deletion.body.operations?.[0].if_hash).toBe(request.body.operations?.[0].if_hash)})
 it("refuses generic downgrade and an image-free first upgrade",async()=>{const saved=base(),empty=structuredClone(saved.deck);empty.schema_version=2;await expect(planSlidesMutation(saved,empty,"invalid_upgrade")).rejects.toThrow("must attach an image");saved.schema_version=2;saved.deck.schema_version=2;const old=structuredClone(saved.deck);old.schema_version=1;await expect(planSlidesMutation(saved,old,"downgrade")).rejects.toThrow("explicit history restore")})
})
