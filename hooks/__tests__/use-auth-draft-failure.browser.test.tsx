// @vitest-environment node
import {existsSync} from "node:fs"
import {buildSync} from "esbuild"
import {chromium} from "@playwright/test"
import {expect,it} from "vitest"
import {documentDraftBrowserFixture as fixture} from "@/lib/document-draft-browser-fixture.test-support"
import {installLocalDraftKeyFixture} from "@/lib/local-draft-seal.test-support"
declare global { interface Window { draftFailureFixture: { auth: ReturnType<typeof import("@/hooks/use-auth").useAuth>; editor: ReturnType<typeof import("@/hooks/use-page-document").usePageDocument>; failWrites: boolean; failOpening: boolean; failedWrites?: number; edit(): void; expire(): void } } }

it.skipIf(!existsSync(chromium.executablePath())).each(["expiry","signout","expiry without storage"])("keeps the real pending document mounted behind recovery UI when %s cannot persist it",async mode=>{
 const {outputFiles}=buildSync({stdin:{contents:mode==="expiry without storage"?fixture.replace("const networkFetch=", "f.failOpening=true;const networkFetch=").replace("if(f.failOpening&&name", "if(f.failOpening&&f.auth?.status==='authenticated'&&name"):fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-failure.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://document-failure.test/pages/example")
  await page.addScriptTag({content:outputFiles[0].text})
  await page.waitForFunction(()=>Boolean(window.draftFailureFixture.editor?.editable),undefined,{timeout:5000})
  await page.evaluate(mode=>{const f=window.draftFailureFixture;f.failWrites=mode!=="expiry without storage";f.edit();if(mode.startsWith("expiry"))f.expire();else void f.auth.signOut()},mode)
  if(mode!=="expiry without storage")await page.waitForFunction(()=>(window.draftFailureFixture.failedWrites??0)>0)
  await page.waitForFunction(()=>Boolean(document.querySelector('[role="alertdialog"]')),undefined,{timeout:2000})
  expect(page.url()).toContain("/pages/example")
  expect(await page.evaluate(()=>Boolean(window.draftFailureFixture.editor.doc.isDestroyed))).toBe(false)
  const hidden=await page.evaluate(()=>{const editorRoot=document.getElementById("root")!;return Array.from(editorRoot.children).some(child=>child.getAttribute("aria-hidden")==="true")})
  expect(hidden).toBe(true)
  expect(await page.evaluate(()=>document.getElementById("draft-private-portal")?.inert)).toBe(true)
  if(mode==="expiry without storage")return
  await page.evaluate(()=>{window.draftFailureFixture.failWrites=false;window.draftFailureFixture.failOpening=false})
  await page.getByRole("button",{name:"Retry keeping my changes"}).click()
  await page.waitForURL("**/login?**")
 }finally{await browser.close()}
},30_000)

it.skipIf(!existsSync(chromium.executablePath()))("holds identity admission when legacy cleanup cannot read storage and retries before opening an editor",async()=>{
 const {outputFiles}=buildSync({stdin:{contents:fixture.replace("const networkFetch=","f.failOpening=true;const networkFetch="),loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://document-failure.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://document-failure.test/pages/example")
  await page.evaluate(()=>new Promise<void>((resolve,reject)=>{
   const request=indexedDB.open("crewship-document-drafts",1)
   request.onupgradeneeded=()=>request.result.createObjectStore("drafts",{keyPath:"key"})
   request.onerror=()=>reject(request.error)
   request.onsuccess=()=>{const db=request.result,tx=db.transaction("drafts","readwrite");tx.objectStore("drafts").put({key:[JSON.stringify(["bob","w","other"]),"old"],draft:{id:"old",snapshot:"other human legacy text"}});tx.oncomplete=()=>{db.close();resolve()}}
  }))
  await page.addScriptTag({content:outputFiles[0].text})
  await page.getByRole("alertdialog").waitFor()
  expect(await page.evaluate(()=>Boolean(window.draftFailureFixture.editor))).toBe(false)
  await page.evaluate(()=>{window.draftFailureFixture.failOpening=false})
  await page.getByRole("button",{name:"Retry keeping my changes"}).click()
  await page.waitForFunction(()=>Boolean(window.draftFailureFixture.editor?.editable))
  expect(await page.evaluate(()=>new Promise<number>((resolve,reject)=>{const r=indexedDB.open("crewship-document-drafts",1);r.onerror=()=>reject(r.error);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").count();tx.oncomplete=()=>{db.close();resolve(get.result)}}}))).toBe(0)
 }finally{await browser.close()}
},30_000)
