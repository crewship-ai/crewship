/**
 * Playwright self-evaluation for /skills. Drives the page at four
 * viewports, captures full-page screenshots, asserts the things the
 * user complained about: Create Skill button works, panels are at
 * sensible widths, detail populates on click.
 *
 * Run: SMOKE_URL=http://crewship.example.com:8081 \
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

const SHOTS_DIR = join("e2e", "_screenshots")
await mkdir(SHOTS_DIR, { recursive: true })

const VIEWPORTS = [
  { name: "desktop-xl", width: 1920, height: 1080 },
  { name: "desktop", width: 1280, height: 800 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 375, height: 812 },
]

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
  // viewport-level check report misleading "no Create button" errors
  // when the real failure was that we never left /login.
  await Promise.all([
    page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
    page.locator('button[type="submit"]').first().click(),
  ])
}

async function evalViewport(browser, vp) {
  console.log(`\n=== ${vp.name} (${vp.width}x${vp.height}) ===`)
  const ctx = await browser.newContext({ viewport: { width: vp.width, height: vp.height } })
  const page = await ctx.newPage()
  page.on("pageerror", (err) => record(false, `[${vp.name}] page error: ${err.message}`))

  await login(page)

  await page.goto(`${URL}/skills`, { waitUntil: "domcontentloaded" })
  // Fail the run if the page never reached its first paint marker —
  // otherwise we'd carry on screenshotting a half-loaded shell.
  await page.waitForSelector('text=/Browse|Create Skill|Skills/', { timeout: 10000 })
  await page.waitForTimeout(2000)

  const shotPath = join(SHOTS_DIR, `skills-${vp.name}.png`)
  await page.screenshot({ path: shotPath, fullPage: true })
  record(true, `[${vp.name}] screenshot -> ${shotPath}`)

  const createBtnVisible = await page.locator('button:has-text("Create Skill")').first().isVisible().catch(() => false)
  record(createBtnVisible, `[${vp.name}] Create Skill button visible`)

  if (createBtnVisible && vp.width >= 768) {
    await page.locator('button:has-text("Create Skill")').first().click().catch(() => {})
    await page.waitForTimeout(700)
    const dialogOpen = await page.locator('[role="dialog"]').first().isVisible().catch(() => false)
    record(dialogOpen, `[${vp.name}] Create Skill dialog opens`)
    if (dialogOpen) {
      const slugVisible = await page.locator('input#skill-slug').isVisible().catch(() => false)
      const promptVisible = await page.locator('textarea#skill-prompt').isVisible().catch(() => false)
      record(slugVisible && promptVisible, `[${vp.name}] Create dialog has slug + prompt fields`)
      await page.screenshot({ path: join(SHOTS_DIR, `skills-${vp.name}-create-dialog.png`), fullPage: false })
      await page.keyboard.press("Escape").catch(() => {})
      await page.waitForTimeout(300)
    }
  }

  const cardSelector = 'button[aria-label*="anthropic/"], button[aria-label*="community/"]'
  const cardCount = await page.locator(cardSelector).count().catch(() => 0)
  record(cardCount > 0, `[${vp.name}] grid renders skill cards (${cardCount} found)`)
  if (cardCount > 0 && vp.width >= 1024) {
    await page.locator(cardSelector).first().click().catch(() => {})
    await page.waitForTimeout(800)
    const installBtn = await page.locator('button:has-text("Install to agent")').first().isVisible().catch(() => false)
    record(installBtn, `[${vp.name}] detail panel populates (Install button visible)`)
    await page.screenshot({ path: join(SHOTS_DIR, `skills-${vp.name}-detail-open.png`), fullPage: false })
  }

  if (vp.width >= 1024) {
    const railWidth = await page.locator('[data-panel-id="skills-rail"]').first().evaluate((el) => el.clientWidth).catch(() => 0)
    const detailWidth = await page.locator('[data-panel-id="skills-detail"]').first().evaluate((el) => el.clientWidth).catch(() => 0)
    record(railWidth >= 200, `[${vp.name}] rail width ${railWidth}px (>=200)`)
    record(detailWidth >= 280, `[${vp.name}] detail width ${detailWidth}px (>=280)`)
  }

  await ctx.close()
}

const browser = await chromium.launch({ headless: true })
try {
  for (const vp of VIEWPORTS) {
    await evalViewport(browser, vp)
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
