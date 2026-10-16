// Isolated component acceptance: no shared server, credentials or production data.
// crewship-heavy node e2e/proposal-card.browser.mjs
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { createServer } from "node:http"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
const output = await mkdtemp(join(tmpdir(), "crewship-proposal-browser-"))
let browser
let server
try {
  await build({ entryPoints: ["e2e/proposal-card.fixture.tsx"], bundle: true, outfile: join(output, "bundle.js"), platform: "browser", format: "esm", jsx: "automatic", alias: { "@": process.cwd() }, define: { "process.env.NODE_ENV": '"test"' } })
  server = createServer(async (request, response) => {
    if (request.url === "/bundle.js") { response.setHeader("Content-Type", "text/javascript"); response.end(await readFile(join(output, "bundle.js"))); return }
    response.setHeader("Content-Type", "text/html"); response.end('<meta name="viewport" content="width=device-width,initial-scale=1"><div id="root"></div><script type="module" src="/bundle.js"></script>')
  })
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve))
  browser = await chromium.launch({ headless: true })
  const fixture = JSON.parse(await readFile("internal/api/testdata/chat-e2/proposal-draft.json", "utf8"))
  for (const width of [1200, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 844 }, isMobile: width < 768, hasTouch: width < 768 })
    page.on("pageerror", error => console.error(error))
    let status = 500
    const writes = []
    await page.route("**/operation", async route => {
      const request = route.request().postDataJSON(); writes.push(request)
      await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(status === 200 ? fixture : {}) })
    })
    await page.goto(`http://127.0.0.1:${server.address().port}`)
    const title = page.getByLabel("Issue 1 title", { exact: true })
    await title.fill("Human draft")
    await page.getByLabel("Comment to agent").fill("Separate comment")
    // Browser keyboard tab order reaches editable controls on both widths.
    await title.focus(); await page.keyboard.press("Tab")
    assert.equal(await page.getByLabel("Issue 1 description").evaluate(node => node === document.activeElement), true)
    await page.getByRole("button", { name: "Simulate agent version" }).click()
    assert.equal(await title.inputValue(), "Human draft")
    await page.reload()
    assert.equal(await title.inputValue(), "Human draft")
    assert.equal(await page.getByLabel("Comment to agent").inputValue(), "Separate comment")
    await page.getByRole("button", { name: "Save proposal edits" }).click()
    await page.getByRole("button", { name: "Retry identical operation" }).waitFor()
    await page.reload()
    status = 200
    await page.getByRole("button", { name: "Retry identical operation" }).click()
    await page.getByRole("button", { name: "Save proposal edits" }).waitFor({ state: "visible" })
    assert.deepEqual(writes[1], writes[0])
    assert.equal(await page.getByLabel("Comment to agent").inputValue(), "Separate comment")
    status = 403
    await page.getByRole("button", { name: "Discard proposal" }).click()
    await page.getByText("Investigate dashboard latency", { exact: true }).waitFor({ state: "hidden" })
    assert.equal(await page.evaluate(() => sessionStorage.length), 0)
    await page.close()
    console.log(`proposal browser: ${width}px keyboard/drafts/reload/retry/access-loss passed`)
  }
} finally {
  await browser?.close()
  await new Promise(resolve => server ? server.close(resolve) : resolve())
  await rm(output, { recursive: true, force: true })
}
