// @vitest-environment node
// Browser storage boundary: real IndexedDB transactions, no storage mock.
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"

it.skipIf(!existsSync(chromium.executablePath()))("only an editor's final capture of a confirmed revision counts as confirmed", async () => {
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-queue.test/**",route=>route.fulfill({body:"<!doctype html><title>Document journal</title>",contentType:"text/html"}))
  await page.goto("https://document-queue.test")
  const {outputFiles}=buildSync({entryPoints:[`${process.cwd()}/lib/document-queue.ts`],bundle:true,write:false,platform:"browser",format:"iife",globalName:"documentQueue"})
  await page.addScriptTag({content:outputFiles[0].text})
  const result=await page.evaluate(async()=>{
   Object.defineProperty(navigator,"locks",{value:undefined,configurable:true})
   const queue=(window as unknown as {documentQueue: typeof import("./document-queue")}).documentQueue
   const store=await queue.openDocumentDrafts("alice","workspace","page")
   const put=(id:string,snapshot:string,final=false)=>store.put({id,update:"",attempted:true,schema:1,epoch:"native-v1",snapshot:JSON.stringify({format:"crewship-local-draft-v1",snapshot})},{final})
   await put("native:own","r1");await put("native:other-tab","r1");await put("native:typed-in-dialog","r1");await put("native:erased-first","r1");await put("native:edited-after","r1")
   const confirmed=await queue.captureDocumentDrafts("alice")
   // The confirming tab erased these before this tab heard of it; one was then really edited.
   await queue.removeCapturedDocumentDrafts(confirmed.filter(row=>/erased-first|edited-after/.test(row.key[1])))
   await put("native:edited-after","r2")
   // Written while the dialog was open, by another tab or this tab's own editor.
   await put("native:other-tab","r2");await put("native:typed-in-dialog","r2")
   queue.registerDocumentDraftWriter("alice",async()=>{},{suspend:async()=>{await put("native:own","r2",true);await put("native:own","r3",true);await put("native:typed-in-dialog","r3",true);await put("native:erased-first","r2",true);await put("native:edited-after","r3",true)}})
   await queue.suspendOtherDocumentDrafts(null)
   const after=await queue.captureDocumentDrafts("alice")
   const successors=await queue.suspendedDocumentDraftRevisions(confirmed)
   await queue.removeCapturedDocumentDrafts([...confirmed,...successors])
   const left=await queue.captureDocumentDrafts("alice")
   store.close()
   return {after:after.length,successors:successors.map(row=>row.key[1]),left:left.map(row=>row.key[1])}
  })
  expect(result.after).toBe(5)
  expect(result.successors.sort()).toEqual([JSON.stringify([1,"native-v1","native:erased-first"]),JSON.stringify([1,"native-v1","native:own"])])
  expect(result.left.sort()).toEqual([JSON.stringify([1,"native-v1","native:edited-after"]),JSON.stringify([1,"native-v1","native:other-tab"]),JSON.stringify([1,"native-v1","native:typed-in-dialog"])])
 }finally{await browser.close()}
})
