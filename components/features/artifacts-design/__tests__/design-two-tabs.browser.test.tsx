// @vitest-environment node
import { existsSync } from "node:fs"
import { buildSync } from "esbuild"
import { chromium, type Page } from "@playwright/test"
import { expect, it } from "vitest"
import { installLocalDraftKeyFixture } from "@/lib/local-draft-seal.test-support"
const fixture = `
import React from 'react';import {createRoot} from 'react-dom/client';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {useDesign} from '@/components/features/artifacts-design/use-design';
import {upsertDesignNode} from '@/components/features/artifacts-design/design-document';
import {artifactDraftKey,artifactDraftStorageState} from '@/lib/artifact-drafts';
// The socket never opens: both tabs are offline and hold only their own work.
window.WebSocket=class{static OPEN=1;readyState=0;close(){}send(){}};
const f=window.designTabs={design:null};
const network=window.fetch.bind(window);
window.fetch=async(url,init)=>{
 const path=String(url);
 if(path.includes('/draft-key?'))return network(url,init);
 if(path.includes('/api/auth/session'))return Response.json({user:{id:'alice',name:'Alice',email:'a@example.test'},expires:'2099-01-01'});
 if(path.includes('/comments?'))return Response.json({comments:[]});
 if(path.includes('/versions?'))return Response.json({versions:[],latest_version:1,shared_version:1});
 if(path.includes('/design'))return Response.json({page_id:'page',authority_revision:'1',can_edit:true,version:1,latest_version:1,shared_version:1,nodes:[],order:[],actor:{id:'alice',name:'Alice'}});
 throw Error('unexpected '+path);
};
function Canvas(){f.design=useDesign('w','slug');return null;}
function App(){return useAuth().session?<Canvas/>:null;}
createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><AuthProvider><App/></AuthProvider></QueryClientProvider>);
f.edit=id=>upsertDesignNode(f.design.doc,{id,type:'text',text:id,x:0,y:0,width:100,height:40});
f.stored=()=>artifactDraftStorageState(artifactDraftKey('design-canvas','alice','w','page'))==='durable';
f.ids=()=>f.design.nodes.map(node=>node.id).sort();
`
type Tabs = { design: { editable: boolean } | null; edit(id: string): void; stored(): boolean; ids(): string[] }
it.skipIf(!existsSync(chromium.executablePath()))("keeps the offline work of a killed tab when another tab stores the canvas", async () => {
 const built = buildSync({ stdin: { contents: fixture, loader: "tsx", resolveDir: process.cwd() }, bundle: true, write: false, platform: "browser", format: "iife", define: { "process.env.NODE_ENV": '"test"' } })
 const browser = await chromium.launch({ headless: true })
 try {
  const context = await browser.newContext()
  await context.route("https://design-tabs.test/**", route => route.fulfill({ body: '<div id="root"></div>', contentType: "text/html" }))
  const open = async () => {
   const page = await context.newPage()
   await installLocalDraftKeyFixture(page, { human: () => "alice" })
   await page.goto("https://design-tabs.test/pages/design")
   await page.addScriptTag({ content: built.outputFiles[0].text })
   await page.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.design?.editable)
   return page
  }
  const edit = async (page: Page, id: string) => {
   await page.evaluate(value => (window as unknown as { designTabs: Tabs }).designTabs.edit(value), id)
   await page.waitForFunction(() => (window as unknown as { designTabs: Tabs }).designTabs.stored())
  }
  const first = await open(), second = await open()
  await edit(first, "from-first")
  await edit(second, "from-second")
  await first.close({ runBeforeUnload: false })
  await second.close({ runBeforeUnload: false })
  const reopened = await open()
  expect(await reopened.evaluate(() => (window as unknown as { designTabs: Tabs }).designTabs.ids())).toEqual(["from-first", "from-second"])
 } finally { await browser.close() }
}, 30_000)
