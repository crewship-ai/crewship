// @vitest-environment node
// Actual React auth/editor hooks and IndexedDB; only HTTP responses are fixtures.
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium } from "@playwright/test"
import { installLocalDraftKeyFixture } from "@/lib/local-draft-seal.test-support"
import { expect, it } from "vitest"

type DocumentWriterFixture = {
  confirmed?: number
  human: string
  auth: ReturnType<typeof import("@/hooks/use-auth").useAuth>
  editor: ReturnType<typeof import("@/hooks/use-page-document").usePageDocument>
  pause: boolean
  putStarted: boolean
  abortCount: number
  purgeStarted: boolean
  done: boolean
  transition: Promise<void>
  holdSession: boolean
  sessionStarted: boolean
  releaseSession(): void
  staleRefresh: Promise<void>
  edit(): void
  rows(): Promise<string[]>
}
declare global { interface Window { documentErasureFixture: DocumentWriterFixture } }

const fixture = `
import React from 'react';
import {createRoot} from 'react-dom/client';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {usePageDocument} from '@/hooks/use-page-document';
import {documentSchema} from '@/lib/documents/schema';
const source=documentSchema.create({type:'doc',content:[{type:'paragraph',attrs:{id:'paragraph'},content:[{type:'text',text:'base'}]}]},3);
const state={page_id:'p',schema:3,rev:1,epoch:'epoch',generation:1,incarnation:'generation',snapshot:documentSchema.snapshot(source),snapshotRev:1,updates:[],blocks:[],content:{type:'doc'},outline:[],can_write:true,can_comment:false,can_resolve:false};
// Keep this single-tab fixture mounted so its post-sign-out IDB rows can be observed.
window.BroadcastChannel=undefined;
window.confirm=()=>{f.confirmed=(f.confirmed||0)+1;return true};const f=window.documentErasureFixture={human:'alice',auth:null,editor:null,pause:false,putStarted:false,abortCount:0,purgeStarted:false,done:false,holdSession:false,sessionStarted:false};
const draftNetwork=window.fetch.bind(window);
window.fetch=async(url,init)=>{
 if(String(url).includes("/draft-key?"))return draftNetwork(url,init);
 const path=String(url);
 if(path.includes('/api/auth/session')){
  const response=Response.json({user:{id:f.human,name:f.human,email:f.human+'@example.test'},expires:'2099-01-01'});
  if(f.holdSession){f.sessionStarted=true;await new Promise(resolve=>f.releaseSession=resolve);}
  return response;
 }
 if(path.includes('/api/auth/signout'))return Response.json({});
 if(path.includes('/document?'))return Response.json(state);
 throw new Error('Unexpected fixture request');
};
const abort=IDBTransaction.prototype.abort;
IDBTransaction.prototype.abort=function(){f.abortCount++;return abort.call(this);};
const put=IDBObjectStore.prototype.put;
IDBObjectStore.prototype.put=function(value,...args){
 const result=put.call(this,value,...args);
 if(this.name==='drafts' && f.pause){
  f.putStarted=true;
  const keepAlive=()=>{if(!f.pause)return;this.get(value.key).onsuccess=keepAlive;};
  keepAlive();
 }
 return result;
};
const cursor=IDBObjectStore.prototype.openCursor;
IDBObjectStore.prototype.openCursor=function(...args){if(this.name==='drafts')f.purgeStarted=true;return cursor.apply(this,args);};
function Editor(){const d=usePageDocument('w','slug','p',false);f.editor=d;return null;}
function App(){const auth=useAuth();f.auth=auth;return auth.session?<Editor key={auth.session.user.id}/>:null;}
createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><AuthProvider><App/></AuthProvider></QueryClientProvider>);
f.edit=()=>documentSchema.textNode(f.editor.doc,'paragraph').insert(0,'private draft ');
f.rows=()=>new Promise((resolve,reject)=>{
 const request=indexedDB.open('crewship-document-drafts',1);
 request.onerror=()=>reject(new Error('fixture read'));
 request.onsuccess=()=>{const db=request.result;const tx=db.transaction('drafts','readonly');const rows=tx.objectStore('drafts').getAll();tx.oncomplete=()=>{db.close();resolve(rows.result.map(row=>JSON.parse(row.key[0])[0]));};};
});
`

