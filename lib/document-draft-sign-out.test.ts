// @vitest-environment node
// Browser storage boundary: real IndexedDB transactions, no storage mock.
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"

const seal = `const sealed=s=>JSON.stringify({format:"crewship-local-draft-v1",s});const row=(id,human="alice")=>({id,update:"",attempted:true,schema:1,epoch:"native-v1",snapshot:sealed(id)})`
async function inBrowser<T>(body: string): Promise<T> {
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-queue.test/**",route=>route.fulfill({body:"<!doctype html><title>Document journal</title>",contentType:"text/html"}))
  await page.goto("https://document-queue.test")
  const {outputFiles}=buildSync({stdin:{contents:'export * from "./document-queue";export * from "./document-draft-sign-out"',resolveDir:`${process.cwd()}/lib`,loader:"ts"},bundle:true,write:false,platform:"browser",format:"iife",globalName:"q"})
  await page.addScriptTag({content:outputFiles[0].text})
  return await page.evaluate(`(async()=>{
   Object.defineProperty(navigator,"locks",{value:undefined,configurable:true})
   ${seal}
   const store=await q.openDocumentDrafts("alice","w","p")
   const put=(id,final=false)=>store.put(row(id),{final})
   const ids=async(human="alice")=>(await q.captureDocumentDrafts(human)).map(r=>JSON.parse(r.key[1])[2]).sort()
   const roots=rows=>rows.map(r=>r.root)
   const confirm=async(filter=()=>true)=>{const c=(await q.captureDocumentDrafts("alice")).filter(filter);await q.recordSignOut("alice",roots(c));return c}
   ${body}
  })()`) as T
 }finally{await browser.close()}
}
const ready=existsSync(chromium.executablePath())

it.skipIf(!ready)("a confirmation captured between two final writes covers the chain",async()=>{
 const left=await inBrowser<string[]>(`await put("native:a");await put("native:a",true);const c=await confirm();await put("native:a",true);await q.removeCapturedDocumentDrafts(c);await q.sweepSignOuts();return ids()`)
 expect(left).toEqual([])
})
it.skipIf(!ready)("a holder that redirects before any broadcast stores nothing, the row already erased",async()=>{
 const left=await inBrowser<string[]>(`await put("native:a");const c=await confirm();await q.removeCapturedDocumentDrafts(c);await q.sweepSignOuts();await put("native:a",true);return ids()`)
 expect(left).toEqual([])
})
it.skipIf(!ready)("a final write before the intent, by a holder that then dies, is swept at the next mount",async()=>{
 const left=await inBrowser<{before:string[];after:string[]}>(`await put("native:a");const c=await q.captureDocumentDrafts("alice");await put("native:a",true);await q.recordSignOut("alice",roots(c));await q.removeCapturedDocumentDrafts(c);const before=await ids();await q.sweepSignOuts();return {before,after:await ids()}`)
 expect(left).toEqual({before:["native:a"],after:[]})
})
it.skipIf(!ready)("genuine later work stays, in every order",async()=>{
 const left=await inBrowser<string[]>(`
  await put("native:typed-before-confirming");await put("native:erased-then-edited");await put("native:other-tab");await put("native:new-chain");
  const c=await q.captureDocumentDrafts("alice");
  await put("native:other-tab");await put("native:new-chain");await put("native:new-chain",true);
  await q.recordSignOut("alice",roots(c));await q.removeCapturedDocumentDrafts(c);
  await put("native:erased-then-edited");await put("native:erased-then-edited",true);
  await put("native:after-sign-out");await put("native:after-sign-out",true);
  await q.sweepSignOuts();return ids()`)
 expect(left).toEqual(["native:after-sign-out","native:erased-then-edited","native:new-chain","native:other-tab"])
})
it.skipIf(!ready)("a real edit resets the root",async()=>{
 const left=await inBrowser<string[]>(`await put("native:a",false);await put("native:a",true);await put("native:a",false);await put("native:a",true);const [r]=await q.captureDocumentDrafts("alice");const again=await store.load();return [r.root===r.revision?"same":"chain",String(again.length)]`)
 expect(left).toEqual(["chain","1"])
})
it.skipIf(!ready)("the intent outlives a signed-out mount and identity cleanup, and signing in clears it",async()=>{
 const left=await inBrowser<string[]>(`
  await put("native:a");const c=await confirm();await q.sweepSignOuts();
  await q.suspendOtherDocumentDrafts(null);await q.purgeOtherDocumentDrafts(null);
  await put("native:a",true);const held=(await ids()).join();
  await q.sweepSignOuts("alice");await put("native:b");await put("native:b",true);
  await q.sweepSignOuts("alice");return [held,(await ids()).join()]`)
 expect(left).toEqual(["","native:b"])
})
it.skipIf(!ready)("one person's intent never touches another person's rows",async()=>{
 const left=await inBrowser<string[]>(`
  const bob=await q.openDocumentDrafts("bob","w","p");await bob.put(row("native:b"));
  await put("native:a");const c=await q.captureDocumentDrafts("alice");const b=await q.captureDocumentDrafts("bob");
  await q.recordSignOut("alice",[...roots(c),...roots(b)]);await q.sweepSignOuts();bob.close();return [...await ids("alice"),...await ids("bob")]`)
 expect(left).toEqual(["native:b"])
})
it.skipIf(!ready)("an intent write held up by another transaction gives up within its bound",async()=>{
 const out=await inBrowser<{answer:string;ms:number}>(`
  const db=await new Promise(done=>{const o=indexedDB.open("crewship-document-drafts",1);o.onsuccess=()=>done(o.result)})
  const held=db.transaction("drafts","readwrite").objectStore("drafts");let hold=true;const spin=()=>{if(hold)held.get("x").onsuccess=spin};spin()
  const at=performance.now(),answer=await q.recordSignOut("alice",["r"]).then(()=>"written",()=>"refused");hold=false
  return {answer,ms:performance.now()-at}`)
 expect(out.answer).toBe("refused");expect(out.ms).toBeLessThan(4_000)
})
it.skipIf(!ready)("a sign-in clears only an intent older than its own request, sweeping first",async()=>{
 const out=await inBrowser<{after:string[];laterStays:string[]}>(`
  await put("native:a");const c=await confirm();await put("native:a",true)
  await q.clearSignOut("alice",0)
  const laterStays=await ids()
  await q.clearSignOut("alice",Date.now()+1000)
  await put("native:b");const b=await q.captureDocumentDrafts("alice");await q.removeCapturedDocumentDrafts(b.filter(r=>JSON.parse(r.key[1])[2]==="native:b"));await put("native:b",true)
  return {after:await ids(),laterStays}`)
 expect(out).toEqual({after:["native:b"],laterStays:[]})
})
