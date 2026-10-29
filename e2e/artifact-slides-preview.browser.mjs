// Actual Chromium, packaged font, React canvas/presentation and download transport.
// HTTP/auth/projection/PDF bytes are fixtures; this is not a live backend acceptance.
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import postcss from "postcss"
import tailwind from "@tailwindcss/postcss"

const directory = await mkdtemp(join(tmpdir(), "artifact-slides-preview-"))
const artifacts = process.env.CREWSHIP_SLIDES_PREVIEW_ARTIFACT_DIR
let browser
try {
  if (artifacts) await mkdir(artifacts, { recursive: true })
  const stylesheet = await postcss([tailwind()]).process(await readFile("app/globals.css", "utf8"), { from: "app/globals.css" })
  const bundle = join(directory, "preview.js")
  await build({
    stdin: { resolveDir: process.cwd(), loader: "tsx", contents: `
      import React, {useState} from 'react';
      import {createRoot} from 'react-dom/client';
      import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
      import {ArtifactSlidesPreview} from './components/features/chat/artifact-context/viewer/artifact-slides-preview';
      const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});
      function App(){
        const [path,setPath]=useState('deck.json');
        window.controls={refresh:()=>client.invalidateQueries()};
        return <QueryClientProvider client={client}><input aria-label="Draft" defaultValue="Keep my draft"/><output id="selection"/>
          <div id="viewer-frame" style={{height:560,overflow:'hidden'}}><ArtifactSlidesPreview workspaceId="workspace" pageId="page" codeVersion={2} artifactKind="slides" path={path} title="Slides" selectMode onNavigate={setPath}
            onSelect={selection=>{document.querySelector('#selection').textContent=JSON.stringify({path,...selection})}}/>
          </div>
        </QueryClientProvider>;
      }
      createRoot(document.querySelector('#root')).render(<App/>);
    ` },
    bundle: true, minify: true, format: "iife", platform: "browser", outfile: bundle, external: ["/fonts/*"],
    define: { "process.env.NEXT_PUBLIC_ARTIFACT_SLIDES": '"true"', "process.env.NODE_ENV": '"production"' },
    plugins: [{ name: "fixture-human-authority", setup(build) {
      build.onResolve({ filter: /^@\/hooks\/use-auth$/ }, () => ({ path: "auth", namespace: "fixture" }))
      build.onResolve({ filter: /^@\/lib\/api-fetch$/ }, () => ({ path: "api", namespace: "fixture" }))
      build.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ contents: args.path === "auth"
        ? 'export const useSessionSafe=()=>({data:{user:{id:"reader"}}});'
        : 'export const AUTH_EVENT="auth:session-expired";export const apiFetch=(url,init)=>fetch(url,init);', loader: "js" }))
    } }],
  })
  const projection = JSON.parse(await readFile("testdata/artifact-slides-projection.json", "utf8"))
  const literal = '<img src="https://escape.invalid/steal" onerror="window.agentExecuted=true"><script>window.agentExecuted=true</script>'
  projection.deck.slides[0].elements[0].text.value = literal
  projection.anchors.title.quote = literal
  const font = await readFile("public/fonts/go-regular/Go-Regular.ttf")
  browser = await chromium.launch({ headless: true, ...(process.env.CREWSHIP_BROWSER ? { executablePath: process.env.CREWSHIP_BROWSER } : {}) })
  const page = await browser.newPage({ acceptDownloads: true }), external = [], requests = []
  let allowed = true
  await page.route("**/*", async route => {
    const url = new URL(route.request().url())
    requests.push(url.pathname + url.search)
    if (url.href === "https://slides.test/") return route.fulfill({ contentType: "text/html", body: '<div id="root" style="max-width:1000px"></div>' })
    if (url.origin === "https://slides.test" && url.pathname === "/fonts/go-regular/Go-Regular.ttf") return route.fulfill({ contentType: "font/ttf", body: font })
    if (url.origin === "https://slides.test" && /^\/fonts\/[A-Za-z0-9_./-]+\.(woff2|ttf)$/.test(url.pathname) && !url.pathname.includes("..")) return route.fulfill({ contentType: url.pathname.endsWith(".woff2") ? "font/woff2" : "font/ttf", body: await readFile(join("public", url.pathname)) })
    if (url.origin === "https://slides.test" && url.pathname === "/favicon.ico") return route.fulfill({ status: 404, body: "" })
    if (url.origin === "https://slides.test" && url.pathname.startsWith("/api/v1/pages/")) {
      assert.equal(url.searchParams.get("workspace_id"), "workspace")
      assert.equal(url.searchParams.get("version"), "2")
      if (!allowed) return route.fulfill({ status: 409, contentType: "application/json", body: '{}' })
      if (url.pathname.endsWith("/artifact/slides")) return route.fulfill({ contentType: "application/json", body: JSON.stringify(projection) })
      if (url.pathname.endsWith("/artifact/export.pdf")) return route.fulfill({ contentType: "application/pdf", body: '%PDF-1.7\n%%EOF' })
    }
    external.push(url.href)
    return route.abort()
  })
  await page.goto("https://slides.test/")
  await page.addStyleTag({ content: stylesheet.css })
  await page.addStyleTag({ content: await readFile(join(directory, "preview.css"), "utf8") })
  await page.addScriptTag({ content: await readFile(bundle, "utf8") })
  const region = page.getByRole("region", { name: "Slides", exact: true })
  await region.getByText("Slide 1 / 2", { exact: true }).waitFor()
  assert.equal(await page.evaluate(() => document.fonts.check('16px "Go Regular"')), true)
  assert.ok(requests.includes("/fonts/go-regular/Go-Regular.ttf"), "the packaged font was actually loaded")
  await region.getByRole("button", { name: "text 1" }).click()
  await page.waitForFunction(() => document.querySelector('#selection').textContent.length > 0)
  assert.deepEqual(JSON.parse(await page.locator("#selection").textContent()), { path: "slides/intro.html", selector: "#title", quote: literal })
  assert.equal(await region.locator("img,iframe,script,[onerror]").count(), 0)
  assert.equal(await page.evaluate(() => window.agentExecuted), undefined)
  assert.equal(await region.getByRole("button", { name: "Slide 2", exact: true }).locator("svg").count(), 1)
  const hostBox = await page.locator("#viewer-frame").boundingBox(), thumbnailBox = await region.getByRole("button", { name: "Slide 2", exact: true }).boundingBox()
  assert.ok(thumbnailBox.y + thumbnailBox.height <= hostBox.y + hostBox.height + 1, "thumbnails remain visible inside the fixed-height viewer")
  if (artifacts) await page.screenshot({ path: join(artifacts, "slides-preview.png"), fullPage: true })
  await region.getByRole("button", { name: "Present", exact: true }).click()
  const dialog = page.getByRole("dialog", { name: "Present slides" })
  await dialog.waitFor(); await dialog.press("ArrowRight")
  await dialog.getByText("North", { exact: true }).waitFor()
  if (artifacts) await page.screenshot({ path: join(artifacts, "slides-present.png"), fullPage: true })
  await dialog.press("Escape")
  await dialog.waitFor({ state: "detached" })
  const downloading = page.waitForEvent("download")
  await region.getByRole("button", { name: "Download PDF", exact: true }).click()
  const download = await downloading
  assert.equal(download.suggestedFilename(), "slides-v2.pdf")
  const chunks = []
  for await (const chunk of await download.createReadStream()) chunks.push(chunk)
  assert.equal(Buffer.concat(chunks).toString(), '%PDF-1.7\n%%EOF')
  await download.delete()
  allowed = false
  await page.evaluate(() => window.controls.refresh())
  await region.waitFor({ state: "detached" })
  assert.equal(await page.getByLabel("Draft").inputValue(), "Keep my draft")
  if (artifacts) await page.screenshot({ path: join(artifacts, "slides-revoked.png"), fullPage: true })
  assert.deepEqual(external, [])
  process.stdout.write("Chromium Slides canvas/font/anchor/presentation/PDF transport/network/revocation passed (fixture HTTP).\n")
} finally {
  await browser?.close()
  await rm(directory, { recursive: true, force: true })
}
