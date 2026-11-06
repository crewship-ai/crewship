// @vitest-environment node
import { randomBytes } from "node:crypto"
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium, type Page } from "@playwright/test"
import { expect, it } from "vitest"
import * as Y from "yjs"
import { upsertDesignNode } from "../design-document"
import { installLocalDraftKeyFixture } from "@/lib/local-draft-seal.test-support"
const fixture = `
import React from 'react';import {createRoot} from 'react-dom/client';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {useDesign} from '@/components/features/artifacts-design/use-design';
import {upsertDesignNode} from '@/components/features/artifacts-design/design-document';
import {artifactDraftKey,artifactDraftStorageState,loadArtifactDraft,writeArtifactDraft} from '@/lib/artifact-drafts';
import {AUTH_EVENT} from '@/lib/api-fetch';
import * as Y from 'yjs';
import * as sync from 'y-protocols/sync';
import * as encoding from 'lib0/encoding';
import * as decoding from 'lib0/decoding';
const person=localStorage.getItem('person')||'alice';
const f=window.designTabs={design:null,silent:false,deny:false};
const seeded=localStorage.getItem('room');
const room=new Y.Doc();
if(seeded)Y.applyUpdate(room,Uint8Array.from(atob(seeded),c=>c.charCodeAt(0)));
const frame=write=>{const e=encoding.createEncoder();encoding.writeVarUint(e,0);write(e);return encoding.toUint8Array(e)};
// Without a seeded room the socket never opens: the tabs are offline and hold only their own work.
window.WebSocket=class{static OPEN=1;readyState=0;binaryType='';
 constructor(){if(!seeded)return;f.socket=this;setTimeout(()=>{this.readyState=1;this.onopen?.();this.deliver(frame(e=>sync.writeSyncStep1(e,room)))},0)}
 deliver(bytes){this.onmessage?.({data:bytes.buffer.slice(bytes.byteOffset,bytes.byteOffset+bytes.byteLength)})}
 close(){}
 send(bytes){if(f.silent)return;const d=decoding.createDecoder(new Uint8Array(bytes));if(decoding.readVarUint(d)!==0)return;const sub=decoding.readVarUint(d);
  if(sub===sync.messageYjsSyncStep1){const reply=frame(e=>sync.readSyncStep1(d,e,room));setTimeout(()=>this.deliver(reply),0)}
  else Y.applyUpdate(room,decoding.readVarUint8Array(d))}
};
f.remote=id=>{const before=Y.encodeStateVector(room);upsertDesignNode(room,{id,type:'text',text:id,x:0,y:0,width:100,height:40});f.socket.deliver(frame(e=>sync.writeUpdate(e,Y.encodeStateAsUpdate(room,before))))};
const network=window.fetch.bind(window);
window.fetch=async(url,init)=>{
 const path=String(url);
 if(path.includes('/draft-key?'))return network(url,init);
 if(path.includes('/api/auth/session'))return Response.json(f.signedOut?null:{user:{id:person,name:person,email:'a@example.test'},expires:'2099-01-01'});
 if(path.includes('/api/auth/signout')){f.signedOut=true;return Response.json({ok:true})}
 if(path.includes('/api/auth/csrf'))return Response.json({csrfToken:'t'});
 if(path.includes('/auth/')&&path.includes('refresh'))return Response.json({ok:true});
 if(path.includes('/comments?'))return Response.json({comments:[]});
 if(path.includes('/versions?'))return Response.json({versions:[],latest_version:1,shared_version:1});
 if(f.deny&&path.includes('/design'))return Response.json({error:'not found'},{status:404});
 if(path.includes('/design'))return Response.json({page_id:'page',authority_revision:'1',can_edit:true,version:1,latest_version:1,shared_version:1,nodes:[],order:[],actor:{id:person,name:person}});
 throw Error('unexpected '+path);
};
function Canvas(){f.design=useDesign('w','slug');return null;}
function App(){f.auth=useAuth();return f.auth.session?<Canvas/>:null;}
const queries=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});
f.refresh=()=>queries.invalidateQueries();
createRoot(document.getElementById('root')).render(<QueryClientProvider client={queries}><AuthProvider><App/></AuthProvider></QueryClientProvider>);
f.edit=id=>upsertDesignNode(f.design.doc,{id,type:'text',text:id,x:0,y:0,width:100,height:40});
f.stored=()=>artifactDraftStorageState(artifactDraftKey('design-tabs','alice','w','page'))==='durable';
f.ids=()=>f.design.nodes.map(node=>node.id).sort();
f.text=(id,text)=>upsertDesignNode(f.design.doc,{id,type:'text',text,x:0,y:0,width:100,height:40});
f.lost=()=>f.design.conflicts.map(conflict=>({left:conflict.left,restored:conflict.restore.text}));
f.shown=id=>f.design.nodes.find(node=>node.id===id)?.text;
f.readOwner=()=>loadArtifactDraft(artifactDraftKey('design-tabs','alice','w','page'));
f.signOut=()=>f.auth.signOut();
f.expire=()=>window.dispatchEvent(new CustomEvent(AUTH_EVENT,{detail:{reason:'session_expired'}}));
f.writeOldFormat=id=>{const doc=new Y.Doc();upsertDesignNode(doc,{id,type:'text',text:id,x:0,y:0,width:100,height:40});const bytes=Y.encodeStateAsUpdate(doc);return writeArtifactDraft(artifactDraftKey('design-canvas','alice','w','page'),btoa(String.fromCharCode(...bytes)));};
`
type Tabs = { design: { editable: boolean } | null; edit(id: string): void; text(id: string, text: string): void; stored(): boolean; ids(): string[]; lost(): { left?: string; restored?: string }[]; silent: boolean; deny: boolean; remote(id: string): void; refresh(): Promise<void>; signOut(): Promise<boolean>; readOwner(): Promise<string>; shown(id: string): string | undefined; expire(): void; writeOldFormat(id: string): Promise<void> }
// A real renderer kill: no unload, no pagehide, nothing queued gets out.
async function kill(page: Page) {
 const session = await page.context().newCDPSession(page)
 const crashed = page.waitForEvent("crash")
 void session.send("Page.crash").catch(() => {})
 await crashed
}
type Row = { key: string[]; text: string }
const storedRows = (page: Page) => page.evaluate(() => new Promise<Row[]>((resolve, reject) => { const r = indexedDB.open("crewship-artifact-drafts", 1); r.onerror = () => reject(r.error); r.onsuccess = () => { const db = r.result, tx = db.transaction("drafts", "readonly"), all = tx.objectStore("drafts").getAll(); tx.oncomplete = () => { db.close(); resolve(all.result) } } }))
const it_ = it.skipIf(!existsSync(chromium.executablePath()))
type Open = (room?: Y.Doc) => Promise<Page>
async function withTabs(run: (open: Open, edit: (page: Page, change: () => void) => Promise<void>, access: { readable: boolean; human: string; person: string }) => Promise<void>) {
 const access = { readable: true, human: "alice", person: "alice" }
 const built = buildSync({ stdin: { contents: fixture, loader: "tsx", resolveDir: process.cwd() }, bundle: true, write: false, platform: "browser", format: "iife", define: { "process.env.NODE_ENV": '"test"' } })
 const browser = await chromium.launch({ headless: true })
 try {
  const context = await browser.newContext()
  await context.route("https://design-tabs.test/**", route => route.fulfill({ body: '<div id="root"></div>', contentType: "text/html" }))
  await run(async room => {
   const page = await context.newPage()
   page.setDefaultTimeout(8000)
   await installLocalDraftKeyFixture(page, { human: () => access.human, readable: () => access.readable })
   await page.goto("https://design-tabs.test/pages/design")
   await page.evaluate(({ seed, person }) => { localStorage.setItem("person", person); if (seed) localStorage.setItem("room", seed); else localStorage.removeItem("room") }, { seed: room ? Buffer.from(Y.encodeStateAsUpdate(room)).toString("base64") : "", person: access.person })
   await page.addScriptTag({ content: built.outputFiles[0].text })
   await page.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.design?.editable)
   return page
  }, async (page, change) => {
   await page.evaluate(change)
   await page.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.stored())
  }, access)
 } finally { await browser.close() }
}
it_("keeps the offline work of a killed tab when another tab stores the canvas", () => withTabs(async (open, edit) => {
 const first = await open(), second = await open()
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-first"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-second"))
 await kill(first)
 await kill(second)
 const reopened = await open()
 expect(await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["from-first", "from-second"])
}), 30_000)
it_("shows the text that lost when two killed tabs changed one layer", () => withTabs(async (open, edit) => {
 const seed = await open()
 await edit(seed, () => (window as unknown as { designTabs: Tabs }).designTabs.text("title", "Base"))
 await seed.close({ runBeforeUnload: false })
 const first = await open(), second = await open()
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.text("title", "First"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.text("title", "Second"))
 await kill(first)
 await kill(second)
 const reopened = await open()
 const shown = await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.shown("title"))
 await reopened.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.lost().length > 0, undefined, { timeout: 3000 })
 expect(await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.lost())).toEqual([{ left: shown, restored: shown === "First" ? "Second" : "First" }])
}), 30_000)
it_("keeps another tab's work when the session of the tab still open expires", () => withTabs(async (open, edit) => {
 const first = await open(), second = await open()
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-first"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-second"))
 await kill(second)
 await first.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.expire())
 await first.waitForURL("**/login?**", { timeout: 5000 })
 const reopened = await open()
 expect(await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["from-first", "from-second"])
}), 30_000)
it_("opens a canvas stored in the old format and adds to it", () => withTabs(async (open, edit) => {
 const seed = await open()
 await seed.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.writeOldFormat("old-format"))
 await seed.close({ runBeforeUnload: false })
 const tab = await open()
 expect(await tab.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["old-format"])
 await edit(tab, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("added"))
 await kill(tab)
 const reopened = await open()
 expect(await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["added", "old-format"])
}), 30_000)
it_("names the unsent text a server text hides after a killed tab, with a remote frame before the last store", () => withTabs(async (open, edit) => {
 const base = new Y.Doc(); upsertDesignNode(base, { id: "title", type: "text", text: "Base", x: 0, y: 0, width: 100, height: 40 })
 const tab = await open(base)
 await tab.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.shown("title") === "Base")
 await tab.evaluate(() => { (window as unknown as { designTabs: Tabs }).designTabs.silent = true })
 await edit(tab, () => (window as unknown as { designTabs: Tabs }).designTabs.text("title", "Mine"))
 await tab.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.remote("remote"))
 await edit(tab, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("second"))
 await kill(tab)
 const server = new Y.Doc(); Y.applyUpdate(server, Y.encodeStateAsUpdate(base))
 upsertDesignNode(server, { id: "remote", type: "text", text: "remote", x: 0, y: 0, width: 100, height: 40 })
 upsertDesignNode(server, { id: "title", text: "Theirs" })
 const reopened = await open(server)
 await reopened.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.lost().length > 0, undefined, { timeout: 5000 })
 const [notice] = await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.lost())
 expect([notice.left, notice.restored].sort()).toEqual(["Mine", "Theirs"])
}), 30_000)
it_("erases the entries of every tab and the old row when read access goes", () => withTabs(async (open, edit, access) => {
 const first = await open(), second = await open()
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-first"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-second"))
 await second.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.writeOldFormat("old-format"))
 await kill(second)
 const rows = () => first.evaluate(() => new Promise<number>((resolve, reject) => { const r = indexedDB.open("crewship-artifact-drafts", 1); r.onerror = () => reject(r.error); r.onsuccess = () => { const db = r.result, tx = db.transaction("drafts", "readonly"), count = tx.objectStore("drafts").count(); tx.oncomplete = () => { db.close(); resolve(count.result) } } }))
 await expect.poll(rows, { timeout: 5000 }).toBe(2)
 access.readable = false
 await first.evaluate(() => { const tabs = (window as unknown as { designTabs: Tabs }).designTabs; tabs.deny = true; return tabs.refresh() })
 await expect.poll(rows, { timeout: 5000 }).toBe(0)
}), 30_000)
it_("a confirmed sign-out erases the stored work of both open tabs and the old row", () => withTabs(async (open, edit) => {
 const first = await open(), second = await open()
 await first.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.writeOldFormat("old-format"))
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-first"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-second"))
 expect((await storedRows(first)).length).toBeGreaterThanOrEqual(2)
 first.on("dialog", dialog => dialog.accept())
 expect(await first.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.signOut())).toBe(true)
 await first.waitForTimeout(1000)
 expect((await storedRows(first)).map(row => row.key[1])).toEqual([])
}), 30_000)
it_("session expiry keeps sealed rows, a different user reads nothing, and the owner gets them back", () => withTabs(async (open, edit, access) => {
 const first = await open(), second = await open()
 await edit(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("private-first"))
 await edit(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("private-second"))
 await second.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.expire())
 await first.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.expire())
 await first.waitForURL("**/login?**", { timeout: 5000 })
 const kept = await storedRows(first)
 expect(kept.map(row => row.key[1])).toContain("design-tabs")
 for (const row of kept) { expect(JSON.parse(row.text).format).toMatch(/^crewship-local-draft-(bundle-)?v1$/); expect(row.text).not.toContain("private-") }
 access.person = "bob"; access.human = "bob"
 const bob = await open()
 expect(await bob.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual([])
 expect(await bob.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.readOwner())).toBe("")
 expect((await storedRows(bob)).map(row => row.key[1])).toContain("design-tabs")
 await bob.close({ runBeforeUnload: false })
 access.person = "alice"; access.human = "alice"
 const owner = await open()
 expect(await owner.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["private-first", "private-second"])
}), 30_000)
it_("still stores with two tabs near the canvas limit", () => withTabs(async (open, edit) => {
 const big = (bytes: number) => { const doc = new Y.Doc(); upsertDesignNode(doc, { id: "big", type: "text", text: randomBytes(Math.ceil(bytes * 3 / 4)).toString("base64").slice(0, bytes), x: 0, y: 0, width: 100, height: 40 }); return doc }
 const synced = async (room: Y.Doc) => { const page = await open(room); await page.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.ids().includes("big")); await page.evaluate(() => { (window as unknown as { designTabs: Tabs }).designTabs.silent = true }); return page }
 const room = big(2 * 1024 * 1024 - 64 * 1024)
 const first = await synced(room), second = await synced(room)
 const timed = async (page: Page, change: () => void) => { const start = Date.now(); await edit(page, change); return Date.now() - start }
 await timed(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-first"))
 const secondStore = await timed(second, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("from-second"))
 const repeatStore = await timed(first, () => (window as unknown as { designTabs: Tabs }).designTabs.edit("again"))
 const [row] = await storedRows(first)
 console.log(`#5017 measure: state bytes ${Y.encodeStateAsUpdate(room).length}, row chars ${row.text.length}, second tab store ${secondStore} ms, repeat store ${repeatStore} ms`)
 await kill(first); await kill(second)
 const reopened = await synced(room)
 const shown = await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())
 expect(shown).toEqual(expect.arrayContaining(["again", "big", "from-first", "from-second"]))
}), 60_000)
it_("refuses a canvas past the limit, says so and stores nothing", () => withTabs(async (open, edit) => {
 void edit
 const room = new Y.Doc(); upsertDesignNode(room, { id: "big", type: "text", text: randomBytes(1_700_000).toString("base64").slice(0, 2_200_000), x: 0, y: 0, width: 100, height: 40 })
 const tab = await open(room)
 await tab.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.ids().includes("big"))
 await tab.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.edit("too-late"))
 await tab.waitForFunction(() => (window as unknown as { designTabs: { design: { draftError: boolean } } }).designTabs.design.draftError)
 expect(await storedRows(tab)).toEqual([])
}), 30_000)
