vi.mock("@/lib/protected-document-drafts", () => ({ openProtectedDocumentDrafts: async (...args: Parameters<typeof import("@/lib/document-queue").openDocumentDrafts>) => (await import("@/lib/document-queue")).openDocumentDrafts(...args) }))
// @vitest-environment node
import {existsSync} from "node:fs"
import {buildSync} from "esbuild"
import {chromium} from "@playwright/test"
import {expect,it} from "vitest"
import {documentDraftBrowserFixture as fixture} from "@/lib/document-draft-browser-fixture.test-support"
import {installLocalDraftKeyFixture} from "@/lib/local-draft-seal.test-support"
it.skipIf(!existsSync(chromium.executablePath())).each([401,403])("retains on expiry and deletes only proven read revocation (%s)",async status=>{
 const built=buildSync({stdin:{contents:fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true});let readable=true
 try{
  const page=await browser.newPage()
  await page.route("https://document-reauth.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice",readable:()=>readable})
  await page.goto("https://document-reauth.test/pages/example");await page.addScriptTag({content:built.outputFiles[0].text})
  await page.waitForFunction(()=>Boolean(window.draftFailureFixture.editor?.editable))
  await page.evaluate(()=>window.draftFailureFixture.edit())
  // This fixture rejects patch writes; wait for the strict local transaction.
  await page.waitForFunction(()=>new Promise<boolean>(resolve=>{const r=indexedDB.open("crewship-document-drafts",1);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").count();tx.oncomplete=()=>{db.close();resolve(get.result>0)}}}))
  readable=status!==403
  await page.evaluate(status=>{(window.draftFailureFixture as typeof window.draftFailureFixture & {readStatus:number}).readStatus=status},status)
  if(status===403)await page.waitForFunction(()=>window.draftFailureFixture.editor.denied)
  else await page.waitForURL("**/login?**", {timeout:4000})
  const count=await page.evaluate(()=>new Promise<number>(resolve=>{const r=indexedDB.open("crewship-document-drafts",1);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").count();tx.oncomplete=()=>{db.close();resolve(get.result)}}}))
  expect(count).toBe(status===403?0:1)
 }finally{await browser.close()}
},30_000)
