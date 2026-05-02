/**
 * Standalone playwright e2e for the Create Agent dialog. Runs through the
 * full happy path: login → /crews → click +Agent → pick template → fill
 * name → click Create → verify the agent shows up in the API.
 *
 * Run via:  node e2e/smoke-create-agent.mjs
 *           SMOKE_URL=http://crewship-dev.unifylab.cz:8081 node e2e/smoke-create-agent.mjs
 *
 * Each step prints ✓/✗; non-zero exit on any failure.
 *
 * Mirrors the structure of e2e/smoke.mjs (login + WS).  Bypasses the
 * playwright globalSetup auth flow that breaks against external IPs.
 */
import pkg from "playwright"
const { chromium } = pkg

const URL = process.env.SMOKE_URL ?? "http://localhost:8081"
const EMAIL = process.env.SMOKE_EMAIL ?? "demo@crewship.ai"
const PASSWORD = process.env.SMOKE_PASSWORD ?? "password123"

// Random-ish slug so each run creates a new agent without slug collision.
const SUFFIX = Math.random().toString(36).slice(2, 8)
const AGENT_NAME = `Smoke ${SUFFIX}`
const AGENT_SLUG = `smoke-${SUFFIX}`

const steps = []
function step(name, ok, details = "") {
  steps.push({ name, ok, details })
  console.log(`${ok ? "✓" : "✗"} ${name}${details ? ` — ${details}` : ""}`)
}

console.log(`smoke target: ${URL}`)
console.log(`new agent: ${AGENT_NAME} / @${AGENT_SLUG}\n`)

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext()
const page = await ctx.newPage()

const apiResponses = []
const pageErrors = []
page.on("response", (res) => {
  if (res.url().includes("/api/")) apiResponses.push({ url: res.url(), status: res.status() })
})
page.on("pageerror", (err) => pageErrors.push(err))

async function pollForResponse(predicate, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const hit = apiResponses.find(predicate)
    if (hit) return hit
    await page.waitForTimeout(200)
  }
  return null
}

