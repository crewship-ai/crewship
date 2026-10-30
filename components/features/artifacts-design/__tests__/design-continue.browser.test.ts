// @vitest-environment node
import { existsSync } from "node:fs"
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import { expect, it } from "vitest"
import * as Y from "yjs"
import { designNodes } from "../design-document"

// Real editor, dialog, Yjs and apiFetch in Chromium. Only session, collaboration
// hook and HTTP responses are fixtures; server transaction proofs live in Go.
it.skipIf(!existsSync(chromium.executablePath()))("continues a full-history canvas without losing the original or replaying an expired creation", async () => {
 const bundle = await build({
  stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
   import { createRoot } from "react-dom/client";
   import * as Y from "yjs";
   import { DesignEditor } from "./components/features/artifacts-design/design-editor";
   import { designNodes, upsertDesignNode } from "./components/features/artifacts-design/design-document";
   const doc = new Y.Doc();
   upsertDesignNode(doc,{id:"local",type:"text",text:"Unsaved canvas",x:0,y:0,width:200,height:40});
   const mutation={mutate(){},mutateAsync:async()=>({version:1}),isPending:false,error:null};
   window.designFixture={query:{data:{page_id:"source",can_edit:true,can_comment:true,latest_version:1},isError:false},comments:{data:{comments:[]}},versions:{data:{versions:[{version:1}]}},doc,nodes:designNodes(doc),peers:[],connection:"connected",editable:true,dirty:true,draftError:false,save:{...mutation,error:Object.assign(new Error("History full"),{status:413})},restore:mutation,share:mutation,comment:mutation,connectAgent:mutation,setCursor(){}};
   createRoot(document.getElementById("root")).render(<DesignEditor workspaceId="ws" slug="original"/>);
  ` },
  bundle: true, write: false, platform: "browser", format: "iife", jsx: "automatic",
  define: { "process.env.NODE_ENV": '"production"', "process.env.NEXT_PUBLIC_ARTIFACTS_DESIGN": '"true"' },
  plugins: [{ name: "editor-fixtures", setup(builder) {
   builder.onResolve({ filter: /^(next\/dynamic|@\/hooks\/(use-auth|use-artifact-agent-dispatch)|\.\/use-design)$/ }, args => ({ path: args.path, namespace: "fixture" }))
   builder.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ contents:
    args.path === "next/dynamic" ? "export default ()=>()=>null" :
    args.path === "./use-design" ? "export const useDesign=()=>window.designFixture" :
    args.path.endsWith("use-auth") ? 'export const useSessionSafe=()=>({data:{user:{id:"alice"}}})' :
    "export const useArtifactAgentDispatch=()=>({ready:false,dispatch:async()=>false})"
   }))
  } }],
 })
 const browser = await chromium.launch({ headless: true })
 try {
  for (const status of [401, 201]) {
   const context = await browser.newContext()
   let creates = 0, refreshes = 0, copiedState = ""
   await context.route("https://design-continue.test/**", async route => {
    const request = route.request()
    if (request.url().includes("/api/auth/token/refresh")) {
     refreshes++
     return route.fulfill({ json: {} })
    }
    if (request.method() === "POST") {
     creates++
     const body = request.postDataJSON()
     expect(body).toMatchObject({ source_page_id: "source", expected_human: "alice" })
     copiedState = body.initial_state
     return route.fulfill({ status, json: status === 201 ? { page_id: "copied", version: 1 } : { error: "expired" } })
    }
    return route.fulfill({ body: '<!doctype html><div id="root"></div>', contentType: "text/html" })
   })
   const page = await context.newPage()
   await page.goto("https://design-continue.test/original")
   await page.addScriptTag({ content: bundle.outputFiles[0].text })
   await page.getByLabel("Comment for agent").fill("Unsent feedback")
   await page.getByRole("button", { name: "Continue in a new Design" }).click()
   await page.getByRole("button", { name: "Create new Design" }).click()
   if (status === 401) {
    await page.getByRole("dialog").getByText("The new Design could not be created.", { exact: false }).waitFor()
    await page.getByRole("button", { name: "Cancel", exact: true }).click()
    expect(await page.getByRole("link", { name: "Open new Design" }).count()).toBe(0)
   } else {
    const [popup] = await Promise.all([page.waitForEvent("popup"), page.getByRole("link", { name: "Open new Design" }).click()])
    await popup.waitForLoadState()
    expect(popup.url()).toBe("https://design-continue.test/artifacts/copied?workspace_id=ws")
   }
   expect(creates).toBe(1)
   expect(refreshes).toBe(0)
   expect(await page.getByLabel("Comment for agent").inputValue()).toBe("Unsent feedback")
   expect(await page.getByText("Unsaved canvas", { exact: true }).count()).toBeGreaterThan(0)
   const copy = new Y.Doc()
   try {
    Y.applyUpdate(copy, Buffer.from(copiedState, "base64"))
    expect(designNodes(copy)).toMatchObject([{ id: "local", text: "Unsaved canvas" }])
   } finally { copy.destroy() }
   await context.close()
  }
 } finally { await browser.close() }
}, 60_000)
