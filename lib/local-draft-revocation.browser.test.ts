// @vitest-environment node
import {existsSync} from "node:fs"
import {buildSync} from "esbuild"
import {chromium} from "@playwright/test"
import {expect,it} from "vitest"
import {installLocalDraftKeyFixture} from "./local-draft-seal.test-support"
type API=typeof import("./artifact-drafts")
it.skipIf(!existsSync(chromium.executablePath()))("deletes revisionless and localStorage Page drafts after proven read loss and preserves later CAS revisions",async()=>{
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage();await page.route("https://draft-revoke.test/**",r=>r.fulfill({body:'<!doctype html>',contentType:'text/html'}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice",readable:()=>false})
  await page.goto("https://draft-revoke.test/page")
  const built=buildSync({entryPoints:[`${process.cwd()}/lib/artifact-drafts.ts`],bundle:true,write:false,platform:'browser',format:'iife',globalName:'draftAPI'})
  await page.addScriptTag({content:built.outputFiles[0].text})
  const result=await page.evaluate(async()=>{
   const api=(window as unknown as {draftAPI:API}).draftAPI
   const human='alice',workspace='w',page='page',scope={human,workspace,page}
   const row=[human,'artifact-source',workspace,page], documentKey=[JSON.stringify([human,workspace,page]),'draft']
   const seed=async(name:string,value:unknown)=>new Promise<void>((resolve,reject)=>{const r=indexedDB.open(name,1);r.onupgradeneeded=()=>r.result.createObjectStore('drafts',{keyPath:'key'});r.onerror=()=>reject(r.error);r.onsuccess=()=>{const db=r.result,tx=db.transaction('drafts','readwrite');tx.objectStore('drafts').put(value);tx.oncomplete=()=>{db.close();resolve()}}})
   const count=async(name:string)=>new Promise<number>(resolve=>{const r=indexedDB.open(name,1);r.onsuccess=()=>{const db=r.result,tx=db.transaction('drafts','readonly'),get=tx.objectStore('drafts').count();tx.oncomplete=()=>{db.close();resolve(get.result)}}})
   await seed('crewship-artifact-drafts',{key:row,text:'legacy private source'})
   await seed('crewship-document-drafts',{key:documentKey,draft:{id:'draft',snapshot:'legacy private document'}})
   localStorage.setItem('design-comment:v2:alice:w:page','legacy private comment')
   let refused=false
   try{await api.loadArtifactDraft(JSON.stringify(row))}catch{refused=true}
   const removed={refused,artifacts:await count('crewship-artifact-drafts'),documents:await count('crewship-document-drafts'),local:localStorage.getItem('design-comment:v2:alice:w:page')}
   await seed('crewship-artifact-drafts',{key:row,text:'old',revision:'old'})
   const captured=await api.captureLocalDraftPage(scope)
   await seed('crewship-artifact-drafts',{key:row,text:'newer',revision:'newer'})
   await api.purgeLocalDraftPage(scope,captured)
   return {removed,retained:await count('crewship-artifact-drafts')}
  })
  expect(result).toEqual({removed:{refused:true,artifacts:0,documents:0,local:null},retained:1})
 }finally{await browser.close()}
},30_000)
