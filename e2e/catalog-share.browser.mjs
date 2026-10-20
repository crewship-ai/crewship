// Isolated production-component browser test (flag on, mocked routes), not a server acceptance.
// Run: crewship-heavy node e2e/catalog-share.browser.mjs
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const output = await mkdtemp(join(tmpdir(), "crewship-catalog-share-"))
let server, browser
try {
  await build({ entryPoints: ["e2e/catalog-share.fixture.tsx"], bundle: true,
    outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic",
    alias: { "@": process.cwd() },
    define: { "process.env": '{"NODE_ENV":"test","NEXT_PUBLIC_ARTIFACT_COPY_MOVE":"true"}' },
    plugins: [{ name: "synthetic-neighbours", setup(b) {
      b.onResolve({ filter: /(?:\/hooks\/use-auth$|\/lib\/api-fetch$|\/hooks\/use-realtime$)/ }, args => ({ path: args.path, namespace: "fixture" }))
      b.onLoad({ filter: /.*/, namespace: "fixture" }, args => ({ loader: "js", contents:
        args.path.includes("use-auth") ? 'export const useSessionSafe = () => ({data:{user:{id:"fixture-human"}},status:"authenticated"});' :
        args.path.includes("api-fetch") ? 'export const apiFetch = (u, i) => window.__fetch(u, i);' :
        'export const useRealtimeChannelSafe = () => {}; export const useRealtimeEventSafe = () => {}; export const useRealtimeEvent = () => {}; export const useRealtimeChannel = () => {}; export const useRealtimeStatusSafe = () => "disconnected";' }))
    } }],
  })
  server = createServer(async (request, response) => {
    if (request.url === "/bundle.js") { response.setHeader("Content-Type", "text/javascript"); response.end(await readFile(join(output, "bundle.js"))); return }
    response.setHeader("Content-Type", "text/html"); response.end('<meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  browser = await chromium.launch({ headless: true })
  for (const width of [1200, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, hasTouch: width < 768, isMobile: width < 768 })
    const errors = []; page.on("pageerror", e => errors.push(e.message))
    await page.goto(`http://127.0.0.1:${server.address().port}`)
    await page.getByRole("button", { name: /^Doc one/ }).waitFor()
    // filter: only stale Documents
    await page.getByRole("button", { name: /^Stale/ }).click()
    assert.equal(await page.getByRole("button", { name: /^Doc one/ }).count(), 0)
    await page.getByRole("button", { name: /^Clear filters/ }).click()
    // select two and open the dialog
    await page.getByRole("checkbox", { name: "Select Doc one" }).click()
    await page.getByRole("checkbox", { name: "Select Doc two" }).click()
    await page.getByRole("button", { name: /Move to a working folder \(2\)/ }).click()
    const dialog = page.getByRole("dialog")
    await dialog.getByRole("radio", { name: /Ops/ }).click()
    await dialog.getByText(/same ID/).waitFor()
    assert.match(await dialog.innerText(), /everyone in this workspace/)
    // no horizontal overflow at this width, and the confirm button is reachable
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `horizontal overflow at ${width}`)
    const confirm = dialog.getByRole("button", { name: "Move and share" })
    assert.ok(await confirm.isVisible())
    await confirm.click()
    const results = dialog.getByRole("list", { name: "Result for each item" })
    await results.waitFor()
    const outcomes = await results.getByRole("listitem").evaluateAll(els => els.map(e => e.getAttribute("data-outcome")))
    assert.deepEqual(outcomes, ["success", "conflict"])
    assert.equal(await page.evaluate(() => window.__moves.length), 2)
    assert.deepEqual(errors, [])
    await page.close()
  }
  console.log("catalog-share browser test passed at 1200 and 390")
} finally {
  await browser?.close(); server?.close(); await rm(output, { recursive: true, force: true })
}
