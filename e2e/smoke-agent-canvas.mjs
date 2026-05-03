import pkg from "playwright"
const { chromium } = pkg

const URL = process.env.SMOKE_URL ?? "http://localhost:3011"
const EMAIL = process.env.SMOKE_EMAIL ?? "demo@crewship.ai"
const PASSWORD = process.env.SMOKE_PASSWORD ?? "password123"
const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext()
const page = await ctx.newPage()

const apiResponses = []
const wsConnections = []
const errors = []

page.on("response", (res) => { if (res.url().includes("/api/")) apiResponses.push({ url: res.url(), status: res.status() }) })
page.on("websocket", (ws) => { if (!ws.url().includes("/_next/")) wsConnections.push({ url: ws.url(), frames: 0 }) })
page.on("pageerror", (err) => errors.push(err))

// 1. Login
console.log("[1] login")
await page.goto(`${URL}/login`, { waitUntil: "domcontentloaded", timeout: 15000 })
await page.waitForResponse((r) => r.url().endsWith("/api/v1/auth/google/status"), { timeout: 10000 }).catch(() => {})
await page.waitForTimeout(300)
await page.fill("#email", EMAIL)
await page.fill("#password", PASSWORD)
await page.locator("button[type=submit]").click()
await page.waitForURL((url) => !url.pathname.startsWith("/login"), { timeout: 15000, waitUntil: "commit" })
await page.waitForTimeout(2000)

// 2. Navigate to /crews?crew=research&agent=filip
console.log("[2] goto /crews?crew=research&agent=filip")
const t0 = Date.now()
await page.goto(`${URL}/crews?crew=research&agent=filip`, { waitUntil: "domcontentloaded", timeout: 30000 })
console.log(`   navigation: ${Date.now() - t0}ms`)
await page.waitForTimeout(3000)

// 3. Inspect what's actually visible
console.log("\n[3] DOM state after 3s settle:")
const main = await page.locator("main, [role=main], body > div").first().innerHTML().catch(() => "(no main)")
const visibleText = await page.evaluate(() => document.body.innerText.slice(0, 500))
console.log(`   visible text (first 500 chars):\n   ${visibleText.split("\n").join("\n   ")}`)

const skeletonCount = await page.locator("[data-slot=skeleton], .animate-pulse").count()
const buttonCount = await page.locator("button").count()
const linkCount = await page.locator("a").count()
console.log(`   skeleton/loading: ${skeletonCount}`)
console.log(`   buttons: ${buttonCount}`)
console.log(`   links: ${linkCount}`)

// 4. Wait longer to see if it eventually renders
console.log("\n[4] wait 10s more, recheck:")
await page.waitForTimeout(10000)
const skeletonCount2 = await page.locator("[data-slot=skeleton], .animate-pulse").count()
const buttonCount2 = await page.locator("button").count()
const visibleText2 = await page.evaluate(() => document.body.innerText.slice(0, 500))
console.log(`   skeleton: ${skeletonCount2}, buttons: ${buttonCount2}`)
console.log(`   visible text:\n   ${visibleText2.split("\n").join("\n   ")}`)

// 5. Network summary
console.log(`\n[5] Network: ${apiResponses.length} API responses, ${wsConnections.length} WS`)
let failed = false
const non200 = apiResponses.filter((r) => r.status >= 400)
if (non200.length > 0) {
  console.log("   FAILED:")
  for (const r of non200) console.log(`   ${r.status} ${r.url}`)
  failed = true
}
const pendingFetch = await page.evaluate(() => {
  // @ts-ignore
  return performance.getEntriesByType("resource")
    .filter((e) => e.responseEnd === 0 && e.name.includes("/api/"))
    .map((e) => e.name)
})
if (pendingFetch.length > 0) {
  console.log("   PENDING fetches:")
  for (const u of pendingFetch) console.log(`   ${u}`)
}

console.log(`\n[6] JS errors: ${errors.length}`)
for (const e of errors) console.log(`   ${e.name}: ${e.message}`)
if (errors.length > 0) failed = true

await browser.close()

if (failed) {
  console.error("\nSmoke test detected API/JS failures")
  process.exit(1)
}
