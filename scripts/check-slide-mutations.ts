// Execute through crewship-heavy pnpm exec tsx scripts/check-slide-mutations.ts.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { readFileSync } from "node:fs"
import { canonicalSlideDeck, slideDeckHashDomain, type SlideDeck } from "../lib/slides/model"
import { planSlidesMutation, type SlidesState } from "../lib/slides/mutations"
import { insertSlide, newElement } from "../components/features/slides/native-model"
async function state(deck: SlideDeck, revision: number): Promise<SlidesState> {
 const digest=await crypto.subtle.digest("SHA-256",new TextEncoder().encode(slideDeckHashDomain(deck.schema_version)+canonicalSlideDeck(deck)))
 return {schema_version:deck.schema_version,revision,order_version:3,content_hash:Array.from(new Uint8Array(digest),b=>b.toString(16).padStart(2,"0")).join(""),deck}
}
async function main() {
 const deck = JSON.parse(readFileSync("testdata/slides-schema-v1.json","utf8")).deck as SlideDeck, base=await state(deck,7)
 const edited=structuredClone(deck), first=edited.slides[0]
 first.notes="Updated notes 🐎";if(first.elements[0].type!=="text")throw new Error("fixture");first.elements[0].text.value="Literal \\u2028 and real \u2028/\u2029 🐎"
 first.elements.splice(1,1);first.elements.push(newElement(edited,"shape"));first.elements.push(newElement(edited,"text"))
 const duplicate=insertSlide(deck,"slide_b",true).deck
 const deleted=structuredClone(deck);deleted.slides=deleted.slides.filter(s=>s.id!=="slide_a");deleted.slide_order=deleted.slide_order.filter(id=>id!=="slide_a")
 const upgraded=structuredClone(deck);upgraded.schema_version=2;upgraded.slides[0].elements.push({id:"image_contract",type:"image",x:24,y:24,width:160,height:100,image:{asset_id:"asset_contract",expected_sha256:"a".repeat(64),alt:"Literal image <script> text 🐎"}})
 const imageBase=await state(upgraded,8), imageEdit=structuredClone(upgraded);const image=imageEdit.slides[0].elements.find(e=>e.type==="image")!;if(image.type!=="image")throw new Error("image");image.image.alt="Updated 🐎";image.x=40
 const imageDelete=structuredClone(upgraded);imageDelete.slides[0].elements=imageDelete.slides[0].elements.filter(e=>e.type!=="image")
 const cases=[{base,draft:edited},{base,draft:duplicate},{base,draft:deleted},{base,draft:upgraded},{base:imageBase,draft:imageEdit},{base:imageBase,draft:imageDelete}],requests=[]
 for(const [index,item]of cases.entries()){const attempt=await planSlidesMutation(item.base,item.draft,`contract_${index}`);assert.equal(attempt.path,"/patch");requests.push({deck:item.base.deck,request:attempt.body})}
 const actual=JSON.parse(execFileSync("go",["run","./scripts/slide-mutation-contract"],{input:JSON.stringify(requests),encoding:"utf8"}))
 assert.deepEqual(actual,cases.map(item=>canonicalSlideDeck(item.draft)))
 console.log("Slides TS planner → actual Go strict decode/apply/canonical:6 cases, all6 operation families, exact1→2 upgrade, image v2 hashes, progressive hashes and Unicode PASS")
}
void main()
