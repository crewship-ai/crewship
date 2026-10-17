/**
 * Playwright self-evaluation for the Harbor 2 /skills page. Drives the page at
 * 1440 / 1024 / 390 px in light and dark, captures screenshots, and checks the
 * controls the page has today: the Library heading, Add skill and its steps,
 * the Skills panel with Views that advertise and obey their 1–6 keys, the
 * library rows, a skill page with Give to agents, and no sideways scroll.
 *
 * Read-only: it logs in, navigates, opens and closes dialogs. It never submits
 * a form, so it is safe against a shared environment such as dev1.
 *
 * Run: SMOKE_URL=http://localhost:8081 \
 *      SMOKE_EMAIL=demo@crewship.ai SMOKE_PASSWORD=password123 \
 *      node e2e/skills-eval.mjs
 */
import pkg from "playwright"
import { mkdir } from "node:fs/promises"
import { join } from "node:path"

const { chromium } = pkg

const URL = process.env.SMOKE_URL ?? "http://localhost:3011"
const EMAIL = process.env.SMOKE_EMAIL
const PASSWORD = process.env.SMOKE_PASSWORD
if (!EMAIL || !PASSWORD) {
  console.error("skills-eval needs SMOKE_EMAIL + SMOKE_PASSWORD")
  process.exit(2)
}

const SHOTS_DIR = process.env.SHOTS_DIR ?? join("e2e", "_screenshots")
await mkdir(SHOTS_DIR, { recursive: true })

const VIEWPORTS = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "laptop", width: 1024, height: 768 },
  { name: "phone", width: 390, height: 844 },
]
const SCHEMES = ["light", "dark"]

const findings = []
const pass = []

function record(ok, msg) {
  ;(ok ? pass : findings).push(msg)
  console.log(`${ok ? "✓" : "✗"} ${msg}`)
}

async function login(page) {
  await page.goto(`${URL}/login`, { waitUntil: "domcontentloaded" })
  await page.waitForSelector('input[type="email"], input[name="email"]', { timeout: 10000 })
  await page.locator('input[type="email"], input[name="email"]').first().fill(EMAIL)
  await page.locator('input[type="password"], input[name="password"]').first().fill(PASSWORD)
  // Fail fast on login — a swallowed redirect timeout makes every
  // viewport-level check report misleading findings when the real
  // failure was that we never left /login.
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
    page.locator('button[type="submit"]').first().click(),
  ])
}

const visible = (loc) => loc.first().isVisible().catch(() => false)

