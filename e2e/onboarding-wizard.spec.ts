import { test, expect, type Page } from "@playwright/test"

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
 * Bootstrap is a one-shot (POST /api/v1/bootstrap returns 410 once a
 * user exists — the header said 403, the test at the bottom of this
 * file has always asserted 410), so the whole describe block runs
 * serially with the
 * validation tests first — they fail submission so they don't consume
 * the shot.
 */

const SETUP_STATUS_PATH = "/api/v1/system/setup-status"

// Token only needs to be syntactically plausible; the server-side
// probe is gated off in CI. Don't expose this as an env var — a real
// token here would hit api.anthropic.com whenever the gate isn't set.
const FAKE_API_KEY = "sk-ant-oat-e2e-fake-token"

// Next.js keeps a permanently-mounted <div role="alert"
// id="__next-route-announcer__"> in the DOM for client-side route
// announcements, so a bare getByRole("alert") always resolves to two
// elements and trips Playwright's strict mode — even when the form's own
// alert renders exactly as expected. Scope every assertion to the real
// alert by excluding the announcer.
const formAlert = (page: Page) => page.locator('[role="alert"]:not(#__next-route-announcer__)')

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

  // #confirm_password is `required`, so leaving it empty makes the browser
  // block submit and the handler never runs — no alert, and the assertion
  // below fails on "element(s) not found" rather than on the wrong text.
  // Every test that expects to reach a validation message must fill it.
  test("bootstrap form rejects short name", async ({ page }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", "A")
    await page.fill("#email", `pre-${email}`)
    await page.fill("#password", "long-enough-pw")
    await page.fill("#confirm_password", "long-enough-pw")
    await page.click("button[type=submit]")
    await expect(formAlert(page)).toContainText(/at least 2 characters/i)
    expect(page.url()).toContain("/bootstrap")
  })

  test("bootstrap form rejects short password", async ({ page }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", fullName)
    await page.fill("#email", `pre-${email}`)
    await page.fill("#password", "short")
    await page.fill("#confirm_password", "short")
    await page.click("button[type=submit]")
    await expect(formAlert(page)).toContainText(/at least 8 characters/i)
    expect(page.url()).toContain("/bootstrap")
  })

  // The confirmation field is the reason this form has four inputs: /bootstrap
  // creates the account that owns the workspace before any session exists, so
  // a typo is only discoverable at the next sign-in. Mismatch must be caught
  // client-side and must NOT burn the one bootstrap the empty DB allows.
  test("bootstrap form rejects a password that does not match its confirmation", async ({
    page,
  }) => {
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await page.fill("#full_name", fullName)
    await page.fill("#email", `pre-${email}`)
    await page.fill("#password", "long-enough-pw")
    await page.fill("#confirm_password", "long-enough-px")
    await page.click("button[type=submit]")
    await expect(formAlert(page)).toContainText(/don't match/i)
    expect(page.url()).toContain("/bootstrap")
  })

  // ── Happy path — single test because the wizard is single-page
  // (step state in React useState; no per-step URL to split on). ──

  test("bootstrap → wizard (3 steps) → launch → DB rows present", async ({ page, request }) => {
    test.setTimeout(90_000)

    // Step 2 now refuses to advance unless THIS server is driving a container
    // runtime — step 3 opens a chat with an agent that runs in one, and
    // letting the user through without it produced two stacked errors on their
    // first message instead. Stub the probe so this spec keeps measuring the
    // WIZARD rather than whether the box running it happens to have Docker
    // wired: dev slots routinely run `crewship start --no-docker`, where the
    // real endpoint honestly answers in_use=false.
    await page.route("**/api/v1/system/runtime", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ available: true, in_use: true }),
      }),
    )

    // Step 2 now PROVES the token before it stores it: persistAdapterCredential
    // calls POST /api/v1/credentials/test, and a key that does not work is not
    // written at all. That is the right product behaviour — it is what stops a
    // crew launching around a dead token — and it is fatal to this spec, which
    // types FAKE_API_KEY. Without the stub the real endpoint asks Anthropic
    // about a key that was never real, the answer is no, POST /credentials
    // never fires, and the spec times out waiting for it.
    //
    // Stubbed for the same reason as the runtime probe above: what is under
    // test is the WIZARD, not whether the CI runner can reach a model provider.
    // The endpoint's own behaviour is covered by internal/api's
    // credentials_test_endpoint tests, against a stubbed provider, where it
    // belongs.
    await page.route("**/api/v1/credentials/test", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        // The real shape (internal/api/credentials_test_endpoint.go):
        // `supported` says the server can check this credential type at all,
        // `valid` is the provider's verdict. Both are read, and a stub that
        // returns only `valid` is refused with "This credential type cannot
        // be verified before setup."
        body: JSON.stringify({ supported: true, valid: true, status: 200 }),
      }),
    )

    // Bootstrap form
    await page.goto("/bootstrap")
    await page.waitForSelector("#full_name")
    await expect(page.getByText(/initial setup/i)).toBeVisible()
    await page.fill("#full_name", fullName)
    await page.fill("#email", email)
    await page.fill("#password", password)
    await page.fill("#confirm_password", password)
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

    // Step 1 is durable too: its Continue PATCHes the workspace name and
    // language, and preferred_language acts as the server-side checkpoint.
    // Reloading here must resume at Adapter, not ask for the workspace again.
    await page.waitForSelector('button:has-text("Pair my CLI")', { timeout: 10_000 })
    await page.reload({ waitUntil: "networkidle" })
    await expect(page.locator("#workspace_name")).toHaveCount(0)

    // Step 2: adapter + token. Switch to browser mode so Continue gates on
    // the API key field instead of the pair countdown (which never
    // completes in CI).
    await page.waitForSelector('button:has-text("Pair my CLI")', { timeout: 10_000 })
    await page.getByRole("button", { name: /chat in browser/i }).click()
    // Wait for the pair snippet to actually leave the DOM rather than
    // sleeping for a magic motion duration.
    await expect(page.locator('code:has-text("crewship login --pair")')).toBeHidden()
    await page.fill("#api_key", FAKE_API_KEY)

    const continueFromAdapter = page.getByRole("button", { name: /continue/i })
    await expect(continueFromAdapter).toBeEnabled()
    // Continuing off this step persists the token via POST /api/v1/credentials
    // (page.tsx's persistAdapterCredential) so step 3's default chat with the
    // setup agent doesn't 428 credential_required — wait for that write to
    // land before asserting on the next step.
    const credentialRespPromise = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/credentials" &&
        r.request().method() === "POST",
      { timeout: 15_000 },
    )
    await continueFromAdapter.click()
    expect((await credentialRespPromise).status()).toBe(201)

    // This is the resume boundary that failed in production: after the
    // credential had been encrypted successfully, a reload/re-login rebuilt
    // React state at step 1 and demanded both the workspace and token again.
    // Durable state must take us straight back to Crew without ever revealing
    // the saved secret to the browser.
    await page.reload({ waitUntil: "networkidle" })
    await expect(page.locator("#workspace_name")).toHaveCount(0)
    await expect(page.getByRole("button", { name: /prefer to pick a template instead/i })).toBeVisible({
      timeout: 20_000,
    })

    // Step 3: pick crew template — the escape hatch, not the chat default,
    // since the chat would need a real setup-agent conversation this suite
    // doesn't drive. AnimatePresence mounts only the active step, so visible
    // aria-pressed buttons are all crew cards — assert the exact count so
    // adding *or* removing a template trips the test.
    await page.getByRole("button", { name: /prefer to pick a template instead/i }).click()
    await page.waitForSelector("button[aria-pressed]", { timeout: 10_000 })
    await expect(page.locator("button[aria-pressed]")).toHaveCount(5)
    await page.getByRole("button", { name: /software development/i }).click()
    await page.waitForSelector('img[width="32"]', { timeout: 10_000 })
    expect(await page.locator('img[width="32"]').count()).toBe(4)

    const launch = page.getByRole("button", { name: /launch/i })
    await expect(launch).toBeEnabled()

    const setupRespPromise = page.waitForResponse(
      (r) => r.url().includes("/api/v1/onboarding/setup") && r.request().method() === "POST",
      { timeout: 30_000 },
    )
    await launch.click()
    expect((await setupRespPromise).status()).toBe(201)

    // Launch no longer redirects. It holds on a receipt — the last thing a
    // person saw of their own setup used to be a chat composer, which never
    // told them what had been built, and with more than one crew there is no
    // single chat that represents the work. So the navigation is now a click,
    // and this spec has to make it.
    await expect(page.getByRole("heading", { name: /your crew is ready|your \d+ crews are ready/i }))
      .toBeVisible({ timeout: 15_000 })

    // "Start chatting" when the agent slug resolved, "Go to dashboard" when it
    // did not — handleLaunch resolves it with one GET /agents/<id> and falls
    // back deliberately, so both buttons are legitimate. Take whichever is
    // offered, then assert below that it was the good one: a fallback becomes
    // a one-line failure naming its cause instead of a 15s timeout.
    await page.getByRole("button", { name: /start chatting|go to dashboard/i }).first().click()

    await page.waitForURL((url) => /^\/chat\/[^/]+$/.test(url.pathname) || url.pathname === "/", {
      timeout: 15_000,
    })
    expect(
      new URL(page.url()).pathname,
      "wizard fell back to the dashboard — GET /agents/<id> did not yield a slug, so the new agent's chat was never linked",
    ).toMatch(/^\/chat\/[^/]+$/)

    // A route that resolves is not the same as a chat that works. The
    // composer is the thing the first click exists to reach; its
    // placeholder is `Message <agent>...`, or "Send a message..." before
    // the agent name has resolved (chat-composer.tsx).
    await expect(
      page.locator('textarea[placeholder^="Message "], textarea[placeholder="Send a message..."]'),
    ).toBeVisible({ timeout: 15_000 })

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

  test("POST /api/v1/bootstrap returns 410 after first user exists", async ({ request }) => {
    // 410, not 403: AuthHandler.Bootstrap (internal/api/auth.go) answers a
    // re-POST on an initialised instance with Gone + "please log in at
    // /login instead", deliberately ahead of the window check so the
    // message is actionable. The Go side pins the same code in
    // internal/api/auth_test.go.
    //
    // Polled because /api/v1/bootstrap shares one 10-req/min per-IP bucket
    // with /api/auth/* (routeWithRateLimiting, internal/api/router.go) and
    // the wizard run above spends it on session calls — the first attempts
    // here come back 429, so a single-shot assertion would observe the
    // rate limiter instead of the contract. Worst case costs one window.
    test.setTimeout(120_000)
    await expect
      .poll(
        async () => {
          const res = await request.post("/api/v1/bootstrap", {
            data: {
              full_name: "Second Admin",
              email: `second-${runId}@example.com`,
              password: "another-pw-1234",
            },
            headers: { "Content-Type": "application/json" },
          })
          return res.status()
        },
        { timeout: 90_000, intervals: [5_000] },
      )
      .toBe(410)
  })
})
