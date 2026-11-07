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

const refusingOpen=`const refuseOpen=async(action)=>{const open=IDBFactory.prototype.open;IDBFactory.prototype.open=function(){throw new DOMException("refused","UnknownError")};try{return await action()}finally{IDBFactory.prototype.open=open}};const intentFails=c=>refuseOpen(()=>q.recordSignOut("alice",roots(c)).then(()=>false,()=>true))`
it.skipIf(!ready)("a failed intent write still rejects, and a holder writing after it stores nothing",async()=>{
 const left=await inBrowser<{failed:boolean;after:string[]}>(`${refusingOpen};await put("native:a");const c=await q.captureDocumentDrafts("alice");const failed=await intentFails(c);await q.removeCapturedDocumentDrafts(c);await put("native:a",true);return {failed,after:await ids()}`)
 expect(left).toEqual({failed:true,after:[]})
})
it.skipIf(!ready)("a final write landing before the failed intent is swept at the next mount, and the intent reaches the store",async()=>{
 const left=await inBrowser<{before:string[];after:string[];moved:boolean}>(`${refusingOpen};await put("native:a");const c=await q.captureDocumentDrafts("alice");await put("native:a",true);await intentFails(c);await q.removeCapturedDocumentDrafts(c);const before=await ids();await q.sweepSignOuts();return {before,after:await ids(),moved:localStorage.length===0}`)
 expect(left).toEqual({before:["native:a"],after:[],moved:true})
})
it.skipIf(!ready)("genuine later work survives a failed intent",async()=>{
 const left=await inBrowser<string[]>(`${refusingOpen};await put("native:a");const c=await q.captureDocumentDrafts("alice");await intentFails(c);await q.removeCapturedDocumentDrafts(c);await put("native:a");await put("native:a",true);await q.sweepSignOuts();return ids()`)
 expect(left).toEqual(["native:a"])
})
it.skipIf(!ready)("a sign-in ends an older failed intent, and a sign-out after that sign-in outlives it",async()=>{
 const left=await inBrowser<{older:string[];newer:string[]}>(`${refusingOpen};
  await put("native:a");let c=await q.captureDocumentDrafts("alice");await intentFails(c);await q.removeCapturedDocumentDrafts(c);
  await q.clearSignOut("alice",Date.now()+1000);await put("native:a",true);const older=await ids();
  c=await q.captureDocumentDrafts("alice");await intentFails(c);await q.removeCapturedDocumentDrafts(c);
  await q.clearSignOut("alice",0);await put("native:a",true);return {older,newer:await ids()}`)
 expect(left).toEqual({older:["native:a"],newer:[]})
})
it.skipIf(!ready)("a transfer of an older record leaves a newer confirmation that the store refused, through a concurrent sign-in",async()=>{
 const left=await inBrowser<{pending:string[];failed:boolean;after:string[]}>(`${refusingOpen};
  await put("native:a");const c=await q.captureDocumentDrafts("alice");await intentFails(c);await q.removeCapturedDocumentDrafts(c);
  await new Promise(done=>setTimeout(done,5));const signIn=Date.now();await new Promise(done=>setTimeout(done,5));
  // While the old record is in its store transaction, another tab confirms a newer sign-out whose write is refused.
  const put_=IDBObjectStore.prototype.put;let failed=false,newer
  IDBObjectStore.prototype.put=function(value){IDBObjectStore.prototype.put=put_;newer=intentFails(c);return put_.call(this,value)}
  await q.clearSignOut("alice",signIn);failed=await newer
  const pending=q.pendingSignOutRoots("alice");await put("native:a",true);return {pending,failed,after:await ids()}`)
 expect(left.failed).toBe(true);expect(left.pending).toHaveLength(1);expect(left.after).toEqual([])
})
it.skipIf(!ready)("merging into the store keeps the newer confirmation time, so a sign-in between them does not end it",async()=>{
 const left=await inBrowser<string[]>(`
  await put("native:a");const c=await confirm();await q.removeCapturedDocumentDrafts(c);
  await new Promise(done=>setTimeout(done,5));const signIn=Date.now();await new Promise(done=>setTimeout(done,5));
  await q.recordSignOut("alice",roots(c));
  localStorage.setItem("crewship.sign-out-intent:old",JSON.stringify({human:"alice",at:1,roots:roots(c)}));
  await q.clearSignOut("alice",signIn);await put("native:a",true);return ids()`)
 expect(left).toEqual([])
})
it.skipIf(!ready)("a confirmation after a refused one stores both root sets and their newest time, before its record goes",async()=>{
 const left=await inBrowser<{after:string[];pending:string[]}>(`${refusingOpen};
  await put("native:a");await put("native:b");const c=await q.captureDocumentDrafts("alice");
  const a=c.filter(r=>JSON.parse(r.key[1])[2]==="native:a"),b=c.filter(r=>JSON.parse(r.key[1])[2]==="native:b");
  await intentFails(a);await q.recordSignOut("alice",roots(b));await q.removeCapturedDocumentDrafts(c);
  const pending=q.pendingSignOutRoots("alice");await put("native:a",true);await q.sweepSignOuts();return {after:await ids(),pending}`)
 expect(left).toEqual({after:[],pending:[]})
})
it.skipIf(!ready)("another tab confirming between a transfer's last look and its removal keeps its record",async()=>{
 const left=await inBrowser<{pending:string[];after:string[]}>(`${refusingOpen};
  await put("native:a");const c=await q.captureDocumentDrafts("alice");await intentFails(c);await q.removeCapturedDocumentDrafts(c);
  await new Promise(done=>setTimeout(done,5));const signIn=Date.now();await new Promise(done=>setTimeout(done,5));
  // The other tab runs in the gap: right after this tab's last read, or right before its removal.
  let armed=false,newer;const other=()=>{if(!armed)return;armed=false;const open=IDBFactory.prototype.open;IDBFactory.prototype.open=function(){throw new DOMException("refused","UnknownError")};newer=q.recordSignOut("alice",roots(c)).catch(()=>{});IDBFactory.prototype.open=open}
  const {getItem,removeItem}=Storage.prototype,put_=IDBObjectStore.prototype.put
  IDBObjectStore.prototype.put=function(value){IDBObjectStore.prototype.put=put_;const asked=put_.call(this,value);armed=true;return asked}
  Storage.prototype.getItem=function(key){const value=getItem.call(this,key);if(String(key).startsWith("crewship.sign-out-intent:"))other();return value}
  Storage.prototype.removeItem=function(key){other();return removeItem.call(this,key)}
  await q.clearSignOut("alice",signIn);await newer;Storage.prototype.getItem=getItem;Storage.prototype.removeItem=removeItem
  const pending=q.pendingSignOutRoots("alice");await put("native:a",true);return {pending,after:await ids()}`)
 expect(left.pending).toHaveLength(1);expect(left.after).toEqual([])
})
it.skipIf(!ready)("with both stores refusing, the intent write still only rejects",async()=>{
 const failed=await inBrowser<boolean>(`${refusingOpen};await put("native:a");const c=await q.captureDocumentDrafts("alice");Storage.prototype.setItem=()=>{throw new DOMException("full","QuotaExceededError")};return intentFails(c)`)
 expect(failed).toBe(true)
})
