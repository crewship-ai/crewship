// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"
import { installLocalDraftKeyFixture } from "@/lib/local-draft-seal.test-support"
const fixture = `
import React from 'react';import {createRoot} from 'react-dom/client';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {AuthProvider,useAuth} from '@/hooks/use-auth';import {AUTH_EVENT} from '@/lib/api-fetch';
import {useDesign} from '@/components/features/artifacts-design/use-design';
import {upsertDesignNode} from '@/components/features/artifacts-design/design-document';
window.BroadcastChannel=undefined;
const f=window.designRetentionFixture={auth:null,design:null};
const network=window.fetch.bind(window);
window.fetch=async(url,init)=>{
 const path=String(url);
 if(path.includes('/draft-key?'))return network(url,init);
 if(path.includes('/api/auth/session'))return Response.json({user:{id:'alice',name:'Alice',email:'a@example.test'},expires:'2099-01-01'});
 if(path.includes('/design'))return Response.json({page_id:'page',authority_revision:'1',can_edit:true,version:1,latest_version:1,shared_version:1,nodes:[],order:[],actor:{id:'alice',name:'Alice'},versions:[],comments:[]});
 throw Error('unexpected '+path);
};
function Canvas(){f.design=useDesign('w','slug');return null;}
function App(){f.auth=useAuth();return f.auth.session?<Canvas/>:null;}
createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><AuthProvider><App/></AuthProvider></QueryClientProvider>);
f.edit=()=>upsertDesignNode(f.design.doc,{id:'draft',type:'text',text:'PRIVATE_CANVAS_CHANGE',x:0,y:0,width:100,height:40});
f.expire=()=>window.dispatchEvent(new CustomEvent(AUTH_EVENT,{detail:{reason:'session_expired'}}));
`;
type Fixture={ design:{editable:boolean}; edit():void; expire():void }
it.skipIf(!existsSync(chromium.executablePath())).each([false,true,"settled"])("retains the Design canvas, blocking expiry when capture exceeds its limit (%s)",async oversized=>{
 const built=buildSync({stdin:{contents:oversized===true?fixture.replace("text:'PRIVATE_CANVAS_CHANGE'", "text:'PRIVATE_CANVAS_CHANGE'.repeat(150000)"):fixture,loader:"tsx",resolveDir:process.cwd()},bundle:true,write:false,platform:"browser",format:"iife",define:{"process.env.NODE_ENV":'"test"'}})
 const browser=await chromium.launch({headless:true})
 try {
  const page=await browser.newPage()
  await page.route("https://design-retention.test/**",route=>route.fulfill({body:'<div id="root"></div>',contentType:"text/html"}))
  await installLocalDraftKeyFixture(page,{human:()=>"alice"})
  await page.goto("https://design-retention.test/pages/design");await page.addScriptTag({content:built.outputFiles[0].text})
  await page.waitForFunction(()=>((window as unknown as {designRetentionFixture:Fixture}).designRetentionFixture.design?.editable))
  if(oversized==="settled"){
   await page.evaluate(()=>(window as unknown as {designRetentionFixture:Fixture}).designRetentionFixture.edit())
   await page.waitForFunction(()=>new Promise<boolean>(resolve=>{const r=indexedDB.open("crewship-artifact-drafts",1);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").count();tx.oncomplete=()=>{db.close();resolve(get.result>0)}}}))
   await page.evaluate(()=>(window as unknown as {designRetentionFixture:Fixture}).designRetentionFixture.expire())
  }else await page.evaluate(()=>{const f=(window as unknown as {designRetentionFixture:Fixture}).designRetentionFixture;f.edit();f.expire()})
  if(oversized===true){
   await page.getByRole("alertdialog").waitFor({timeout:3000})
   expect(page.url()).toContain("/pages/design")
   return
  }
  await page.waitForURL("**/login?**",{timeout:3000})
  const rows=await page.evaluate(()=>new Promise<{text:string}[]>((resolve,reject)=>{const r=indexedDB.open("crewship-artifact-drafts",1);r.onerror=()=>reject(r.error);r.onsuccess=()=>{const db=r.result,tx=db.transaction("drafts","readonly"),get=tx.objectStore("drafts").getAll();tx.oncomplete=()=>{db.close();resolve(get.result)}}}))
  expect(rows).toHaveLength(1);expect(JSON.parse(rows[0].text).format).toBe("crewship-local-draft-v1")
  expect(JSON.stringify(rows)).not.toContain("PRIVATE_CANVAS_CHANGE")
 }finally{await browser.close()}
},30_000)