async function evalViewport(browser, vp, scheme) {
  const tag = `${vp.name}-${scheme}`
  console.log(`\n=== ${tag} (${vp.width}x${vp.height}) ===`)
  const phone = vp.width < 768
  const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height }, colorScheme: scheme, isMobile: phone, hasTouch: phone })
  // Pinned panel and no appearance sync, so the run does not write preferences.
  await ctx.addInitScript(() => {
    localStorage.setItem("crewship_sidebar_mode", "pinned")
    localStorage.setItem("crewship-appearance-sync", "0")
  })
  const page = await ctx.newPage()
  page.on("pageerror", (err) => record(false, `[${tag}] page error: ${err.message}`))

  await login(page)

  await page.goto(`${URL}/skills`, { waitUntil: "domcontentloaded" })
  const heading = page.getByRole("heading", { level: 1 })
  await heading.waitFor({ timeout: 10000 }).catch(() => {})
  record((await heading.textContent().catch(() => ""))?.trim() === "Library", `[${tag}] lands on Library`)

  const shotPath = join(SHOTS_DIR, `skills-${tag}.png`)
  await page.screenshot({ path: shotPath, fullPage: true })
  record(true, `[${tag}] screenshot -> ${shotPath}`)

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  record(overflow <= 0, `[${tag}] no sideways scroll (overflow ${overflow}px)`)

  // Add skill: three steps, plus Describe it / Write it; Escape closes it.
  const addBtn = page.getByRole("button", { name: "Add skill" })
  const addVisible = await visible(addBtn)
  record(addVisible, `[${tag}] Add skill button visible`)
  if (addVisible) {
    await addBtn.first().click()
    const dialog = page.getByRole("dialog", { name: /Add skill/ })
    const open = await dialog.waitFor({ timeout: 5000 }).then(() => true, () => false)
    record(open, `[${tag}] Add skill dialog opens`)
    if (open) {
      const steps = (await dialog.getByRole("navigation", { name: "Add skill steps" }).textContent().catch(() => "")) ?? ""
      record(steps.includes("Source") && steps.includes("Who gets it"), `[${tag}] Add skill steps show Source … Who gets it`)
      const ways = (await visible(dialog.getByRole("button", { name: /Describe it/ }))) && (await visible(dialog.getByRole("button", { name: /Write it/ })))
      record(ways, `[${tag}] Describe it and Write it offered`)
      await page.screenshot({ path: join(SHOTS_DIR, `skills-${tag}-add-skill.png`), fullPage: false })
      await page.keyboard.press("Escape")
      record(await dialog.waitFor({ state: "hidden", timeout: 3000 }).then(() => true, () => false), `[${tag}] Escape closes Add skill`)
    }
  }

  // The Skills panel and its Views (pinned on desktop widths).
  if (!phone) {
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=skills]")
    const panelOn = await visible(panel)
    record(panelOn, `[${tag}] Skills panel visible`)
    if (panelOn) {
      const width = await panel.first().evaluate((el) => el.clientWidth).catch(() => 0)
      record(width >= 200, `[${tag}] panel width ${width}px (>=200)`)
      const views = panel.getByRole("navigation", { name: "Skill views" })
      const hints = await views.locator("[aria-keyshortcuts]").evaluateAll((els) => els.map((e) => e.getAttribute("aria-keyshortcuts")))
      record(hints.length >= 5 && hints.every((k, i) => k === String(i + 1)), `[${tag}] Views advertise keys ${hints.join(",")}`)
      await page.locator("body").click({ position: { x: vp.width - 5, y: vp.height - 5 } }).catch(() => {})
      await page.keyboard.press("2")
      const onAgents = await page.waitForURL(/view=used/, { timeout: 3000 }).then(() => true, () => false)
      record(onAgents, `[${tag}] key 2 switches to On agents`)
      await page.keyboard.press("1")
      await page.waitForURL((u) => !u.searchParams.has("view"), { timeout: 3000 }).catch(() => {})
    }
  }

  // Library rows (a table on desktop, rows on phone) and a skill page.
  const rows = page.locator("[data-slot=skill-row]")
  const rowCount = await rows.count().catch(() => 0)
  record(rowCount > 0, `[${tag}] library lists skills (${rowCount} rows)`)
  record((await page.locator(phone ? "[data-slot=skills-phone-rows]" : "[data-slot=skills-table]").count()) === 1, `[${tag}] ${phone ? "phone rows" : "library table"} shown`)
  if (rowCount > 0) {
    await rows.first().click()
    const overview = await page.getByRole("tab", { name: "Overview", selected: true }).waitFor({ timeout: 5000 }).then(() => true, () => false)
    record(overview, `[${tag}] a row opens the skill on Overview`)
    record(await visible(page.getByRole("button", { name: "Give to agents" })), `[${tag}] skill page offers Give to agents`)
    await page.screenshot({ path: join(SHOTS_DIR, `skills-${tag}-skill.png`), fullPage: false })
  }

  await ctx.close()
}

const browser = await chromium.launch({ headless: true })
try {
  for (const vp of VIEWPORTS) {
    for (const scheme of SCHEMES) await evalViewport(browser, vp, scheme)
  }
} finally {
  await browser.close()
}

console.log(`\n=== Summary ===`)
console.log(`pass: ${pass.length}`)
console.log(`findings: ${findings.length}`)
if (findings.length > 0) {
  console.log("\nFindings:")
  findings.forEach((f) => console.log(`  - ${f}`))
  process.exit(1)
}
