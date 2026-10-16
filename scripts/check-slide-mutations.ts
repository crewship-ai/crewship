// Execute through crewship-heavy pnpm exec tsx scripts/check-slide-mutations.ts.
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"
import { readFileSync } from "node:fs"
import { canonicalSlideDeck, type SlideDeck } from "../lib/slides/model"
import { planSlidesMutation, type SlidesState } from "../lib/slides/mutations"
import { insertSlide, newElement } from "../components/features/slides/native-model"
async function main() {
 const deck = JSON.parse(readFileSync("testdata/slides-schema-v1.json","utf8")).deck as SlideDeck
 const base:SlidesState={schema_version:1,revision:7,order_version:3,content_hash:"a".repeat(64),deck}
 const edited=structuredClone(deck), first=edited.slides[0]
 first.notes="Updated notes 🐎";if(first.elements[0].type!=="text")throw new Error("fixture");first.elements[0].text.value="Literal \\u2028 and real \u2028/\u2029 🐎"
 first.elements.splice(1,1);first.elements.push(newElement(edited,"shape"));first.elements.push(newElement(edited,"text"))
 const duplicate=insertSlide(deck,"slide_b",true).deck
 const deleted=structuredClone(deck);deleted.slides=deleted.slides.filter(s=>s.id!=="slide_a");deleted.slide_order=deleted.slide_order.filter(id=>id!=="slide_a")
 const drafts=[edited,duplicate,deleted], requests=[]
 for(const [index,draft] of drafts.entries()){const attempt=await planSlidesMutation(base,draft,`contract_${index}`);assert.equal(attempt.path,"/patch");requests.push({deck,request:attempt.body})}
 const actual=JSON.parse(execFileSync("go",["run","./scripts/slide-mutation-contract"],{input:JSON.stringify(requests),encoding:"utf8"}))
 assert.deepEqual(actual,drafts.map(canonicalSlideDeck))
 console.log("Slides TS planner → actual Go strict decode/apply/canonical:3 cases, all6 semantic operation families, progressive hashes and Unicode PASS")
}
void main()
