// @vitest-environment node
// Real AuthProvider, storage and navigation. Only the HTTP server is a fixture.
import {existsSync} from "node:fs"
import {buildSync} from "esbuild"
import {chromium} from "@playwright/test"
import {expect,it} from "vitest"
import {installLocalDraftKeyFixture} from "@/lib/local-draft-seal.test-support"
const fixture=`
import React from 'react';
import {createRoot} from 'react-dom/client';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {writeArtifactDraft,artifactDraftKey} from '@/lib/artifact-drafts';
import {AUTH_EVENT} from '@/lib/api-fetch';
window.BroadcastChannel=undefined;
const f=window.retentionFixture={auth:null,expire(){window.dispatchEvent(new CustomEvent(AUTH_EVENT,{detail:{reason:'session_expired'}}));},async seed(){await writeArtifactDraft(artifactDraftKey('artifact-source','alice','workspace','caaaaaaaaaaaaaaaaaaaaaaaa'),'PRIVATE_UNSAVED_DRAFT');}};
const networkFetch=window.fetch.bind(window);
window.fetch=async (url,init)=>{
 const path=String(url);
 if(path.includes("/draft-key?"))return networkFetch(url,init);
 if(path.includes('/api/auth/session'))return Response.json({user:{id:'alice',name:'Alice',email:'alice@example.test'},expires:'2099-01-01'});
 throw new Error('unexpected fixture request '+path);
};
function App(){f.auth=useAuth();return null;}
createRoot(document.getElementById('root')).render(<AuthProvider><App/></AuthProvider>);
`;
it.skipIf(!existsSync(chromium.executablePath()))("retains the browser copy across real session-expiry navigation",async()=>{
 const {outputFiles}=buildSync({stdin:{contents:fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try{
  const page=await browser.newPage()
  await page.route("https://draft-retention.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://draft-retention.test/pages/example")
  await page.addScriptTag({content:outputFiles[0].text})
  await page.waitForFunction(()=>((window as unknown as {retentionFixture:{auth:{status:string}}}).retentionFixture.auth?.status)==="authenticated")
  await page.evaluate(()=>((window as unknown as {retentionFixture:{seed():Promise<void>}}).retentionFixture.seed()))
  await page.evaluate(()=>((window as unknown as {retentionFixture:{expire():void}}).retentionFixture.expire()))
  await page.waitForURL("**/login?**")
  const rows=await page.evaluate(()=>new Promise<unknown[]>((resolve,reject)=>{
   const request=indexedDB.open("crewship-artifact-drafts",1)
   request.onerror=()=>reject(new Error("storage read"))
   request.onsuccess=()=>{const db=request.result,tx=db.transaction("drafts","readonly"),rows=tx.objectStore("drafts").getAll();tx.oncomplete=()=>{db.close();resolve(rows.result)}}
  }))
  expect(rows).toHaveLength(1)
  expect(JSON.stringify(rows)).not.toContain("PRIVATE_UNSAVED_DRAFT")
 }finally{await browser.close()}
},30_000)
