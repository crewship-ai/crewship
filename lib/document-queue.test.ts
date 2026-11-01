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