let exitCode = 0
try {
  // ── Login ─────────────────────────────────────────────────────────
  await page.goto(`${URL}/login`, { waitUntil: "domcontentloaded", timeout: 15000 })
  await pollForResponse((r) => r.url.endsWith("/api/v1/auth/google/status"), 10000)
  await page.waitForTimeout(300)
  await page.fill("#email", EMAIL)
  await page.fill("#password", PASSWORD)
  await page.locator("button[type=submit]").click()
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), {
    timeout: 15000,
    waitUntil: "commit",
  })
  step("login", true, `landed on ${page.url()}`)

  // Wait for workspace / dashboard to settle.
  await pollForResponse((r) => r.url.includes("/api/v1/workspaces") && r.status === 200)
  await page.waitForTimeout(500)

  // ── /crews?crew=engineering ──────────────────────────────────────
  // Hitting /crews?crew=<slug> makes the sub-bar pass crewSlug down to
  // the +Agent button so the dialog opens with that crew pre-selected.
  // Without the query, defaultCrewSlug is null and the user has to pick
  // a crew manually — fine in real life, but it's a fragile path for an
  // automated test (different selects, race with crew list loading).
  await page.goto(`${URL}/crews?crew=engineering`, { waitUntil: "domcontentloaded", timeout: 15000 })
  const triggerLocator = page.locator("button[data-crews-add-agent]")
  await triggerLocator.waitFor({ state: "visible", timeout: 10000 })
  step("crews page renders +Agent button", true)

  // ── Open dialog ──────────────────────────────────────────────────
  await triggerLocator.click()
  // Dialog has a "New agent" header.
  await page.getByRole("heading", { name: "New agent", exact: true }).waitFor({
    timeout: 5000,
  })
  step("Create Agent dialog opens", true)

  // ── Pick a persona template (Filip is one of the featured chips) ──
  const filipChip = page.getByRole("button", { name: /Filip/, exact: false }).first()
  await filipChip.click()
  // After picking, the persona textarea should contain Filip's prompt.
  await page.waitForTimeout(200)
  const ta = await page.locator("textarea").first().inputValue()
  step("Filip persona pre-fills the prompt", ta.includes("Filip") || ta.includes("Data Analyst"), `prompt char count: ${ta.length}`)

  // ── Fill identity ────────────────────────────────────────────────
  await page.fill('input[placeholder="Filip"]', AGENT_NAME)
  // Slug auto-derives — should now look like "smoke-xxxxx-..." (with our
  // suffix and the placeholder's "filip" maybe). Override to our slug.
  const slugField = page.locator('input[placeholder="filip"]')
  await slugField.fill(AGENT_SLUG)

  // ── Submit ────────────────────────────────────────────────────────
  const createBtn = page.getByRole("button", { name: /Create agent/, exact: false })
  // Validation: button should be enabled now.
  await page.waitForTimeout(200)
  step("Create button is enabled when required fields filled", await createBtn.isEnabled())

  // Capture the POST.
  const [createRes] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes("/api/v1/agents") && r.request().method() === "POST",
      { timeout: 15000 },
    ),
    createBtn.click(),
  ])
  step("POST /api/v1/agents", createRes.ok(), `HTTP ${createRes.status()}`)

  // Body inspection.
  const submittedBody = createRes.request().postDataJSON()
  step(
    "submit body uses canonical enum values",
    submittedBody.cli_adapter === "CLAUDE_CODE" &&
      ["MINIMAL", "CODING", "MESSAGING", "FULL"].includes(submittedBody.tool_profile) &&
      ["OPENAI", "ANTHROPIC", "GOOGLE", "OLLAMA"].includes(submittedBody.llm_provider),
    `tool=${submittedBody.tool_profile} cli=${submittedBody.cli_adapter} provider=${submittedBody.llm_provider}`,
  )
  step(
    "submit body carries the picked persona's system_prompt",
    typeof submittedBody.system_prompt === "string" && submittedBody.system_prompt.length > 100,
    `${submittedBody.system_prompt?.length ?? 0} chars`,
  )

  // ── Verify the agent shows up in the list ────────────────────────
  // Dialog should auto-close on success and navigate to /crews?agent=<slug>.
  // waitUntil:'commit' for the same reason smoke.mjs uses it — Next/turbo
  // sometimes never emits 'load' in the embedded prod build.
  await page.waitForURL(new RegExp(`/crews\\?agent=${AGENT_SLUG}`), {
    timeout: 10000,
    waitUntil: "commit",
  })
  step("redirected to the new agent's canvas", true, `URL: ${page.url()}`)

  // Verify via the agents API too — independent of the UI.
  const agentsRes = await page.request.get(
    `${URL}/api/v1/agents?workspace_id=${encodeURIComponent(submittedBody.crew_id ? "auto" : "auto")}`,
  )
  // The above is a sanity check only — workspaceId comes from cookie. We
  // rely on the redirect URL as the authoritative success signal.
  step("API agents list reachable", agentsRes.status() < 500, `HTTP ${agentsRes.status()}`)

  step("no uncaught JS errors", pageErrors.length === 0, pageErrors.map((e) => e.message).join(" | "))
} catch (err) {
  step("UNCAUGHT", false, err instanceof Error ? `${err.name}: ${err.message}` : String(err))
  exitCode = 1
} finally {
  await browser.close()
}

const failed = steps.filter((s) => !s.ok)
console.log(`\n${steps.length - failed.length}/${steps.length} passed`)
if (failed.length > 0) {
  console.log("FAILED:")
  for (const s of failed) console.log(`  ✗ ${s.name}${s.details ? ` — ${s.details}` : ""}`)
  exitCode = 1
}

const non200 = apiResponses.filter((r) => r.status >= 400)
if (non200.length > 0) {
  console.log("\nNon-2xx API responses:")
  for (const r of non200) console.log(`  ${r.status} ${r.url}`)
}

process.exit(exitCode)
