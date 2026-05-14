import { test, expect } from "@playwright/test"

/**
 * Onboarding wizard E2E — first-run journey from empty DB to a
 * deployed crew. Replaces the standalone `e2e/onboarding-fresh.mjs`
 * script with a proper test-runner suite that slots into the
 * e2e-devcontainer nightly workflow.
 *
 * Preconditions
 * ─────────────
 *   - Server has NEVER been bootstrapped (needs_bootstrap=true).
 *     The suite skips itself with a clear message otherwise; in CI
 *     the workflow asserts this before Playwright even starts.
 *   - Server env CREWSHIP_E2E_SKIP_TOKEN_PROBE=1, so the Launch step
 *     accepts a fake Claude Code CLI token instead of live-calling
 *     api.anthropic.com.
 *
 * Bootstrap is a one-shot (POST /api/v1/bootstrap returns 403 after
 * the first user), so the whole describe block runs serially with the
 * validation tests first — they fail submission so they don't consume
 * the shot.
 */

const SETUP_STATUS_PATH = "/api/v1/system/setup-status"

// Token only needs to be syntactically plausible; the server-side
// probe is gated off in CI. Don't expose this as an env var — a real
// token here would hit api.anthropic.com whenever the gate isn't set.
const FAKE_API_KEY = "sk-ant-oat-e2e-fake-token"

test.use({ storageState: { cookies: [], origins: [] } })
test.describe.configure({ mode: "serial" })

