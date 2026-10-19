// Component boundary qualification only; no Page persistence/ACL API claim.
// Missing browser binaries are failures, never skips.
// crewship-heavy node e2e/static-app-preview.browser.mjs
import { build } from "esbuild"
import { chromium, firefox, webkit } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"

const output = await mkdtemp(join(tmpdir(), "crewship-static-app-"))
let server
try {
  await build({ entryPoints: ["e2e/static-app-preview.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic", alias: { "@": process.cwd() }, define: { "process.env.NODE_ENV": '"test"' } })
  const escaped = []
  server = createServer(async (request, response) => {
    if (request.url.startsWith("/escape")) escaped.push(request.url)
    response.setHeader("Content-Type", request.url === "/bundle.js" ? "text/javascript" : "text/html")
    response.end(request.url === "/bundle.js" ? await readFile(join(output, "bundle.js")) : '<div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  const failures = []
  for (const engine of [chromium, firefox, webkit]) {
    let browser
    try {
      browser = await engine.launch({ headless: true })
      const page = await browser.newPage()
      const requests = [], errors = []
      page.on("request", request => requests.push(request.url()))
      page.on("pageerror", error => errors.push(error.message))
      await page.addInitScript(() => {
        if (window !== window.top) return
        window.authorMessages = []
        window.addEventListener("message", event => window.authorMessages.push(event.data))
        window.hostSecret = "synthetic-host-credential"
      })
      const url = `http://127.0.0.1:${server.address().port}`
      await page.goto(url)
      const frame = page.frameLocator('iframe[title="App static preview"]')
      await frame.getByRole("heading", { name: "Static calculator" }).waitFor()
      assert.equal(await frame.locator("h1").evaluate(node => getComputedStyle(node).color), "rgb(1, 2, 3)")
      assert.equal(await page.locator("iframe").getAttribute("sandbox"), "")
      assert.equal(await page.locator("iframe").getAttribute("referrerpolicy"), "no-referrer")
      assert.equal(await frame.locator("script, iframe, object, embed, form, input, svg, math, video, base, link").count(), 0)
      assert.equal(await frame.locator("[href], [src]:not([alt='Local raster']), [srcset], [onerror], [onfocus], [ping]").count(), 0)
      assert.deepEqual(await frame.getByAltText("Local raster").evaluate(image => [image.naturalWidth, image.naturalHeight]), [2, 3])
      await frame.getByText("No navigation").click()
      assert.equal(page.url(), `${url}/`)
      assert.deepEqual(await page.evaluate(() => window.authorMessages), [])
      assert.equal(await frame.locator("body").evaluate(() => typeof window.authorExecuted), "undefined")
      assert.equal(await frame.locator("body").evaluate(() => typeof window.hostSecret), "undefined")
      assert.equal(await frame.locator("body").evaluate(() => {
        try { return parent.hostSecret } catch (error) { return error.name }
      }), "SecurityError")
      await page.getByRole("button", { name: "Publish second" }).click()
      await page.getByRole("button", { name: "Load version 2" }).waitFor()
      assert.equal(await frame.getByRole("heading", { name: "Static calculator" }).count(), 1)
      await page.getByRole("button", { name: "Load version 2" }).click()
      await frame.getByRole("heading", { name: "Version two" }).waitFor()
      await page.getByRole("button", { name: "Revoke access" }).click()
      await page.locator("iframe").waitFor({ state: "detached" })
      await page.getByRole("button", { name: "Replace Page" }).click()
      await page.getByRole("alert").waitFor()
      assert.equal(await page.locator("iframe").count(), 0)
      assert.equal(requests.filter(value => value.includes("/escape")).length, 0)
      assert.deepEqual(escaped, [])
      assert.deepEqual(errors, [])
      console.log(`${engine.name()}: T-AX-05 static execution/network/navigation and T-AX-04 pinned revision/access removal PASS`)
    } catch (error) {
      failures.push(error)
      console.error(`${engine.name()}: FAIL ${error.message}`)
    } finally { await browser?.close() }
  }
  if (failures.length) throw new AggregateError(failures, "Static App browser qualification failed")
} finally {
  await new Promise(resolve => server ? server.close(resolve) : resolve())
  await rm(output, { recursive: true, force: true })
}
