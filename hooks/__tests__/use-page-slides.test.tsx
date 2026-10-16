import { act, renderHook, waitFor, cleanup } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { webcrypto } from "node:crypto"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "@/lib/slides/model"
import { usePageSlides } from "../use-page-slides"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/lib/api-fetch",()=>({apiFetch:vi.fn()}))
vi.stubGlobal("crypto",webcrypto)
const fetch=vi.mocked(apiFetch)
const state=(rev=7)=>({schema_version:1,revision:rev,order_version:3,content_hash:"a".repeat(64),deck:structuredClone(fixture.deck) as SlideDeck})
const json=(value:unknown,status=200)=>new Response(JSON.stringify(value),{status,headers:{"content-type":"application/json"}})
const page=()=>json({id:"fixture_page",can_edit_slides:true})
afterEach(()=>{cleanup();vi.clearAllMocks()})
async function loaded() {
 fetch.mockImplementation(async(url)=>String(url).includes("/slides")?json(state()):page())
 const hook=renderHook(()=>usePageSlides("ws","report","fixture_page"))
 await waitFor(()=>expect(hook.result.current.base?.revision).toBe(7));return hook
}
function edit(deck:SlideDeck) { const next=structuredClone(deck);next.slides[0].notes="unsaved notes";return next }
describe("durable Slides host",()=>{
 it("ignores older reopen completion after newer read and draft",async()=>{
  const h=await loaded();let resolveOld!:(r:Response)=>void
  fetch.mockImplementation(async(url)=>String(url).includes("/slides")?new Promise< Response >(resolve=>{resolveOld=resolve}):page())
  let old!:Promise<void>;act(()=>{old=h.result.current.reopen()})
  fetch.mockImplementation(async(url)=>String(url).includes("/slides")?json(state(9)):page())
  await act(async()=>{await h.result.current.reopen()})
  act(()=>h.result.current.change(edit(h.result.current.draft!)))
  await act(async()=>{resolveOld(json(state(8)));await old})
  expect(h.result.current.base?.revision).toBe(9);expect(h.result.current.draft?.slides[0].notes).toBe("unsaved notes");expect(h.result.current.dirty).toBe(true)
 })
 it("retains original preconditions and exact body after uncertain save",async()=>{
  const h=await loaded();act(()=>h.result.current.change(edit(h.result.current.draft!)))
  let firstBody:string|undefined
  fetch.mockImplementation(async(_url,options)=>{firstBody=options?.body as string;throw new Error("network lost")})
  await act(async()=>{await h.result.current.save()})
  expect(h.result.current.retry).toBe(true);expect(h.result.current.status).toContain("uncertain")
  fetch.mockImplementation(async(url,options)=>{
   if(options?.method==="POST") { expect(options.body).toBe(firstBody);const body=JSON.parse(firstBody!);expect(body.base_rev).toBe(7);expect(body.page_id).toBe("fixture_page");return json({mutation_id:body.mutation_id,revision:8,order_version:3,content_hash:"a".repeat(64),duplicate:true}) }
   const confirmed=state(8);confirmed.deck=edit(confirmed.deck);expect(String(url)).toContain("/revisions/8");return json(confirmed)
  })
  await act(async()=>{await h.result.current.save()})
  expect(h.result.current.status).toContain("Saved revision 8");expect(h.result.current.status).toContain("original request confirmed");expect(h.result.current.dirty).toBe(false)
 })
 for(const code of [403,409])it(`retains draft and never says Saved on ${code}`,async()=>{
  const h=await loaded();act(()=>h.result.current.change(edit(h.result.current.draft!)))
  fetch.mockResolvedValue(json({error:"denied"},code))
  await act(async()=>{await h.result.current.save()})
  expect(h.result.current.blocked).toBe(true);expect(h.result.current.draft?.slides[0].notes).toBe("unsaved notes");expect(h.result.current.status).not.toContain("Saved")
 })
 it("blocks content→reorder and reorder→content until explicit save/discard",async()=>{
  const h=await loaded();act(()=>h.result.current.change(edit(h.result.current.draft!)))
  const reordered=structuredClone(h.result.current.draft!);reordered.slide_order.reverse();act(()=>h.result.current.change(reordered))
  expect(h.result.current.draft?.slide_order).toEqual(fixture.deck.slide_order);expect(h.result.current.error).toContain("before reordering")
  await act(async()=>{await h.result.current.reopen()})
  const order=structuredClone(h.result.current.draft!);order.slide_order.reverse();act(()=>h.result.current.change(order))
  act(()=>h.result.current.change(edit(h.result.current.draft!)))
  expect(h.result.current.orderOnly).toBe(true);expect(h.result.current.draft?.slides[0].notes).toBe(fixture.deck.slides[0].notes);expect(h.result.current.error).toContain("before editing")
 })
})