test.describe("onboarding wizard — first-run flow", () => {
  // Per-run identifier kept inside the describe so it's evaluated when
  // Playwright reaches this block, not at module load. Keeps email
  // collisions out of cross-suite scenarios that load this file.
  const runId = String(Date.now())
  const fullName = process.env.BOOTSTRAP_NAME ?? "QA Tester"
  const email = process.env.BOOTSTRAP_EMAIL ?? `qa-${runId}@example.com`
  const password = process.env.BOOTSTRAP_PASSWORD ?? "playwright-onboarding-pw"

  let suiteSkipped = false

  test.beforeAll(async ({ request }) => {
    const res = await request.get(SETUP_STATUS_PATH)
    if (res.status() !== 200) return // let the first test fail loudly with the real reason
    const body = await res.json().catch(() => ({}))
    if (body?.needs_bootstrap !== true) {
      suiteSkipped = true
      console.log(
        `[onboarding-wizard] skipping: needs_bootstrap=${body?.needs_bootstrap}. ` +
          `Point Playwright at a fresh instance.`,
      )
    }
  })

  test.beforeEach(async () => {
    test.skip(suiteSkipped, "instance already bootstrapped")
  })

  // ── Validation — don't submit successfully, don't burn the shot ──

  test("/login redirects anonymous visitor to /bootstrap on empty DB", async ({ page }) => {
    await page.goto("/login")
    await page.waitForURL(/\/bootstrap/, { timeout: 10_000 })
    expect(page.url()).toContain("/bootstrap")
  })

  test("bootstrap form rejects short name", async ({ page }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", "A")
    await page.fill("#email", `pre-${email}`)
    await page.fill("#password", "long-enough-pw")
    await page.click("button[type=submit]")
    await expect(page.getByRole("alert")).toContainText(/at least 2 characters/i)
    expect(page.url()).toContain("/bootstrap")
  })

  test("bootstrap form rejects short password", async ({ page }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", fullName)
    await page.fill("#email", `pre-${email}`)
    await page.fill("#password", "short")
    await page.click("button[type=submit]")
    await expect(page.getByRole("alert")).toContainText(/at least 8 characters/i)
    expect(page.url()).toContain("/bootstrap")
  })

  // ── Happy path — single test because the wizard is single-page
  // (step state in React useState; no per-step URL to split on). ──

  test("bootstrap → wizard (3 steps) → launch → DB rows present", async ({ page, request }) => {
    test.setTimeout(90_000)

    // Bootstrap form
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await expect(page.getByText(/initial setup/i)).toBeVisible()
    await page.fill("#full_name", fullName)
    await page.fill("#email", email)
    await page.fill("#password", password)
    await page.click("button[type=submit]")
    await page.waitForURL(/\/onboarding/, { timeout: 20_000 })

    // Mid-wizard reload — wizard state is in-memory, not localStorage.
    // Pin that contract so a future persistence PR notices.
    await page.waitForSelector("#workspace_name", { timeout: 20_000 })
    expect((await page.inputValue("#workspace_name")).length).toBeGreaterThanOrEqual(2)
    await page.reload({ waitUntil: "networkidle" })
    await page.waitForSelector("#workspace_name", { timeout: 20_000 })
    expect((await page.inputValue("#workspace_name")).length).toBeGreaterThanOrEqual(2)

    // Step 1: workspace
    await expect(page.locator('button[aria-label="Pick a language"]')).toHaveCount(1)
    await expect(page.getByRole("button", { name: /continue/i })).toBeEnabled()
    await page.getByRole("button", { name: /continue/i }).click()

    // Step 2: pick crew template. AnimatePresence mounts only the
    // active step, so visible aria-pressed buttons are all crew
    // cards — assert the exact count so adding *or* removing a
    // template trips the test.
    await page.waitForSelector("button[aria-pressed]", { timeout: 10_000 })
    await expect(page.locator("button[aria-pressed]")).toHaveCount(5)
    await page.getByRole("button", { name: /software development/i }).click()
    await page.waitForSelector('img[width="32"]', { timeout: 10_000 })
    expect(await page.locator('img[width="32"]').count()).toBe(4)
    await expect(page.getByRole("button", { name: /continue/i })).toBeEnabled()
    await page.getByRole("button", { name: /continue/i }).click()

    // Step 3: switch to browser mode so Launch gates on the API key
    // field instead of the pair countdown (which never completes in CI).
    await page.waitForSelector('button:has-text("Pair my CLI")', { timeout: 10_000 })
    await page.getByRole("button", { name: /chat in browser/i }).click()
    // Wait for the pair snippet to actually leave the DOM rather than
    // sleeping for a magic motion duration.
    await expect(page.locator('code:has-text("crewship login --pair")')).toBeHidden()
    await page.fill("#api_key", FAKE_API_KEY)

    const launch = page.getByRole("button", { name: /launch/i })
    await expect(launch).toBeEnabled()

    const setupRespPromise = page.waitForResponse(
      (r) => r.url().includes("/api/v1/onboarding/setup") && r.request().method() === "POST",
      { timeout: 30_000 },
    )
    await launch.click()
    expect((await setupRespPromise).status()).toBe(201)

    await page.waitForURL(/\/crews\/agents\//, { timeout: 15_000 })

    // DB-state assertions
    const statusAfter = await (await request.get(SETUP_STATUS_PATH)).json()
    expect(statusAfter.needs_bootstrap).toBe(false)

    const wsRes = await page.request.get("/api/v1/workspaces")
    expect(wsRes.status()).toBe(200)
    const workspaces = await wsRes.json()
    expect(Array.isArray(workspaces)).toBe(true)
    expect(workspaces.length).toBeGreaterThan(0)
    expect(typeof workspaces[0]?.preferred_language).toBe("string")
    expect(workspaces[0].preferred_language.length).toBeGreaterThan(0)
  })

  // ── Post-bootstrap guards — depend on happy-path having run ──

  test("/bootstrap redirects to /login once the DB is initialised", async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const page = await ctx.newPage()
    try {
      await page.goto("/bootstrap")
      await page.waitForURL(/\/login(\?|$)/, { timeout: 10_000 })
      expect(page.url()).toContain("/login")
    } finally {
      await ctx.close()
    }
  })

  test("POST /api/v1/bootstrap returns 403 after first user exists", async ({ request }) => {
    const res = await request.post("/api/v1/bootstrap", {
      data: {
        full_name: "Second Admin",
        email: `second-${runId}@example.com`,
        password: "another-pw-1234",
      },
      headers: { "Content-Type": "application/json" },
    })
    expect(res.status()).toBe(403)
  })
})
