// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"
import { installLocalDraftKeyFixture } from "@/lib/local-draft-seal.test-support"
const fixture = `
import React from 'react';import {createRoot} from 'react-dom/client';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {artifactDraftKey,writeArtifactDraft,loadArtifactDraft} from '@/lib/artifact-drafts';
window.BroadcastChannel=undefined;
const key=artifactDraftKey('artifact-source','alice','w','page');
const f=window.signoutDraftFixture={auth:null,posts:0,allow:false,started:false,done:false,warning:'',async seed(text){await loadArtifactDraft(key);await writeArtifactDraft(key,text);},async leave(){await f.auth.signOut();f.done=true;}};
window.confirm=message=>{f.warning=message;return f.allow;};
const network=window.fetch.bind(window);
window.fetch=async(url,init)=>{
 if(String(url).includes('/draft-key?'))return network(url,init);
 if(String(url).includes('/api/auth/session'))return Response.json({user:{id:'alice',name:'Alice',email:'a@example.test'},expires:'2099-01-01'});
 if(String(url).includes('/api/auth/signout')){f.posts++;f.started=true;await new Promise(resolve=>f.release=resolve);return Response.json({});}
 throw Error('unexpected request '+url);
};
function App(){f.auth=useAuth();return null;}createRoot(document.getElementById('root')).render(<AuthProvider><App/></AuthProvider>);
`;
type Fixture = { auth: {status:string}; posts:number; warning:string; allow:boolean; started:boolean; done:boolean; seed(text:string):Promise<void>; leave():Promise<void>; release():void }
it.skipIf(!existsSync(chromium.executablePath()))("asks before deleting the sole copy and preserves a newer revision after a delayed signout ACK", async () => {
 const built=buildSync({stdin:{contents:fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try {
  const page=await browser.newPage()
  await page.route("https://signout-draft.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://signout-draft.test/pages/example");await page.addScriptTag({content:built.outputFiles[0].text})
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.auth?.status)==="authenticated")
  await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.seed("original private copy")))
  await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.leave()))
  expect(await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.posts))).toBe(0)
  expect(await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.warning))).toMatch(/only|sole|unsaved/i)
  await page.evaluate(()=>{const f=(window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture;f.allow=true;f.done=false;void f.leave()})
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.started))
  await page.evaluate(async()=>{const f=(window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture;await f.seed("newer private copy");f.release()})
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.done))
  const rows=await page.evaluate(()=>new Promise<unknown[]>((resolve,reject)=>{const r=indexedDB.open("crewship-artifact-drafts",1);r.onerror=()=>reject(r.error);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").getAll();tx.oncomplete=()=>{db.close();resolve(get.result)}}}))
  expect(rows).toHaveLength(1);expect(JSON.stringify(rows)).not.toContain("newer private copy")
 } finally {await browser.close()}
},30_000)

it.skipIf(!existsSync(chromium.executablePath()))("warns before deleting a sole legacy localStorage Design copy on signout", async () => {
 const built=buildSync({stdin:{contents:fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try {
  const page=await browser.newPage()
  await page.route("https://signout-draft.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://signout-draft.test/pages/example");await page.addScriptTag({content:built.outputFiles[0].text})
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.auth?.status)==="authenticated")
  await page.evaluate(()=>localStorage.setItem("crewship-design:v2:alice:w:page","sole legacy canvas"))
  // Observe either confirmation or a sign-out POST: missing confirmation
  // must fail directly instead of waiting for a deliberately delayed ACK.
  await page.evaluate(()=>{void (window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.leave()})
  await page.waitForFunction(()=>{const f=(window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture;return f.warning!==""||f.posts>0})
  expect(await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.posts))).toBe(0)
  expect(await page.evaluate(()=>localStorage.getItem("crewship-design:v2:alice:w:page"))).toBe("sole legacy canvas")
  expect(await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.warning))).toMatch(/unsaved/i)
  await page.evaluate(()=>{const f=(window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture;f.allow=true;f.done=false;void f.leave()})
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.started))
  await page.evaluate(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.release()))
  await page.waitForFunction(()=>((window as unknown as {signoutDraftFixture:Fixture}).signoutDraftFixture.done))
  expect(await page.evaluate(()=>localStorage.getItem("crewship-design:v2:alice:w:page"))).toBeNull()
 } finally {await browser.close()}
},30_000)
