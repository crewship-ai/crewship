// @vitest-environment node
// Browser storage boundary: real IndexedDB transactions, no storage mock.
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"

// Frontend unit shards do not provision browsers. Browser qualification runs
// this boundary when Chromium is installed; a skip is not acceptance evidence.
it.skipIf(!existsSync(chromium.executablePath()))("isolates durable drafts by human/workspace/Page and deletes only the receipted mutation",async()=>{
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-queue.test/**",route=>route.fulfill({body:"<!doctype html><title>Document journal</title>",contentType:"text/html"}))
  await page.goto("https://document-queue.test")
  const {outputFiles}=buildSync({entryPoints:[`${process.cwd()}/lib/document-queue.ts`],bundle:true,write:false,platform:"browser",format:"iife",globalName:"documentQueue"})
  await page.addScriptTag({content:outputFiles[0].text})
  const result=await page.evaluate(async()=>{
   // This case verifies the supported non-exclusive fallback's row isolation.
   // Real Web Locks refuse the second writer (covered by the custody tests).
   Object.defineProperty(navigator,"locks",{value:undefined,configurable:true})
   const {openDocumentDrafts}= (window as unknown as {documentQueue: typeof import("./document-queue")}).documentQueue
   const original=await openDocumentDrafts("alice","workspace","page")
   const draft={id:"mutation",update:"exact-update",attempted:true,schema:3,epoch:"original-grant",snapshot:"draft",baselineRev:4,request:{id:"mutation",update:"exact-update",schema:3,epoch:"original-grant",kind:"human",page_id:"page"}}
   await original.put(draft);original.close()
   const same=await openDocumentDrafts("alice","workspace","page")
   const reloaded=await same.load()
   const unrelated=await Promise.all([
    openDocumentDrafts("bob","workspace","page"),
    openDocumentDrafts("alice","other-workspace","page"),
    openDocumentDrafts("alice","workspace","other-page"),
   ])
   const others=await Promise.all(unrelated.map(store=>store.load()))
   const tab=await openDocumentDrafts("alice","workspace","page")
   await tab.put({...draft,id:"another-tab",request:{...draft.request,id:"another-tab"}})
   await same.remove("mutation")
   const remaining=await same.load()
   same.close();tab.close();unrelated.forEach(store=>store.close())
   return {reloaded,others,remaining,exclusive:same.exclusive}
  })
  expect(result.exclusive).toBe(false)
  expect(result.reloaded).toHaveLength(1)
  expect(result.reloaded[0].request?.epoch).toBe("original-grant")
  expect(result.others).toEqual([[],[],[]])
  expect(result.remaining.map(row=>row.id)).toEqual(["another-tab"])
 }finally{await browser.close()}
})

// #5104: a take-over that gives up on an open must not keep this Page's lock
// or IDB connection, or the next attempt and other tabs find storage taken.
it.skipIf(!existsSync(chromium.executablePath()))("a cancelled exclusive open releases the Page lock and closes a late IDB connection",async()=>{
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-queue.test/**",route=>route.fulfill({body:"<!doctype html><title>Document journal</title>",contentType:"text/html"}))
  await page.goto("https://document-queue.test")
  const {outputFiles}=buildSync({entryPoints:[`${process.cwd()}/lib/document-queue.ts`],bundle:true,write:false,platform:"browser",format:"iife",globalName:"documentQueue"})
  await page.addScriptTag({content:outputFiles[0].text})
  const result=await page.evaluate(async()=>{
   const {openDocumentDrafts}= (window as unknown as {documentQueue: typeof import("./document-queue")}).documentQueue
   const outcome=(p:Promise<unknown>)=>p.then(()=>"opened",(e:Error)=>e.constructor.name)
   const pageLockHeld=async()=>((await navigator.locks.query()).held??[]).some(l=>String(l.name).startsWith("crewship-document-drafts:"))
   const exclusiveNow=async()=>{const s=await openDocumentDrafts("alice","w","page",{requireExclusive:true});const e=s.exclusive;s.close();return e}
   // 1. Already abandoned: nothing is opened.
   const before=new AbortController();before.abort()
   const preAborted=await outcome(openDocumentDrafts("alice","w","page",{requireExclusive:true,signal:before.signal}))
   // 2. Abandoned while the lock request is pending (the grant arrives asynchronously).
   const early=new AbortController(),pending=openDocumentDrafts("alice","w","page",{requireExclusive:true,signal:early.signal});early.abort()
   const duringLock=await outcome(pending)
   const lockAfterLockAbort=await pageLockHeld()
   // 3. Abandoned while IDB opens: hold the open request, abort, then deliver a late success.
   const realOpen=indexedDB.open.bind(indexedDB)
   let held:{request:Record<string,unknown>;closed:boolean}|undefined,openCalled!:()=>void
   const opened=new Promise<void>(r=>{openCalled=r})
   ;(indexedDB as unknown as {open:unknown}).open=()=>{const request:Record<string,unknown>={};held={request,closed:false};openCalled();return request}
   const middle=new AbortController(),idbPending=openDocumentDrafts("alice","w","page",{requireExclusive:true,signal:middle.signal})
   await opened
   const lockWhileIdbOpening=await pageLockHeld()
   middle.abort()
   const duringIdb=await outcome(idbPending)
   const lockAfterIdbAbort=await pageLockHeld()
   ;(held!.request as {result:unknown}).result={close(){held!.closed=true}}
   ;(held!.request.onsuccess as ()=>void)?.()
   ;(indexedDB as unknown as {open:unknown}).open=realOpen
   // 4. Abandoned only after it opened: the returned store stays usable and is closed by its owner.
   const after=new AbortController(),store=await openDocumentDrafts("alice","w","page",{requireExclusive:true,signal:after.signal});after.abort()
   const draft={id:"kept",update:"",attempted:false,schema:1,epoch:"e",snapshot:"s"}
   await store.put(draft);const keptRows=(await store.load()).length;await store.remove("kept");store.close()
   return {preAborted,duringLock,lockAfterLockAbort,lockWhileIdbOpening,duringIdb,lockAfterIdbAbort,lateClosed:held!.closed,keptRows,exclusiveAtEnd:await exclusiveNow()}
  })
  expect(result.preAborted).toBe("DocumentStorageError")
  expect(result.duringLock).toBe("DocumentStorageError")
  expect(result.lockAfterLockAbort).toBe(false)
  expect(result.lockWhileIdbOpening).toBe(true)
  expect(result.duringIdb).toBe("DocumentStorageError")
  expect(result.lockAfterIdbAbort).toBe(false)
  expect(result.lateClosed).toBe(true)
  expect(result.keptRows).toBe(1)
  expect(result.exclusiveAtEnd).toBe(true)
 }finally{await browser.close()}
})