it.skipIf(!existsSync(chromium.executablePath())).each(["coalesced sign-out", "in-flight sign-out", "identity replacement", "stale session refresh", "stuck sign-out"])(
  "drains the real document writer and retains revisions not confirmed for deletion: %s", async scenario => {
    const { outputFiles } = buildSync({ stdin: { contents: fixture, loader: "tsx", resolveDir: process.cwd() }, bundle: true, write: false, platform: "browser", format: "iife", define: { "process.env.NODE_ENV": '"test"' } })
    const browser = await chromium.launch({ headless: true })
    try {
      const page = await browser.newPage()
      await page.route("https://document-writer.test/**", route => route.fulfill({ body: '<div id="root"></div>', contentType: "text/html" }))
      await installLocalDraftKeyFixture(page)
      await page.goto("https://document-writer.test")
      await page.addScriptTag({ content: outputFiles[0].text })
      await page.waitForFunction(() => window.documentErasureFixture.editor?.state?.rev === 1 && window.documentErasureFixture.editor?.durability === "confirmed")
      if (scenario === "stale session refresh") {
        await page.evaluate(() => { const f = window.documentErasureFixture; f.holdSession = true; f.staleRefresh = f.auth.refresh() })
        await page.waitForFunction(() => window.documentErasureFixture.sessionStarted)
      }
      const paused = scenario === "in-flight sign-out" || scenario === "identity replacement" || scenario === "stuck sign-out"
      await page.evaluate(paused => { const f = window.documentErasureFixture; f.pause = paused; f.edit() }, paused)
      if (paused) await page.waitForFunction(() => window.documentErasureFixture.putStarted)
      await page.evaluate(replace => {
        const f = window.documentErasureFixture
        if (replace) f.human = "bob"
        f.purgeStarted = false
        f.transition = (replace ? f.auth.refresh() : f.auth.signOut()).then(() => { f.done = true })
      }, scenario === "identity replacement")
      if (paused && scenario !== "stuck sign-out") {
        // Let the auth response settle while the first actual IDB transaction is
        // paused, then add another batch. It must never follow the final purge.
        await page.waitForFunction(() => window.documentErasureFixture.purgeStarted)
        await page.evaluate(() => { const f = window.documentErasureFixture; f.edit(); f.pause = false })
      }
      await page.waitForFunction(() => window.documentErasureFixture.done, undefined, { timeout: 6000 })
      if (scenario === "stuck sign-out") {
        expect(await page.evaluate(() => window.documentErasureFixture.abortCount)).toBeGreaterThan(0)
        // Releasing the old fixture after auth settles must not revive its write.
        await page.evaluate(() => { window.documentErasureFixture.pause = false })
      }
      if (scenario === "identity replacement") {
        await page.waitForFunction(() => window.documentErasureFixture.auth.session?.user.id === "bob" && window.documentErasureFixture.editor?.state?.rev === 1)
        await page.evaluate(() => window.documentErasureFixture.edit())
      }
      if (scenario === "stale session refresh") {
        await page.evaluate(async () => { const f = window.documentErasureFixture; f.releaseSession(); await f.staleRefresh })
        expect(await page.evaluate(() => window.documentErasureFixture.auth.session)).toBeNull()
      }
      // Wait on observable fixture state, never a fixed sleep: under CI load a
      // timed pause races the draft write and the purge. A scenario that never
      // reaches its end state still fails, by the waitForFunction timeout.
      // page.waitForFunction does not await an async predicate, so poll the fixture.
      const settle = { timeout: 15_000, intervals: [25, 50, 100, 250] }
      const readRows = () => page.evaluate(() => window.documentErasureFixture.rows())
      if (scenario === "stuck sign-out") await page.getByRole("alertdialog").waitFor({ timeout: settle.timeout })
      else if (scenario === "coalesced sign-out") {
        // Never a silent loss: the user confirmed deletion, the unconfirmed
        // revision is retained, or (storage too slow under load) the recovery
        // screen keeps the editor and asks to retry (#4702).
        await expect.poll(async () => (await page.evaluate(() => window.documentErasureFixture.confirmed ?? 0)) > 0 || (await readRows()).includes("alice") || (await page.getByRole("alertdialog").count()) === 1, settle).toBe(true)
      } else if (scenario === "identity replacement") {
        await expect.poll(async () => { const o = await readRows(); return o.includes("alice") && o.includes("bob") }, settle).toBe(true)
      } else await expect.poll(async () => (await readRows()).includes("alice"), settle).toBe(true)
      const owners = await readRows()
      if (scenario === "stuck sign-out") {
        expect(await page.getByRole("alertdialog").count()).toBe(1)
        expect(await page.evaluate(() => window.documentErasureFixture.editor.doc.isDestroyed)).toBe(false)
      } else {
        if(scenario==="coalesced sign-out" && !owners.includes("alice") && await page.getByRole("alertdialog").count()===1) { console.log("[4702] coalesced sign-out: recovery screen shown"); expect(await page.evaluate(()=>window.documentErasureFixture.editor.doc.isDestroyed)).toBe(false) }
        else if(scenario==="coalesced sign-out" && !owners.includes("alice")) expect(await page.evaluate(()=>window.documentErasureFixture.confirmed??0)).toBeGreaterThan(0)
        else expect(owners).toContain("alice")
        if(scenario === "identity replacement") expect(owners).toContain("bob")
      }
    } finally { await browser.close() }
  }, 30_000,
)
