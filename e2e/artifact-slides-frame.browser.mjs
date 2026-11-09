// Actual Chromium: a typed Slides deck inside the ArtifactFrame (#5150). One selected slide is
// shared by the stage, filmstrip, grid, notes and Present, and the address names it as
// #slide-<id>: a link, reload and back/forward select it, the viewer's keys move it, and leaving
// Present (Escape, Close, or the browser leaving full screen) keeps the last slide shown.
// The project snapshot, projection and auth are fixtures; this is not a live backend acceptance.
// Run: crewship-heavy node e2e/artifact-slides-frame.browser.mjs
// Screenshots: $CREWSHIP_SLIDES_FRAME_ARTIFACT_DIR (optional)
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import postcss from "postcss"
import tailwind from "@tailwindcss/postcss"

const directory = await mkdtemp(join(tmpdir(), "artifact-slides-frame-"))
const artifacts = process.env.CREWSHIP_SLIDES_FRAME_ARTIFACT_DIR
const ORIGIN = "https://slides-frame.test"
const DECK_URL = `${ORIGIN}/artifacts/page?workspace_id=workspace`
let browser
try {
  if (artifacts) await mkdir(artifacts, { recursive: true })
  const projection = JSON.parse(await readFile("testdata/artifact-slides-projection.json", "utf8"))
  projection.deck.slide_order.push("closing")
  projection.deck.slides.push({ id: "closing", notes: "Thank the board", elements: [{ id: "thanks", type: "text", x: 40, y: 40, width: 1000, height: 100, text: { value: "Thank you", font_family: "Go Regular", font_size: 32, color: "#111111", align: "left" } }] })
  projection.anchors.thanks = { slide_id: "closing", path: "slides/closing.html", selector: "#thanks", quote: "Thank you" }
  const FIXTURES = {
    auth: 'export const useSessionSafe=()=>({data:{user:{id:"person"}}});export const useSession=useSessionSafe;',
    agent: "export const useArtifactAgentDispatch=()=>({ready:true,dispatch:async()=>true});",
    seal: "export const createLocalDraftCipher=async()=>({seal:async t=>t,open:async t=>t,close(){}});export function forgetLocalDraftPage(){};export const isSealedLocalDraft=()=>true;export class LocalDraftReadRevoked extends Error{};",
    project: `const snapshot={page_id:"page",artifact_kind:"slides",can_write:true,can_share:false,can_restore:false,can_comment:true,can_send_to_agent:false,latest_version:2,shared_version:1,origin:null,
      manifest:{version:2,entrypoint:"slides/intro.html",files:[{path:"deck.json",media_type:"application/json",content:"{}"}]},versions:[{version:2,message:"Board update"}],comments:[]};
      const idle={mutate(){},isPending:false};
      export const useArtifactProjectSnapshot=()=>({data:snapshot,error:null,refetch(){}});
      export const useArtifactProjectMutations=()=>({restore:idle,share:idle,comment:idle});`,
    slides: `import {parseArtifactSlides} from "@/lib/pages/artifact-slides";const projection=parseArtifactSlides(${JSON.stringify(projection)},"page");
      export const useArtifactSlides=()=>({scope:"s",enabled:true,projection,error:false,downloadPDF:async()=>null});`,
  }
  const FIXTURE_OF = [
    [/^@\/hooks\/use-auth$/, "auth"], [/^@\/hooks\/use-artifact-agent-dispatch$/, "agent"], [/^@\/lib\/local-draft-seal$/, "seal"],
    [/\/projects\/use-artifact-project-data$/, "project"], [/\/projects\/use-artifact-slides$/, "slides"],
  ]
  await build({
    stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
      import React from "react";
      import {createRoot} from "react-dom/client";
      import {ArtifactFrame, NO_FRAME_CAPABILITIES} from "./components/features/artifacts-library/frame";
      import {SlidesBody} from "./components/features/artifacts-library/kinds/slides/slides-detail";
      function App(){
        const [frame,setFrame]=React.useState(null);
        return <div style={{height:"100vh",display:"flex"}}><ArtifactFrame name="Q4 board update" kind={{kind:"slides"}} onHome={()=>{}} layout="desktop"
          caps={{...NO_FRAME_CAPABILITIES,canEdit:frame?.canEdit===true,status:frame?.status??null,agent:{ask:{chatHref:()=>"/chat"}}}}>
          <SlidesBody workspaceId="workspace" slug="q4" onFrame={setFrame}/>
        </ArtifactFrame></div>;
      }
      createRoot(document.querySelector("#root")).render(<App/>);
    ` },
    bundle: true, format: "iife", platform: "browser", outdir: directory, entryNames: "bundle", external: ["/fonts/*"], jsx: "automatic",
    alias: { "@": process.cwd() }, logLevel: "error",
    define: { "process.env": JSON.stringify({ NODE_ENV: "production", NEXT_PUBLIC_ARTIFACT_SLIDES: "true", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true" }) },
    plugins: [{ name: "fixture-neighbours", setup(b) {
      b.onResolve({ filter: /.*/ }, args => { const hit = FIXTURE_OF.find(([pattern]) => pattern.test(args.path)); return hit ? { path: hit[1], namespace: "fixture" } : undefined })
      b.onResolve({ filter: /^@\/lib\/pages\/artifact-slides$/, namespace: "fixture" }, () => ({ path: join(process.cwd(), "lib/pages/artifact-slides.ts") }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ contents: FIXTURES[args.path], loader: "js", resolveDir: process.cwd() }))
    } }],
  })
  const stylesheet = await postcss([tailwind()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  const bundle = await readFile(join(directory, "bundle.js"), "utf8")
  const bundleCSS = await readFile(join(directory, "bundle.css"), "utf8").catch(() => "")
  browser = await chromium.launch({ headless: true, ...(process.env.CREWSHIP_BROWSER ? { executablePath: process.env.CREWSHIP_BROWSER } : {}) })
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } }), external = []
  await page.route("**/*", async route => {
    const url = new URL(route.request().url())
    if (url.origin !== ORIGIN) { external.push(url.href); return route.abort() }
    if (url.pathname === "/artifacts/page" || url.pathname === "/artifacts") return route.fulfill({ contentType: "text/html", body: '<!doctype html><html><head><link rel="stylesheet" href="/app.css"><link rel="stylesheet" href="/bundle.css"></head><body><div id="root"></div><script src="/bundle.js"></script></body></html>' })
    if (url.pathname === "/app.css") return route.fulfill({ contentType: "text/css", body: stylesheet.css })
    if (url.pathname === "/bundle.css") return route.fulfill({ contentType: "text/css", body: bundleCSS })
    if (url.pathname === "/bundle.js") return route.fulfill({ contentType: "text/javascript", body: bundle })
    if (/^\/fonts\/[A-Za-z0-9_./-]+\.(woff2|ttf)$/.test(url.pathname) && !url.pathname.includes("..")) return route.fulfill({ contentType: url.pathname.endsWith(".woff2") ? "font/woff2" : "font/ttf", body: await readFile(join("public", url.pathname)) })
    if (url.pathname === "/favicon.ico") return route.fulfill({ status: 404, body: "" })
    // The comment composer asks for its local-draft key; refused here, so no draft is kept.
    if (url.pathname === "/api/v1/pages/@page/draft-key") return route.fulfill({ status: 404, contentType: "application/json", body: "{}" })
    external.push(url.href)
    return route.abort()
  })
  const stage = page.getByRole("region", { name: "Slides", exact: true })
  const at = async (expected, hash) => {
    await page.waitForFunction(text => document.querySelector('[data-artifact-slot="nav"] [aria-live="polite"]')?.textContent === text, expected)
    if (hash) assert.equal(new URL(page.url()).hash, hash)
  }
  const shot = name => artifacts ? page.screenshot({ path: join(artifacts, `${name}.png`) }) : null

  // A link names the slide; the query is kept as it is.
  await page.goto(`${DECK_URL}#slide-details`)
  await stage.waitFor()
  await at("2 / 3", "#slide-details")
  assert.equal(new URL(page.url()).search, "?workspace_id=workspace")
  await shot("01-link-slide-2")

  // The viewer's keys move the same selection; the address follows.
  await page.locator("body").press("ArrowRight"); await at("3 / 3", "#slide-closing")
  await page.locator("body").press("ArrowRight"); await at("3 / 3", "#slide-closing")
  await page.locator("body").press("Home"); await at("1 / 3", "#slide-intro")
  await page.locator("body").press("ArrowLeft"); await at("1 / 3", "#slide-intro")
  await page.locator("body").press("End"); await at("3 / 3", "#slide-closing")
  await page.locator("body").press("PageUp"); await at("2 / 3", "#slide-details")

  // Filmstrip, grid and notes share it.
  await page.getByRole("navigation", { name: "Slide thumbnails" }).getByRole("button", { name: "Slide 3", exact: true }).click()
  await at("3 / 3", "#slide-closing")
  await page.getByRole("button", { name: "Speaker notes" }).click()
  await page.getByRole("complementary", { name: "Speaker notes" }).getByText("Thank the board").waitFor()
  await page.getByRole("button", { name: "All slides" }).click()
  assert.equal(await page.getByRole("list", { name: "All slides" }).getByRole("button").nth(2).getAttribute("aria-current"), "true")
  await shot("02-grid-keeps-slide-3")
  await page.getByRole("list", { name: "All slides" }).getByRole("button").nth(1).click()
  await at("2 / 3", "#slide-details")
  await page.getByRole("complementary", { name: "Speaker notes" }).getByText("No notes for this slide.").waitFor()

  // Reload keeps the slide.
  await page.reload(); await stage.waitFor(); await at("2 / 3", "#slide-details")

  // Back and forward: leave the deck and come back to the same slide.
  await page.locator("body").press("ArrowRight"); await at("3 / 3", "#slide-closing")
  await page.goto(`${ORIGIN}/artifacts?workspace_id=workspace`)
  await page.goBack(); await stage.waitFor(); await at("3 / 3", "#slide-closing")
  await page.goForward(); await page.goBack(); await stage.waitFor(); await at("3 / 3", "#slide-closing")
  await shot("03-back-restores-slide-3")

  // Present 2 → 3 → Escape leaves the viewer on 3.
  await page.getByRole("navigation", { name: "Slide thumbnails" }).getByRole("button", { name: "Slide 2", exact: true }).click()
  await at("2 / 3", "#slide-details")
  await page.getByRole("button", { name: "Present", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "Present slides" })
  await dialog.waitFor()
  await dialog.getByText("2 / 3", { exact: true }).waitFor()
  assert.equal(await dialog.getByText("Thank the board").count(), 0, "Present never shows speaker notes")
  await dialog.press("ArrowRight"); await dialog.getByText("3 / 3", { exact: true }).waitFor()
  await dialog.press("ArrowRight"); await dialog.getByText("3 / 3", { exact: true }).waitFor()
  await shot("04-present-slide-3")
  // In real full screen the browser takes Escape itself; leaving full screen must end Present too.
  const fullscreen = await page.evaluate(() => Boolean(document.fullscreenElement))
  if (fullscreen) await page.evaluate(() => document.exitFullscreen())
  else await dialog.press("Escape")
  await dialog.waitFor({ state: "detached" })
  await at("3 / 3", "#slide-closing")
  await shot("05-after-present-slide-3")

  // First slide bound, Home/End, and Close presentation.
  await page.locator("body").press("Home"); await at("1 / 3", "#slide-intro")
  await page.getByRole("button", { name: "Present", exact: true }).click()
  await dialog.waitFor()
  await dialog.press("ArrowLeft"); await dialog.getByText("1 / 3", { exact: true }).waitFor()
  await dialog.press("End"); await dialog.getByText("3 / 3", { exact: true }).waitFor()
  await dialog.press("PageUp"); await dialog.getByText("2 / 3", { exact: true }).waitFor()
  await dialog.getByRole("button", { name: "Close presentation" }).click()
  await dialog.waitFor({ state: "detached" })
  await at("2 / 3", "#slide-details")
  // Escape without full screen ends Present too.
  await page.getByRole("button", { name: "Present", exact: true }).click()
  await dialog.waitFor(); await dialog.press("End")
  if (await page.evaluate(() => Boolean(document.fullscreenElement))) await page.evaluate(() => document.exitFullscreen())
  await page.waitForFunction(() => !document.fullscreenElement)
  if (await dialog.count()) await dialog.press("Escape")
  await dialog.waitFor({ state: "detached" })
  await at("3 / 3", "#slide-closing")

  assert.deepEqual(external, [])
  process.stdout.write(`Chromium Slides frame: link/reload/back-forward/keys/filmstrip/grid/notes/Present return passed (fixture snapshot; full screen ${fullscreen ? "entered and exited" : "unavailable, Escape path"}).\n`)
} finally {
  await browser?.close()
  await rm(directory, { recursive: true, force: true })
}
