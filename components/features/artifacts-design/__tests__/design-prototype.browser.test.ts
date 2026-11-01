// @vitest-environment node
import { existsSync } from "node:fs"
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"

// Real editor, player, Yjs document and keyboard focus in Chromium. Only the
// session, agent dispatch and collaboration hook are fixtures.
it.skipIf(!existsSync(chromium.executablePath()))("plays three linked screens A to B to C and goes back to B without changing the canvas", async () => {
 const bundle = await build({
  stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
   import { createRoot } from "react-dom/client";
   import * as Y from "yjs";
   import { DesignEditor } from "./components/features/artifacts-design/design-editor";
   import { designNodes, setDesignNodeLink, upsertDesignNode } from "./components/features/artifacts-design/design-document";
   const doc = new Y.Doc();
   ["a","b","c"].forEach((id,index)=>upsertDesignNode(doc,{id,type:"frame",name:"Screen "+id.toUpperCase(),x:64+index*424,y:64,width:360,height:640,fill:"#ffffff"}));
   upsertDesignNode(doc,{id:"to-b",type:"rect",parentId:"a",name:"Next",x:24,y:24,width:160,height:48,fill:"#2563eb"});
   upsertDesignNode(doc,{id:"to-c",type:"text",parentId:"b",name:"Continue",text:"Continue",x:24,y:120,width:160,height:48,fill:"#172554"});
   setDesignNodeLink(doc,"to-b","b"); setDesignNodeLink(doc,"to-c","c");
   window.designUpdates = 0; doc.on("update",()=>{window.designUpdates++});
   const mutation={mutate(){},mutateAsync:async()=>({version:1}),isPending:false,error:null};
   window.designFixture={query:{data:{page_id:"page",can_edit:false,can_comment:false,latest_version:1},isError:false},comments:{data:{comments:[]}},versions:{data:{versions:[{version:1}]}},doc,nodes:designNodes(doc),peers:[],connection:"connected",editable:false,dirty:false,draftError:false,save:mutation,restore:mutation,share:mutation,comment:mutation,connectAgent:mutation,setCursor(){}};
   createRoot(document.getElementById("root")).render(<DesignEditor workspaceId="ws" slug="flow"/>);
  ` },
  bundle: true, write: false, platform: "browser", format: "iife", jsx: "automatic",
  define: { "process.env.NODE_ENV": '"production"', "process.env.NEXT_PUBLIC_ARTIFACTS_DESIGN": '"true"', "process.env.NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE": '"true"' },
  plugins: [{ name: "editor-fixtures", setup(builder) {
   builder.onResolve({ filter: /^(next\/dynamic|@\/hooks\/(use-auth|use-artifact-agent-dispatch)|\.\/use-design)$/ }, args => ({ path: args.path, namespace: "fixture" }))
   builder.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ contents:
    args.path === "next/dynamic" ? "export default ()=>()=>null" :
    args.path === "./use-design" ? "export const useDesign=()=>window.designFixture" :
    args.path.endsWith("use-auth") ? "export const useSessionSafe=()=>null" :
    "export const useArtifactAgentDispatch=()=>({ready:false,dispatch:async()=>false})"
   }))
  } }],
 })
 const browser = await chromium.launch({ headless: true })
 try {
  const page = await browser.newPage()
  await page.route("https://design-prototype.test/**", route => route.fulfill({ body: '<!doctype html><div id="root"></div>', contentType: "text/html" }))
  await page.goto("https://design-prototype.test/flow")
  await page.addScriptTag({ content: bundle.outputFiles[0].text })
  await page.getByRole("button", { name: "Play" }).click()
  const player = page.getByRole("dialog", { name: "Prototype playback" })
  await player.getByRole("heading", { name: "Screen A" }).waitFor()
  await player.getByRole("button", { name: "Next: open Screen B" }).click()
  await player.getByRole("heading", { name: "Screen B" }).waitFor()
  // Second hop by keyboard: Enter on the focused hotspot follows the link.
  await player.getByRole("button", { name: "Continue: open Screen C" }).focus()
  await page.keyboard.press("Enter")
  await player.getByRole("heading", { name: "Screen C" }).waitFor()
  await player.getByRole("button", { name: "Back" }).click()
  await player.getByRole("heading", { name: "Screen B" }).waitFor()
  await page.keyboard.press("Escape")
  expect(await page.getByRole("dialog", { name: "Prototype playback" }).count()).toBe(0)
  expect(await page.evaluate(() => document.activeElement?.textContent)).toContain("Play")
  expect(await page.evaluate(() => (window as unknown as { designUpdates: number }).designUpdates)).toBe(0)
 } finally { await browser.close() }
}, 60_000)
