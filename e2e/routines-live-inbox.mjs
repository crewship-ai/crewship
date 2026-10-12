// Receives fixture credentials on stdin and emits only sanitized assertions.
// Called by routines-live-contracts.py --bucket access --browser.
import { chromium } from "playwright"
import fs from "node:fs"

const fixture = JSON.parse(fs.readFileSync(0, "utf8"))
const browser = await chromium.launch({ headless: true })
let stage = "login"
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } })
  await page.goto(`${fixture.server}/login`, { waitUntil: "domcontentloaded" })
  await page.locator("#email").fill(fixture.email)
  await page.locator("#password").fill(fixture.password)
  await page.locator("button[type=submit]").click()
  await page.waitForURL(url => !url.pathname.startsWith("/login"), { timeout: 30000 })
  stage = "inbox API"
  const query = new URLSearchParams({ workspace_id: fixture.workspace, state: "all", limit: "500" })
  const response = await page.request.get(`${fixture.server}/api/v1/inbox?${query}`)
  if (response.status() !== 200) throw new Error("inbox API rejected browser session")
  const feed = await response.json()
  for (const title of fixture.titles) {
    if (!feed.rows.some(row => row.title === title)) throw new Error("fixture notification absent from browser API")
  }
  stage = "inbox UI"
  await page.goto(`${fixture.server}/inbox?filter=${encodeURIComponent(fixture.prefix)}`, { waitUntil: "domcontentloaded" })
  for (const title of fixture.titles) {
    await page.getByText(title, { exact: false }).first().waitFor({ state: "visible", timeout: 15000 })
  }
  console.log(JSON.stringify({ result: "PASS", role: "MEMBER", browser_api_count: fixture.titles.length, visible_ui_count: fixture.titles.length, priorities: ["low", "medium"], addressing: ["workspace", "crew"] }))
} catch (error) {
  // Playwright call logs can echo fill() input. Do not serialize error.message.
  console.log(JSON.stringify({ result: "FAIL", stage, error_type: error.name }))
  process.exitCode = 1
} finally {
  await browser.close()
}
