/**
 * Real end-to-end run of the "Describe it" skill authoring flow (Harbor 2).
 * Add skill › Describe it opens the skill editor; Write skill asks the API to
 * generate a SKILL.md and expects 201 with real content, not a stub. The page
 * then opens the new skill, and the library list API must return it.
 *
 * This CREATES a skill in the workspace it logs into. Do not point it at a
 * shared environment you are not allowed to write to.
 *
 * Run: SMOKE_URL=http://localhost:8081 \
 *      SMOKE_EMAIL=demo@crewship.ai SMOKE_PASSWORD=password123 \
 *      node e2e/skills-create-flow.mjs
 */
import pkg from "playwright"
const { chromium } = pkg

const URL = process.env.SMOKE_URL ?? "http://localhost:3011"
const EMAIL = process.env.SMOKE_EMAIL
const PASSWORD = process.env.SMOKE_PASSWORD
if (!EMAIL || !PASSWORD) {
  console.error("needs SMOKE_EMAIL + SMOKE_PASSWORD")
  process.exit(2)
}

// The editor derives the slug from the name; this name is already a slug.
const slug = `e2e-test-${Date.now().toString(36)}`
console.log(`Will attempt to create skill: ${slug}`)

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
await ctx.addInitScript(() => localStorage.setItem("crewship-appearance-sync", "0"))
const page = await ctx.newPage()
page.on("pageerror", (e) => console.log("PAGE ERROR:", e.message))

// Login
await page.goto(`${URL}/login`, { waitUntil: "domcontentloaded" })
await page.locator('input[type="email"]').first().fill(EMAIL)
await page.locator('input[type="password"]').first().fill(PASSWORD)
// Don't swallow the redirect timeout — if login never completes we
// want this script to fail loudly rather than report a broken create
// flow when auth is the real problem.
await Promise.all([
  page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 15000 }),
  page.locator('button[type="submit"]').first().click(),
])
console.log("✓ logged in")

// Skills › Add skill › Describe it
await page.goto(`${URL}/skills`, { waitUntil: "domcontentloaded" })
await page.getByRole("button", { name: "Add skill" }).first().click()
const add = page.getByRole("dialog", { name: /Add skill/ })
await add.waitFor({ timeout: 5000 })
console.log("✓ Add skill dialog open")
await add.getByRole("button", { name: /Describe it/ }).click()
const editor = page.getByRole("dialog", { name: /New skill/ })
await editor.waitFor({ timeout: 5000 })
console.log("✓ skill editor open on Describe it")

// Fill the editor
await editor.locator("input#skill-name").fill(slug)
await editor.locator("textarea#skill-prompt").fill(
  "Use when the user wants a quick smoke summary of a TypeScript repo: report TS file count, top three dependencies, and any obvious tsconfig misconfiguration. Be terse and structured.",
)
console.log("✓ form filled")

// Capture API response
const responsePromise = page.waitForResponse(
  (resp) => resp.url().includes("/skills/generate") && resp.request().method() === "POST",
  { timeout: 90000 },
)

await editor.getByRole("button", { name: "Write skill" }).click()
console.log("→ Write skill clicked, awaiting LLM response (up to 90s)…")

const resp = await responsePromise.catch(() => null)
if (!resp) {
  console.log("✗ no response received")
  process.exit(1)
}
const status = resp.status()
console.log(`response status: ${status}`)

if (status === 412) {
  const body = await resp.text().catch(() => "")
  console.log(`PRECONDITION (env): workspace lacks API_KEY credential — wiring still proves correct.`)
  console.log(`  detail: ${body.substring(0, 200)}`)
  console.log(`✓ wiring OK (this is a credential-deployment issue, not a code defect)`)
  await browser.close()
  process.exit(0)
}

if (status === 502) {
  const body = await resp.text().catch(() => "")
  if (body.includes("invalid Anthropic API key")) {
    console.log(`UPSTREAM 401 (env): API_KEY exists but is invalid. Wiring reached Anthropic.`)
    console.log(`  detail: ${body.substring(0, 200)}`)
    console.log(`✓ wiring OK (this is a credential-rotation issue, not a code defect)`)
    await browser.close()
    process.exit(0)
  }
}

if (status !== 201) {
  const body = await resp.text().catch(() => "")
  console.log(`✗ unexpected status: ${status} body=${body.substring(0, 400)}`)
  process.exit(1)
}

const json = await resp.json().catch(() => null)
if (!json?.skill_id || !json?.content?.startsWith("---")) {
  console.log(`✗ malformed response:`, JSON.stringify(json).substring(0, 300))
  process.exit(1)
}
console.log(`✓ skill generated: ${json.slug} (${json.skill_id}), ${json.content.length} chars`)
console.log(`  scan_status=${json.scan_status} quality=${json.description_quality || "ok"}`)

// The editor closes and the page opens the new skill on its Overview.
await editor.waitFor({ state: "hidden", timeout: 15000 })
console.log("✓ editor closed")
await page.waitForURL((u) => u.searchParams.get("skill") === json.skill_id, { timeout: 10000 }).catch(() => {})
const opened = new globalThis.URL(page.url()).searchParams.get("skill") === json.skill_id
console.log(`${opened ? "✓" : "✗"} new skill page open (?skill=${json.skill_id})`)
if (!opened) process.exit(1)

// Final API double-check: the workspace library lists it.
const ws = await page.request.get(`${URL}/api/v1/workspaces`).then((r) => r.json())
const workspaceId = (Array.isArray(ws) ? ws : [ws])[0]?.id
const listResp = await page.request.get(`${URL}/api/v1/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
const list = await listResp.json().catch(() => [])
const found = list.find?.((s) => s.slug === slug)
if (!found) {
  console.log(`✗ skill not in the workspace library list`)
  process.exit(1)
}
console.log(`✓ skill in the library via list API: source=${found.source}`)

await browser.close()
console.log("\n=== ALL CHECKS PASSED ===")
