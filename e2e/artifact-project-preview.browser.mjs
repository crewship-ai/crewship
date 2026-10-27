// Trusted HTML preview bridge regression. No live services or credentials.
// Run: crewship-heavy node e2e/artifact-project-preview.browser.mjs
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
const directory = await mkdtemp(join(tmpdir(), "artifact-project-preview-"))
let browser
try {
 const bundle = join(directory, "preview.js")
 await build({ entryPoints: ["lib/pages/artifact-project-preview.ts"], bundle: true, format: "iife", globalName: "ArtifactPreview", platform: "browser", outfile: bundle })
 browser = await chromium.launch({ headless: true })
 const page = await browser.newPage()
 const requests = []
 page.on("request", request => requests.push(request.url()))
 await page.setContent('<iframe title="Preview" sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>')
 await page.addScriptTag({ content: await readFile(bundle, "utf8") })
 await page.evaluate(() => {
  window.messages = []
  window.addEventListener("message", event => window.messages.push(event.data))
  window.files = [
   { path: "index.html", media_type: "text/html", content: '<h1 id="hero-heading" onclick="parent.postMessage(\'bad\',\'*\')">Overview</h1><a href="details.html">Details</a><script>parent.postMessage("bad","*")</script><iframe src="https://escape.invalid"></iframe><style>@import "https://escape.invalid/style";</style>' },
   { path: "details.html", media_type: "text/html", content: '<h1>Details</h1><a href="contact.html">Contact</a>' },
   { path: "contact.html", media_type: "text/html", content: '<h1>Contact</h1><a href="index.html">Overview</a>' },
  ]
  window.openScreen = (path, selecting = false) => { document.querySelector("iframe").srcdoc = ArtifactPreview.artifactProjectDocument(window.files, path, "trusted-token", selecting) }
  window.openScreen("index.html")
 })
 const frame = page.frameLocator("iframe")
 await frame.getByRole("heading", { name: "Overview" }).waitFor()
 assert.equal(await frame.locator("script").count(), 1)
 assert.equal(await frame.locator("iframe,[onclick],[href]").count(), 0)
 assert.equal(await frame.locator("body").evaluate(() => { try { return parent.document.title } catch (error) { return error.name } }), "SecurityError")
 await frame.getByText("Details", { exact: true }).click()
 await page.waitForFunction(() => window.messages.some(message => message.screen === "details.html"))
 await page.evaluate(() => window.openScreen("details.html"))
 await frame.getByText("Contact", { exact: true }).click()
 await page.waitForFunction(() => window.messages.some(message => message.screen === "contact.html"))
 await page.evaluate(() => window.openScreen("contact.html"))
 await frame.getByRole("heading", { name: "Contact" }).waitFor()
 await page.evaluate(() => window.openScreen("index.html", true))
 await frame.getByRole("heading", { name: "Overview" }).click()
 await page.waitForFunction(() => window.messages.some(message => message.selector === "#hero-heading" && message.quote === "Overview"))
 assert.equal(await page.evaluate(() => window.messages.includes("bad")), false)
 assert.deepEqual(requests, [])
 console.log("PASS: three linked screens, exact stable-id selection, author scripts/network blocked, opaque origin")
} finally { await browser?.close(); await rm(directory, { recursive: true, force: true }) }
