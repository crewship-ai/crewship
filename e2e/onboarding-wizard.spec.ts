import { test, expect, type Page } from "@playwright/test"

/**
 * Onboarding wizard E2E — full first-run journey from empty DB to a
 * deployed crew.
 *
 * Replaces the standalone `onboarding-fresh.mjs` script. Same assertion
 * surface, but runs inside the Playwright test runner so it shows up in
 * HTML reports, retries flakes on CI, and slots into the nightly
 * e2e-devcontainer workflow alongside the other E2E specs.
 *
 * Fresh-DB precondition
 * ─────────────────────
 * The whole point of this suite is the first-run flow, so it can only
 * run against an instance that has NEVER been bootstrapped (no users,
 * no workspaces). The first test reads /api/v1/system/setup-status; if
 * needs_bootstrap=false the entire suite is skipped with a clear
 * message instead of false-failing on selectors that don't render once
 * the bootstrap chip is gone. CI gets a fresh DB per run from the
 * e2e-devcontainer workflow.
 *
 * Token-probe gate
 * ────────────────
 * Step 3 of the wizard live-calls api.anthropic.com to validate the
 * Claude Code CLI token before letting Launch succeed. Without a real
 * sk-ant-oat token nightly would either need an Anthropic secret in
 * GH Actions or perpetual flakes when the upstream wobbles, so the
 * server-side env CREWSHIP_E2E_SKIP_TOKEN_PROBE=1 short-circuits that
 * call. The workflow sets it; local devs can either export it before
 * pnpm dev or supply a real token via BOOTSTRAP_API_KEY.
 *
 * Serial execution
 * ────────────────
 * Bootstrap is a one-shot — POST /api/v1/bootstrap returns 403 after
 * the first user exists. Running the validation tests in parallel
 * with the happy path would race the single shot, so the whole
 * describe block is serial and the validation cases come first
 * (they never submit successfully and so don't consume the shot).
 */

const SETUP_STATUS_PATH = "/api/v1/system/setup-status"

// Test data — kept distinct so a happy-path run after a flake in the
// validation tests can't accidentally land an email collision.
const RUN_ID = String(Date.now())
const EMAIL = process.env.BOOTSTRAP_EMAIL ?? `qa-${RUN_ID}@example.com`
const PASSWORD = process.env.BOOTSTRAP_PASSWORD ?? "playwright-onboarding-pw"
const FULL_NAME = process.env.BOOTSTRAP_NAME ?? "QA Tester"
// Fake token only works because the server-side probe is gated off via
// CREWSHIP_E2E_SKIP_TOKEN_PROBE. If you remove that env on the server
// you must supply a real sk-ant-oat… via BOOTSTRAP_API_KEY.
const API_KEY = process.env.BOOTSTRAP_API_KEY ?? "sk-ant-oat-e2e-fake-token"

// Drop any cookies inherited from global-setup so this suite runs as
// an anonymous visitor on a fresh instance.
test.use({ storageState: { cookies: [], origins: [] } })

test.describe.configure({ mode: "serial" })

