import { chromium } from "playwright"

const BASE = "https://crewship-dev2.unifylab.cz"
const errors = []
const browser = await chromium.launch()
const ctx = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await ctx.newPage()

page.on("console", (m) => {
  if (m.type() === "error" || m.type() === "warning") errors.push(`[${m.type()}] ${m.text()}`)
})
page.on("pageerror", (e) => errors.push(`[pageerror] ${e.message}`))

try {
  // --- login ---
  await page.goto(`${BASE}/login`, { waitUntil: "domcontentloaded", timeout: 30000 })
  await page.fill('input[type="email"]', "demo@crewship.ai").catch(() => {})
  await page.fill('input[type="password"]', "password123").catch(() => {})
  await page.click('button[type="submit"]').catch(() => {})
  await page.waitForTimeout(3500)
  console.log("after login, url:", page.url())

  // --- activity page ---
  await page.goto(`${BASE}/activity`, { waitUntil: "domcontentloaded", timeout: 30000 })
  await page.waitForTimeout(4000)
  console.log("activity url:", page.url())

  // detect main-thread block (freeze): a healthy page resolves this fast
  let frozen = false
  try {
    await Promise.race([
      page.evaluate(() => 1),
      new Promise((_, rej) => setTimeout(() => rej(new Error("timeout")), 4000)),
    ])
  } catch {
    frozen = true
  }
  console.log("main-thread responsive after load:", !frozen)

  // try clicking rail toolbar menus (Sort / Group / Filter) if present
  for (const label of ["Sort", "Group", "Filter", "Newest", "Source"]) {
    const el = page.getByText(label, { exact: false }).first()
    if (await el.isVisible().catch(() => false)) {
      await el.click().catch((e) => errors.push(`[click ${label}] ${e.message}`))
      await page.waitForTimeout(1200)
      console.log(`clicked "${label}"`)
    }
  }
  await page.waitForTimeout(1500)
} catch (e) {
  errors.push(`[script] ${e.message}`)
}

console.log("\n===== CONSOLE ERRORS/WARNINGS =====")
const seen = new Set()
for (const e of errors) {
  const key = e.slice(0, 160)
  if (seen.has(key)) continue
  seen.add(key)
  console.log(e.slice(0, 400))
}
console.log("===== total:", errors.length, "=====")
await browser.close()
