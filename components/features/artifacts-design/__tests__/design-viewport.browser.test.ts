// @vitest-environment node
import { existsSync } from "node:fs"
import { build } from "esbuild"
import { chromium, type Browser, type Page } from "@playwright/test"
import { afterAll, beforeAll, describe, expect, it } from "vitest"

// The real editor inside the real ArtifactFrame, a live Yjs document and real
// Chromium pointer, wheel, keyboard and touch input. Only the session, agent
// dispatch and collaboration hook are fixtures; the fixture hook re-reads the
// document on every update, so moves render as they do live.
const ready = existsSync(chromium.executablePath())
let bundle = ""
let browser: Browser

async function fixtureBundle(): Promise<string> {
 const out = await build({
  stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
   import { createRoot } from "react-dom/client";
   import * as Y from "yjs";
   import { DesignEditor } from "./components/features/artifacts-design/design-editor";
   import { designNodes, upsertDesignNode } from "./components/features/artifacts-design/design-document";
   import { ArtifactFrame, NO_FRAME_CAPABILITIES } from "./components/features/artifacts-library/frame";
   const options = Object.assign({ editable: true, layout: "desktop", framed: true, far: false }, window.fixtureOptions);
   const doc = new Y.Doc();
   upsertDesignNode(doc,{id:"screen",type:"frame",name:"Screen A",x:64,y:64,width:600,height:400,fill:"#ffffff"});
   upsertDesignNode(doc,{id:"cta",type:"rect",parentId:"screen",name:"Call to action",x:40,y:40,width:200,height:100,fill:"#2563eb"});
   if (options.far) upsertDesignNode(doc,{id:"far",type:"rect",name:"Far left",x:-500,y:-300,width:120,height:80,fill:"#16a34a"});
   window.designUpdates = 0; doc.on("update",()=>{window.designUpdates++});
   window.nodesJSON = () => JSON.stringify(designNodes(doc));
   window.cursors = []; window.menus = [];
   window.addEventListener("contextmenu", event => window.menus.push({ prevented: event.defaultPrevented, inCanvas: Boolean(event.target.closest && event.target.closest('[aria-label="Design canvas viewport"]')) }));
   const mutation={mutate(){},mutateAsync:async()=>({version:1}),isPending:false,isSuccess:false,error:null};
   window.designFixture={conflicts:[],dismissConflict(){},query:{data:{page_id:"page",can_edit:options.editable,can_comment:true,can_share:true,latest_version:1},isError:false},comments:{data:{comments:[]}},versions:{data:{versions:[{version:1}]}},doc,peers:[],connection:"connected",editable:options.editable,dirty:false,unsynced:false,draftError:false,save:mutation,restore:mutation,share:mutation,comment:mutation,connectAgent:mutation,setCursor(cursor){window.cursors.push(cursor)}};
   const caps = { ...NO_FRAME_CAPABILITIES, canEdit: options.editable };
   const editor = <DesignEditor workspaceId="ws" slug="gestures"/>;
   createRoot(document.getElementById("root")).render(<div style={{height:"100vh",display:"flex",flexDirection:"column"}}>{options.framed ? <ArtifactFrame name="Gestures" kind={{kind:"design",mode:"canvas"}} caps={caps} onHome={()=>{}} layout={options.layout}>{editor}</ArtifactFrame> : editor}</div>);
  ` },
  bundle: true, write: false, platform: "browser", format: "iife", jsx: "automatic",
  // The frame reads other build flags; they are all off here.
  banner: { js: "var process={env:{}};" },
  define: { "process.env.NODE_ENV": '"production"', "process.env.NEXT_PUBLIC_ARTIFACTS_DESIGN": '"true"' },
  plugins: [{ name: "editor-fixtures", setup(builder) {
   builder.onResolve({ filter: /^(next\/dynamic|next\/navigation|@\/hooks\/(use-auth|use-artifact-agent-dispatch)|\.\/use-design)$/ }, args => ({ path: args.path, namespace: "fixture" }))
   builder.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ resolveDir: process.cwd(), loader: "tsx", contents:
    args.path === "next/dynamic" ? "export default ()=>()=>null" :
    args.path === "next/navigation" ? "export const useRouter=()=>({push(){},replace(){},back(){}});export const usePathname=()=>'/artifacts';export const useSearchParams=()=>new URLSearchParams()" :
    args.path === "./use-design" ? `import { useEffect, useState } from "react"; import { designNodes } from "./components/features/artifacts-design/design-document";
     export const useDesign=()=>{const [,tick]=useState(0);const doc=window.designFixture.doc;useEffect(()=>{const f=()=>tick(n=>n+1);doc.on("update",f);return()=>doc.off("update",f)},[doc]);return {...window.designFixture,nodes:designNodes(doc)}}` :
    args.path.endsWith("use-auth") ? "export const useSessionSafe=()=>null" :
    "export const useArtifactAgentDispatch=()=>({ready:false,dispatch:async()=>false})",
   }))
  } }],
 })
 return out.outputFiles[0].text
}

// No Tailwind here: the canvas viewport gets the fixed box the frame gives it in
// the app (1000 × 600 at 140,100), with the few utilities the canvas relies on.
const LAYOUT = `
 [aria-label="Design canvas viewport"]{position:fixed;left:140px;top:100px;width:1000px;height:600px;overflow:hidden;touch-action:none;user-select:none;background:#f4f4f5;z-index:1}
 .absolute{position:absolute}.left-0{left:0}.top-0{top:0}.max-w-none{max-width:none}.hidden{display:none}
`

interface FixtureOptions { editable?: boolean; layout?: "desktop" | "phone"; framed?: boolean; far?: boolean }
async function open(touch = false, options: FixtureOptions = {}): Promise<Page> {
 const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, hasTouch: touch })
 const page = await context.newPage()
 const errors: string[] = []
 page.on("pageerror", error => errors.push(error.message))
 await page.route("https://design-viewport.test/**", route => route.fulfill({ body: `<!doctype html><html><head><style>${LAYOUT}</style></head><body style="margin:0"><div id="root"></div></body></html>`, contentType: "text/html" }))
 await page.goto("https://design-viewport.test/canvas")
 await page.addScriptTag({ content: `window.fixtureOptions=${JSON.stringify(options)}` })
 await page.addScriptTag({ content: bundle })
 await page.locator('[data-design-node="cta"]').waitFor({ timeout: 15_000 }).catch(error => { throw new Error(`${error.message}\npage errors: ${errors.join(" | ")}\n${bundle.length}`) })
 return page
}

const viewOf = async (page: Page) => {
 const [scale, x, y] = (await page.getByLabel("Design canvas viewport").getAttribute("data-design-view"))!.split(" ").map(Number)
 return { scale, x, y }
}
const nodes = (page: Page) => page.evaluate(() => (window as unknown as { nodesJSON: () => string }).nodesJSON())
const updates = (page: Page) => page.evaluate(() => (window as unknown as { designUpdates: number }).designUpdates)
const cta = async (page: Page) => (await page.locator('[data-design-node="cta"] rect').first().boundingBox())!
const ctaNode = async (page: Page) => (JSON.parse(await nodes(page)) as { id: string; x: number; y: number }[]).find(node => node.id === "cta")!
const selectedOutline = (page: Page) => page.locator('[data-design-node="cta"] rect[stroke="#2563eb"]').count()
const background = async (page: Page) => { const box = (await page.getByLabel("Design canvas viewport").boundingBox())!; return { x: box.x + 20, y: box.y + box.height / 2 } }

async function drag(page: Page, from: { x: number; y: number }, by: { x: number; y: number }, button: "left" | "right" | "middle" = "left") {
 await page.mouse.move(from.x, from.y)
 await page.mouse.down({ button })
 await page.mouse.move(from.x + by.x, from.y + by.y, { steps: 6 })
 await page.mouse.up({ button })
}

describe.skipIf(!ready)("Design canvas viewport in Chromium", () => {
 beforeAll(async () => { bundle = await fixtureBundle(); browser = await chromium.launch({ headless: true }) }, 60_000)
 afterAll(async () => { await browser?.close() })

 it("pans with right, middle and Space + left drag over a layer and the background without changing the design", async () => {
  const page = await open()
  const before = await nodes(page)
  const writes = await updates(page)
  const box = await cta(page)
  const center = { x: box.x + box.width / 2, y: box.y + box.height / 2 }

  // Right drag starting on the layer: the view moves, the layer does not.
  let view = await viewOf(page)
  await drag(page, center, { x: 120, y: 80 }, "right")
  let after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(120, 0)
  expect(after.y - view.y).toBeCloseTo(80, 0)
  expect((await cta(page)).x - box.x).toBeCloseTo(120, 0)
  expect(await selectedOutline(page)).toBe(0)

  // Right drag on the empty canvas.
  view = after
  await drag(page, await background(page), { x: -60, y: 30 }, "right")
  after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(-60, 0)
  expect(after.y - view.y).toBeCloseTo(30, 0)

  // Middle drag.
  view = after
  await drag(page, await background(page), { x: 40, y: -50 }, "middle")
  after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(40, 0)
  expect(after.y - view.y).toBeCloseTo(-50, 0)

  // Space + left drag starting on the layer.
  view = after
  const now = await cta(page)
  await page.mouse.move(now.x + 10, now.y + 10)
  await page.keyboard.down("Space")
  await drag(page, { x: now.x + 10, y: now.y + 10 }, { x: -30, y: -20 })
  await page.keyboard.up("Space")
  after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(-30, 0)
  expect(after.y - view.y).toBeCloseTo(-20, 0)

  // Nothing about the design changed, and no browser menu opened in the canvas.
  expect(await nodes(page)).toBe(before)
  expect(await updates(page)).toBe(writes)
  expect(await selectedOutline(page)).toBe(0)
  const menus = await page.evaluate(() => (window as unknown as { menus: { prevented: boolean; inCanvas: boolean }[] }).menus)
  expect(menus.length).toBeGreaterThan(0)
  expect(menus.every(menu => menu.inCanvas && menu.prevented)).toBe(true)
  await page.context().close()
 }, 60_000)

 it("never moves or selects a layer with a right-button drag (V03 regression: main moved it)", async () => {
  const page = await open()
  const before = await nodes(page)
  const box = await cta(page)
  await drag(page, { x: box.x + box.width / 2, y: box.y + box.height / 2 }, { x: 150, y: 90 }, "right")
  expect(await nodes(page)).toBe(before)
  expect(await selectedOutline(page)).toBe(0)
  await page.context().close()
 }, 60_000)

 it("keeps the browser menu outside the canvas", async () => {
  const page = await open()
  await page.evaluate(() => {
   const outside = document.createElement("p"); outside.id = "outside"; outside.textContent = "outside"
   document.body.prepend(outside)
  })
  await page.locator("#outside").click({ button: "right" })
  const menus = await page.evaluate(() => (window as unknown as { menus: { prevented: boolean; inCanvas: boolean }[] }).menus)
  expect(menus).toEqual([{ prevented: false, inCanvas: false }])
  await page.context().close()
 }, 60_000)

 it("pans with the wheel or a two-finger scroll and zooms around the pointer with Ctrl or Cmd + wheel", async () => {
  const page = await open()
  const before = await nodes(page)
  const pointer = { x: 700, y: 420 }
  await page.mouse.move(pointer.x, pointer.y)
  let view = await viewOf(page)
  await page.mouse.wheel(30, 120)
  let after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(-30, 0)
  expect(after.y - view.y).toBeCloseTo(-120, 0)
  expect(after.scale).toBe(view.scale)

  const viewportBox = (await page.getByLabel("Design canvas viewport").boundingBox())!
  const anchor = { x: pointer.x - viewportBox.x, y: pointer.y - viewportBox.y }
  for (const key of ["Control", "Meta"]) {
   view = await viewOf(page)
   const under = { x: (anchor.x - view.x) / view.scale, y: (anchor.y - view.y) / view.scale }
   await page.keyboard.down(key)
   await page.mouse.wheel(0, -100)
   await page.keyboard.up(key)
   after = await viewOf(page)
   expect(after.scale).toBeGreaterThan(view.scale)
   expect((anchor.x - after.x) / after.scale).toBeCloseTo(under.x, 1)
   expect((anchor.y - after.y) / after.scale).toBeCloseTo(under.y, 1)
  }
  // Fit returns to the fitted view.
  await page.getByRole("button", { name: "Fit" }).dispatchEvent("click")
  expect(await page.getByRole("button", { name: "Fit" }).getAttribute("aria-pressed")).toBe("true")
  expect(await nodes(page)).toBe(before)
  await page.context().close()
 }, 60_000)

 it("moves a layer with a left drag, deselects on the background, and Escape, pointercancel and blur put the layer back", async () => {
  const page = await open()
  const start = await ctaNode(page)
  const { scale } = await viewOf(page)
  let box = await cta(page)
  await drag(page, { x: box.x + 20, y: box.y + 20 }, { x: 60, y: 40 })
  const moved = await ctaNode(page)
  expect(moved.x).toBe(Math.round(start.x + 60 / scale))
  expect(moved.y).toBe(Math.round(start.y + 40 / scale))
  expect(await selectedOutline(page)).toBe(1)

  // A click without travel selects and writes nothing.
  const writes = await updates(page)
  box = await cta(page)
  await page.mouse.click(box.x + 20, box.y + 20)
  expect(await updates(page)).toBe(writes)

  for (const interrupt of ["Escape", "pointercancel", "blur"] as const) {
   box = await cta(page)
   await page.mouse.move(box.x + 20, box.y + 20)
   await page.mouse.down()
   await page.mouse.move(box.x + 120, box.y + 90, { steps: 5 })
   expect((await ctaNode(page)).x).not.toBe(moved.x)
   if (interrupt === "Escape") await page.keyboard.press("Escape")
   else if (interrupt === "blur") await page.evaluate(() => window.dispatchEvent(new Event("blur")))
   else await page.getByLabel("Design canvas viewport").dispatchEvent("pointercancel", { pointerId: 1, pointerType: "mouse", bubbles: true })
   const restored = await ctaNode(page)
   expect([interrupt, restored.x, restored.y]).toEqual([interrupt, moved.x, moved.y])
   // The rest of the press does nothing: no hanging drag.
   await page.mouse.move(box.x + 200, box.y + 150, { steps: 3 })
   await page.mouse.up()
   expect((await ctaNode(page)).x).toBe(moved.x)
  }

  // Escape during a pan restores the view.
  const view = await viewOf(page)
  const bg = await background(page)
  await page.mouse.move(bg.x, bg.y)
  await page.mouse.down({ button: "right" })
  await page.mouse.move(bg.x + 90, bg.y + 90, { steps: 4 })
  await page.keyboard.press("Escape")
  await page.mouse.up({ button: "right" })
  expect(await viewOf(page)).toEqual(view)

  // Left on the background clears the selection.
  await page.mouse.click(bg.x, bg.y)
  expect(await selectedOutline(page)).toBe(0)
  await page.context().close()
 }, 60_000)

 it("pans with one finger on the background and pinch-zooms around the fingers on touch", async () => {
  const page = await open(true)
  const before = await nodes(page)
  const cdp = await page.context().newCDPSession(page)
  const touch = (type: "touchStart" | "touchMove" | "touchEnd", points: { x: number; y: number }[]) =>
   cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points.map((point, id) => ({ ...point, id })) })
  const bg = await background(page)
  let view = await viewOf(page)
  await touch("touchStart", [bg])
  for (let i = 1; i <= 5; i++) await touch("touchMove", [{ x: bg.x + i * 20, y: bg.y + i * 10 }])
  await touch("touchEnd", [])
  let after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(100, 0)
  expect(after.y - view.y).toBeCloseTo(50, 0)

  const viewportBox = (await page.getByLabel("Design canvas viewport").boundingBox())!
  const mid = { x: viewportBox.x + 640, y: viewportBox.y + 400 }
  view = after
  const under = { x: (mid.x - viewportBox.x - view.x) / view.scale, y: (mid.y - viewportBox.y - view.y) / view.scale }
  await touch("touchStart", [{ x: mid.x - 50, y: mid.y }])
  await touch("touchStart", [{ x: mid.x - 50, y: mid.y }, { x: mid.x + 50, y: mid.y }])
  for (let i = 1; i <= 5; i++) await touch("touchMove", [{ x: mid.x - 50 - i * 10, y: mid.y }, { x: mid.x + 50 + i * 10, y: mid.y }])
  await touch("touchEnd", [])
  after = await viewOf(page)
  expect(after.scale / view.scale).toBeCloseTo(2, 1)
  expect((mid.x - viewportBox.x - after.x) / after.scale).toBeCloseTo(under.x, 0)
  expect((mid.y - viewportBox.y - after.y) / after.scale).toBeCloseTo(under.y, 0)
  // A second finger joining a one-finger pan continues from where the pan is, without a jump.
  await touch("touchStart", [bg])
  for (let i = 1; i <= 3; i++) await touch("touchMove", [{ x: bg.x + i * 20, y: bg.y }])
  const panned = await viewOf(page)
  await touch("touchStart", [{ x: bg.x + 60, y: bg.y }, { x: bg.x + 160, y: bg.y }])
  await touch("touchMove", [{ x: bg.x + 60, y: bg.y }, { x: bg.x + 160, y: bg.y }])
  expect(await viewOf(page)).toEqual(panned)
  await touch("touchEnd", [])
  expect(await nodes(page)).toBe(before)
  await page.context().close()
 }, 60_000)
 it.each([
  ["a reader", { editable: false }, false],
  ["a phone review", { layout: "phone" as const }, true],
 ])("never writes for %s: a left or finger drag on a layer selects or pans, never moves", async (_who, options, touch) => {
  const page = await open(touch, options)
  const before = await nodes(page)
  const writes = await updates(page)
  const box = await cta(page)
  const from = { x: box.x + 20, y: box.y + 20 }
  if (touch) {
   const cdp = await page.context().newCDPSession(page)
   const send = (type: "touchStart" | "touchMove" | "touchEnd", points: { x: number; y: number }[]) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points.map((point, id) => ({ ...point, id })) })
   const view = await viewOf(page)
   await send("touchStart", [from])
   for (let i = 1; i <= 4; i++) await send("touchMove", [{ x: from.x + i * 15, y: from.y + i * 10 }])
   await send("touchEnd", [])
   const after = await viewOf(page)
   expect(after.x - view.x).toBeCloseTo(60, 0)
   expect(after.y - view.y).toBeCloseTo(40, 0)
  } else {
   await drag(page, from, { x: 80, y: 60 })
   expect(await selectedOutline(page)).toBe(1)
  }
  await drag(page, from, { x: 50, y: 30 }, "right")
  expect(await nodes(page)).toBe(before)
  expect(await updates(page)).toBe(writes)
  await page.context().close()
 }, 60_000)

 it("reports the pointer in canvas coordinates after pan and zoom, as peers draw it", async () => {
  const page = await open()
  await drag(page, await background(page), { x: 70, y: -40 }, "right")
  await page.mouse.move(700, 420)
  await page.keyboard.down("Control"); await page.mouse.wheel(0, -100); await page.keyboard.up("Control")
  // The layer's centre on screen is the layer's centre on the canvas: 64+40+100, 64+40+50.
  const box = await cta(page)
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  const last = await page.evaluate(() => { const list = (window as unknown as { cursors: ({ x: number; y: number } | null)[] }).cursors; return list[list.length - 1] })
  expect(last!.x).toBeCloseTo(204, 0)
  expect(last!.y).toBeCloseTo(154, 0)
  await page.context().close()
 }, 60_000)

 it("adds up wheel events that arrive before a render", async () => {
  const page = await open()
  const view = await viewOf(page)
  await page.getByLabel("Design canvas viewport").evaluate(element => {
   for (let i = 0; i < 2; i++) element.dispatchEvent(new WheelEvent("wheel", { deltaY: 100, clientX: 600, clientY: 400, bubbles: true, cancelable: true }))
  })
  const after = await viewOf(page)
  expect(after.y - view.y).toBeCloseTo(-200, 0)
  await page.context().close()
 }, 60_000)

 it("zooms with Safari's trackpad gesture events around the pointer", async () => {
  const page = await open()
  const view = await viewOf(page)
  const viewportBox = (await page.getByLabel("Design canvas viewport").boundingBox())!
  const point = { x: viewportBox.x + 500, y: viewportBox.y + 300 }
  const under = { x: (500 - view.x) / view.scale, y: (300 - view.y) / view.scale }
  await page.getByLabel("Design canvas viewport").evaluate((element, at) => {
   const fire = (type: string, scale: number) => element.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), { scale, clientX: at.x, clientY: at.y }))
   fire("gesturestart", 1); fire("gesturechange", 1.5); fire("gestureend", 1.5)
  }, point)
  const after = await viewOf(page)
  expect(after.scale / view.scale).toBeCloseTo(1.5, 2)
  expect((500 - after.x) / after.scale).toBeCloseTo(under.x, 1)
  expect((300 - after.y) / after.scale).toBeCloseTo(under.y, 1)
  await page.context().close()
 }, 60_000)

 it("draws layers at negative coordinates and fits them", async () => {
  const page = await open(false, { far: true })
  const far = (await page.locator('[data-design-node="far"] rect').first().boundingBox())!
  const viewportBox = (await page.getByLabel("Design canvas viewport").boundingBox())!
  expect(far.width).toBeGreaterThan(0)
  expect(far.x).toBeGreaterThanOrEqual(viewportBox.x)
  expect(far.y).toBeGreaterThanOrEqual(viewportBox.y)
  await page.context().close()
 }, 60_000)

 it("pans, zooms and fits in the standalone editor too", async () => {
  const page = await open(false, { framed: false })
  const before = await nodes(page)
  let view = await viewOf(page)
  const box = await cta(page)
  await drag(page, { x: box.x + 20, y: box.y + 20 }, { x: 40, y: 30 }, "right")
  let after = await viewOf(page)
  expect(after.x - view.x).toBeCloseTo(40, 0)
  await page.mouse.move(600, 400)
  await page.keyboard.down("Control"); await page.mouse.wheel(0, 100); await page.keyboard.up("Control")
  view = after; after = await viewOf(page)
  expect(after.scale).toBeLessThan(view.scale)
  await page.getByRole("button", { name: "Fit" }).dispatchEvent("click")
  expect(await page.getByRole("button", { name: "Fit" }).getAttribute("aria-pressed")).toBe("true")
  expect(await nodes(page)).toBe(before)
  await page.context().close()
 }, 60_000)

 it("ends a pinch when a finger lifts or on Escape, and the fingers left start nothing; the next touch works again", async () => {
  const page = await open(true)
  // Explicit touch pointer events: CDP cannot lift one finger of two.
  // The first finger down of a touch is the primary one (10 here, and 12 for the lost one).
  const finger = (type: "pointerdown" | "pointermove" | "pointerup", id: number, x: number, y: number) => page.getByLabel("Design canvas viewport").evaluate((element, event) => {
   element.dispatchEvent(new PointerEvent(event.type, { pointerId: event.id, pointerType: "touch", isPrimary: event.id !== 11, clientX: event.x, clientY: event.y, button: event.type === "pointermove" ? -1 : 0, buttons: event.type === "pointerup" ? 0 : 1, bubbles: true, cancelable: true }))
  }, { type, id, x, y })
  const bg = await background(page)
  const ax = bg.x + 200, bx = bg.x + 300, y = bg.y
  // Lift one finger of a pinch: the other one moving changes nothing.
  await finger("pointerdown", 10, ax, y); await finger("pointerdown", 11, bx, y)
  await finger("pointermove", 10, ax - 20, y); await finger("pointermove", 11, bx + 20, y)
  const pinched = await viewOf(page)
  await finger("pointerup", 10, ax - 20, y)
  await finger("pointermove", 11, bx + 120, y + 40)
  expect(await viewOf(page)).toEqual(pinched)
  await finger("pointerup", 11, bx + 120, y + 40)
  // Escape mid-pinch goes back to where the pinch started; the fingers left start nothing.
  await page.getByLabel("Design canvas viewport").focus()
  await finger("pointerdown", 10, ax, y); await finger("pointerdown", 11, bx, y)
  await finger("pointermove", 10, ax - 40, y); await finger("pointermove", 11, bx + 40, y)
  expect(await viewOf(page)).not.toEqual(pinched)
  await page.keyboard.press("Escape")
  expect(await viewOf(page)).toEqual(pinched)
  await finger("pointermove", 10, ax + 60, y); await finger("pointermove", 11, bx + 60, y)
  expect(await viewOf(page)).toEqual(pinched)
  await finger("pointerup", 10, ax + 60, y); await finger("pointerup", 11, bx + 60, y)
  // A one-finger pan whose pointerup was lost (it stays behind) does not block the next touch.
  await finger("pointerdown", 12, ax, y)
  for (let i = 1; i <= 2; i++) await finger("pointermove", 12, ax + i * 5, y)
  const stuck = await viewOf(page)
  // A fresh one-finger pan works.
  await finger("pointerdown", 10, ax, y)
  for (let i = 1; i <= 3; i++) await finger("pointermove", 10, ax + i * 10, y)
  await finger("pointerup", 10, ax + 30, y)
  expect((await viewOf(page)).x - stuck.x).toBeCloseTo(30, 0)
  await page.context().close()
 }, 60_000)

 it("leaves Space to a focused button while the pointer is over the canvas", async () => {
  const page = await open()
  const bg = await background(page)
  await page.mouse.move(bg.x, bg.y)
  const zoomIn = page.getByRole("button", { name: "Zoom in" })
  await zoomIn.focus()
  await page.evaluate(() => window.addEventListener("keydown", event => { if (event.key === " ") setTimeout(() => { (window as unknown as { spacePrevented: boolean }).spacePrevented = event.defaultPrevented }) }, { once: true }))
  await page.keyboard.press("Space")
  await page.waitForFunction(() => typeof (window as unknown as { spacePrevented?: boolean }).spacePrevented === "boolean")
  expect(await page.evaluate(() => (window as unknown as { spacePrevented: boolean }).spacePrevented)).toBe(false)
  // With the canvas focused, Space is the canvas's.
  await page.getByLabel("Design canvas viewport").focus()
  await page.evaluate(() => window.addEventListener("keydown", event => { if (event.key === " ") setTimeout(() => { (window as unknown as { canvasSpace: boolean }).canvasSpace = event.defaultPrevented }) }, { once: true }))
  await page.keyboard.press("Space")
  await page.waitForFunction(() => typeof (window as unknown as { canvasSpace?: boolean }).canvasSpace === "boolean")
  expect(await page.evaluate(() => (window as unknown as { canvasSpace: boolean }).canvasSpace)).toBe(true)
  await page.context().close()
 }, 60_000)
})