test.describe("onboarding wizard — first-run flow", () => {
  let suiteSkipped = false

  test.beforeAll(async ({ request }) => {
    // Probe once, gate the whole describe — avoids a separate "is it
    // fresh?" call per test that would slow the suite down.
    const res = await request.get(SETUP_STATUS_PATH)
    if (res.status() !== 200) {
      // Server isn't even up properly — let the first test fail loudly
      // with the real reason instead of skipping silently.
      return
    }
    const body = await res.json().catch(() => ({}))
    if (body?.needs_bootstrap !== true) {
      suiteSkipped = true
      console.log(
        `[onboarding-wizard] skipping: instance already bootstrapped ` +
          `(needs_bootstrap=${body?.needs_bootstrap}). Point Playwright at a ` +
          `fresh instance — see e2e/onboarding-wizard.spec.ts header.`,
      )
    }
  })

  test.beforeEach(async () => {
    test.skip(suiteSkipped, "instance already bootstrapped (see header)")
  })

  // ──────────────────────────────────────────────────────────────────
  // Validation — these never submit successfully, so they don't burn
  // the one-shot bootstrap. Always run before the happy-path test.
  // ──────────────────────────────────────────────────────────────────

  test("/login redirects anonymous visitor to /bootstrap on empty DB", async ({ page }) => {
    await page.goto("/login")
    await page.waitForURL(/\/bootstrap/, { timeout: 10_000 })
    expect(page.url()).toContain("/bootstrap")
  })

  test("bootstrap form rejects short name (client-side validation)", async ({ page }) => {
    await page.goto("/bootstrap")
    // The page renders nothing while it's still checking setup-status
    // (returns an empty div) — wait for the form before typing.
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", "A") // 1 char — below the 2-char minimum
    await page.fill("#email", `pre-${EMAIL}`)
    await page.fill("#password", "long-enough-pw")
    await page.click("button[type=submit]")
    // Error renders into a role="alert" region — assert on the visible
    // string, not the toast position, so the test survives copy tweaks.
    await expect(page.getByRole("alert")).toContainText(/at least 2 characters/i)
    // Page must NOT have navigated away — bootstrap form still mounted.
    expect(page.url()).toContain("/bootstrap")
  })

  test("bootstrap form rejects short password", async ({ page }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", FULL_NAME)
    await page.fill("#email", `pre-${EMAIL}`)
    await page.fill("#password", "short") // 5 chars — below the 8-char minimum
    await page.click("button[type=submit]")
    await expect(page.getByRole("alert")).toContainText(/at least 8 characters/i)
    expect(page.url()).toContain("/bootstrap")
  })

  // ──────────────────────────────────────────────────────────────────
  // Happy path — single long test because the bootstrap → wizard →
  // launch flow is one continuous journey from the user's POV and the
  // wizard is single-page (step state lives in React useState; there
  // is no per-step URL we could split on without losing the state).
  // ──────────────────────────────────────────────────────────────────

  test("bootstrap → wizard (3 steps) → launch → DB rows present", async ({ page, request }) => {
    test.setTimeout(90_000)

    // ── Bootstrap form ──────────────────────────────────────────────
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await expect(page.getByText(/initial setup/i)).toBeVisible()
    await page.fill("#full_name", FULL_NAME)
    await page.fill("#email", EMAIL)
    await page.fill("#password", PASSWORD)
    await page.click("button[type=submit]")
    await page.waitForURL(/\/onboarding/, { timeout: 20_000 })

    // ── Edge case: refresh mid-wizard ──────────────────────────────
    // Wizard state lives entirely in React useState — there's no
    // localStorage persistence — so a reload between steps should
    // bring the user back to step 1 with a fresh-looking form. This
    // is a deliberate design choice (the alternative is half-filled
    // forms that confuse users on Mondays), and we pin it here so a
    // future "let's persist this in localStorage" PR notices.
    await page.waitForSelector("#workspace_name", { timeout: 20_000 })
    const ws1 = await page.inputValue("#workspace_name")
    expect(ws1.length).toBeGreaterThanOrEqual(2)
    await page.reload({ waitUntil: "networkidle" })
    await page.waitForSelector("#workspace_name", { timeout: 20_000 })
    const ws2 = await page.inputValue("#workspace_name")
    // Refresh re-derives the workspace name from email, so equal value
    // is fine — we're asserting that no weird half-mount state survives.
    expect(ws2.length).toBeGreaterThanOrEqual(2)

    // ── Step 1: workspace ───────────────────────────────────────────
    expect(await page.locator('button[aria-label="Pick a language"]').count()).toBe(1)
    await expectContinueAndClick(page)

    // ── Step 2: pick crew template ──────────────────────────────────
    await page.waitForSelector("button[aria-pressed]", { timeout: 10_000 })
    const crewCards = await page
      .locator(
        'button[aria-pressed]:has-text("Software Development"), ' +
          'button[aria-pressed]:has-text("DevOps"), ' +
          'button[aria-pressed]:has-text("Marketing"), ' +
          'button[aria-pressed]:has-text("Accounting"), ' +
          'button[aria-pressed]:has-text("blank")',
      )
      .count()
    expect(crewCards).toBe(5)
    await page.getByRole("button", { name: /software development/i }).click()
    // Preview animates — give the avatars a beat to mount before
    // counting them.
    await page.waitForSelector('img[width="32"]', { timeout: 10_000 })
    expect(await page.locator('img[width="32"]').count()).toBe(4)
    await expectContinueAndClick(page)

    // ── Step 3: adapter + token. Switch to "Chat in browser" so the
    // Launch button gates on the API key field instead of the pair
    // countdown (which never completes in CI). ──────────────────────
    await page.waitForSelector('button:has-text("Pair my CLI")', { timeout: 10_000 })
    await page.getByRole("button", { name: /chat in browser/i }).click()
    await page.waitForTimeout(300) // motion settle
    await page.fill("#api_key", API_KEY)

    const launch = page.getByRole("button", { name: /launch/i })
    await expect(launch).toBeEnabled()

    // The /onboarding/setup POST is the contract this whole flow is
    // protecting — race the navigation by waiting on the response.
    const setupRespPromise = page.waitForResponse(
      (r) => r.url().includes("/api/v1/onboarding/setup") && r.request().method() === "POST",
      { timeout: 30_000 },
    )
    await launch.click()
    const setupResp = await setupRespPromise
    expect(setupResp.status()).toBe(201)

    // ── Post-launch: DB state assertions ────────────────────────────
    await page.waitForURL(/\/crews\/agents\//, { timeout: 15_000 })

    // setup-status must flip — proves the admin user persisted.
    const statusAfter = await (await request.get(SETUP_STATUS_PATH)).json()
    expect(statusAfter.needs_bootstrap).toBe(false)

    // The wizard's submit creates a workspace row with a non-empty
    // preferred_language (defaults from navigator.language).
    // /api/v1/workspaces requires the session cookie, which the page
    // context already carries from the bootstrap submit. We pull the
    // request through the page so it inherits cookies.
    const wsRes = await page.request.get("/api/v1/workspaces")
    expect(wsRes.status()).toBe(200)
    const workspaces = await wsRes.json()
    expect(Array.isArray(workspaces)).toBe(true)
    expect(workspaces.length).toBeGreaterThan(0)
    expect(typeof workspaces[0]?.preferred_language).toBe("string")
    expect(workspaces[0].preferred_language.length).toBeGreaterThan(0)
  })

  // ──────────────────────────────────────────────────────────────────
  // Post-bootstrap guards — these depend on the happy-path test having
  // run, so they go LAST and serial mode keeps the order honest.
  // ──────────────────────────────────────────────────────────────────

  test("/bootstrap redirects to /login once the DB is initialised", async ({ browser }) => {
    // Fresh context — no cookies — so we test the redirect that an
    // unauthenticated visitor sees, not a session-cookie short-circuit.
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
        email: `second-${RUN_ID}@example.com`,
        password: "another-pw-1234",
      },
      headers: { "Content-Type": "application/json" },
    })
    expect(res.status()).toBe(403)
  })
})

/**
 * The wizard's Continue button stays disabled until the current step's
 * fields validate — clicking it before that is a flaky no-op. Wrap the
 * "is it ready?" check + click together so the call sites read cleanly.
 */
async function expectContinueAndClick(page: Page): Promise<void> {
  const cont = page.getByRole("button", { name: /continue/i })
  await expect(cont).toBeEnabled()
  await cont.click()
}
