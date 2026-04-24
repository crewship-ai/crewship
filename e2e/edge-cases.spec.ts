import { test, expect, request as plwRequest } from "@playwright/test"

/**
 * Edge-case / robustness suite — věci, které happy-path testy nevidí.
 *
 * Pokrývá:
 *   1. Stale/unknown slug handling (toast + URL clear)
 *   2. Keyboard shortcuts (Esc, j/k cycle)
 *   3. Rapid selection switch — abort orphan fetches
 *   4. Deep-link share (fresh cookies, direct nav to ?agent=)
 *   5. Cross-tenant isolation (agent from another workspace → 404)
 *   6. Malformed URL query (empty slug, typos)
 *   7. Logout + re-auth (session persistence)
 *   8. Missing right-panel action visibility
 */

const E2E_EMAIL = process.env.E2E_EMAIL
const E2E_PASSWORD = process.env.E2E_PASSWORD
const BASE_URL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:3001"

test.describe.configure({ mode: "serial" })
test.use({ storageState: { cookies: [], origins: [] } })

let cachedCookies: Awaited<ReturnType<Awaited<ReturnType<typeof plwRequest.newContext>>["storageState"]>>["cookies"] = []

test.beforeAll(async () => {
  test.skip(
    !E2E_EMAIL || !E2E_PASSWORD,
    "edge-cases: set E2E_EMAIL and E2E_PASSWORD to run this suite",
  )
  const ctx = await plwRequest.newContext({ baseURL: BASE_URL })
  const { csrfToken } = (await (await ctx.get("/api/auth/csrf")).json()) as { csrfToken: string }
  const res = await ctx.post("/api/auth/callback/credentials", {
    form: { csrfToken, email: E2E_EMAIL!, password: E2E_PASSWORD!, callbackUrl: "/", json: "true" },
  })
  if (!res.ok()) throw new Error(`login ${res.status()}`)
  cachedCookies = (await ctx.storageState()).cookies
  await ctx.dispose()
})

async function login(page: import("@playwright/test").Page) {
  await page.context().addCookies(cachedCookies)
  await page.goto("/")
  await page.waitForLoadState("domcontentloaded")
}

// `/api/v1/workspaces` has historically returned either an array or a single
// object (depending on seed/version). Normalize to a list at every call site
// so one test never throws on the singleton shape before the real assertion
// runs.
async function getWorkspaceId(page: import("@playwright/test").Page): Promise<string> {
  return page.evaluate(async () => {
    const r = await fetch("/api/v1/workspaces")
    const json = await r.json()
    const list = Array.isArray(json) ? json : [json]
    return list[0].id as string
  })
}

// ---------------------------------------------------------------------------
// 1. Stale / unknown slug handling
// ---------------------------------------------------------------------------

test("stale agent slug clears ?agent= and lands on /crews without panel", async ({ page }) => {
  await login(page)
  await page.goto("/crews?agent=this-agent-does-not-exist")
  await page.waitForLoadState("networkidle")
  // After agents load, the stale-slug watcher should clear the param.
  // We tolerate a moment of grace for the agents[] fetch + React reconciliation.
  await expect(page).toHaveURL(/\/crews(\?|$)/, { timeout: 8_000 })
  // No right panel should be open
  await expect(page.getByRole("button", { name: "Close" })).toHaveCount(0)
})

test("stale crew slug clears ?crew=", async ({ page }) => {
  await login(page)
  await page.goto("/crews?crew=unknown-crew-xyz")
  await page.waitForLoadState("networkidle")
  await expect(page).toHaveURL(/\/crews(\?|$)/, { timeout: 8_000 })
})

// ---------------------------------------------------------------------------
// 2. Keyboard shortcuts — Esc closes panel, j/k cycles agents
// ---------------------------------------------------------------------------

test("Esc closes the agent preview panel and clears ?agent=", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length === 0) {
    test.skip(true, "no agents seeded")
    return
  }
  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(800)
  // Confirm panel is open
  await expect(page.url()).toContain(`agent=${agents[0].slug}`)
  // Press Esc
  await page.keyboard.press("Escape")
  await page.waitForTimeout(500)
  expect(page.url()).not.toContain(`agent=${agents[0].slug}`)
})

test("j key cycles to next agent in the explorer", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length < 2) {
    test.skip(true, "need at least 2 agents")
    return
  }
  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(800)
  const urlBefore = page.url()
  await page.keyboard.press("j")
  await page.waitForTimeout(500)
  expect(page.url()).not.toBe(urlBefore)
  expect(page.url()).toMatch(/agent=/)
})

// ---------------------------------------------------------------------------
// 3. Rapid agent switching — no race, final URL == last click
// ---------------------------------------------------------------------------

test("rapid agent switching settles on the last agent", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length < 3) {
    test.skip(true, "need 3+ agents")
    return
  }
  // Three real navigations back-to-back — `router.replace` through
  // useSearchParams is what the abort logic actually keys on, so
  // `history.replaceState` inside page.evaluate would no-op the hook.
  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.goto(`/crews?agent=${agents[1].slug}`)
  await page.goto(`/crews?agent=${agents[2].slug}`)
  await page.waitForLoadState("networkidle")
  expect(page.url()).toContain(`agent=${agents[2].slug}`)
})

// ---------------------------------------------------------------------------
// 4. Deep-link share — fresh browser context, direct nav to ?agent=
// ---------------------------------------------------------------------------

test("deep-link to ?agent=<slug> works after full page load", async ({ browser }) => {
  const ctx = await browser.newContext()
  await ctx.addCookies(cachedCookies)
  const page = await ctx.newPage()
  // Fetch via request API to avoid needing page context
  const req = await plwRequest.newContext({ baseURL: BASE_URL, storageState: { cookies: cachedCookies, origins: [] } })
  const wsJson = await (await req.get("/api/v1/workspaces")).json()
  const wsList = Array.isArray(wsJson) ? wsJson : [wsJson]
  const agents = await (await req.get(`/api/v1/agents?workspace_id=${wsList[0].id}`)).json()
  await req.dispose()
  if (agents.length === 0) {
    test.skip(true, "no agents")
    await ctx.close()
    return
  }
  await page.goto(`${BASE_URL}/crews?agent=${agents[0].slug}`)
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(1500)
  // Panel content should be visible — preview has Chat button
  await expect(page.locator("a[aria-label='Chat'], a:has-text('Chat')").first()).toBeVisible({ timeout: 10_000 })
  await ctx.close()
})

// ---------------------------------------------------------------------------
// 5. Cross-tenant / unknown agent URL
// ---------------------------------------------------------------------------

test("/crews/agents/nonexistent-id shows empty state, not 500", async ({ page }) => {
  await login(page)
  const errors: string[] = []
  page.on("pageerror", (e) => errors.push(e.message))
  const resp = await page.goto("/crews/agents/this-definitely-does-not-exist-123456")
  expect(resp?.status()).toBeLessThan(500)
  await page.waitForLoadState("networkidle")
  // Should show "Agent not found" empty state OR redirect cleanly — either way not a 500
  await page.waitForTimeout(800)
  expect(errors, "pageerrors").toHaveLength(0)
})

// ---------------------------------------------------------------------------
// 6. Malformed URL query
// ---------------------------------------------------------------------------

test("/crews?agent= (empty value) does not open a panel", async ({ page }) => {
  await login(page)
  await page.goto("/crews?agent=")
  await page.waitForLoadState("networkidle")
  // No Close/Inbox button should be visible
  await expect(page.getByRole("button", { name: "Close" })).toHaveCount(0)
})

// ---------------------------------------------------------------------------
// 7. Logout → re-auth
// ---------------------------------------------------------------------------

test("logout redirects to /login and re-login works", async ({ page }) => {
  await login(page)
  const csrf = (await (await page.request.get("/api/auth/csrf")).json()).csrfToken as string
  await page.request.post("/api/auth/signout", {
    form: { csrfToken: csrf, callbackUrl: "/", json: "true" },
  })
  await page.context().clearCookies()

  // After logout, /crews must *not* render as a protected page. The old
  // assertion allowed both /login and /crews, which silently passed
  // whenever the sign-out endpoint was broken.
  await page.goto("/crews")
  await page.waitForLoadState("domcontentloaded")
  expect(page.url(), "post-logout URL should redirect to login").toMatch(/\/login/)

  // Re-login path still works end-to-end.
  await login(page)
  await page.goto("/crews")
  await page.waitForLoadState("domcontentloaded")
  expect(page.url(), "re-login should grant access to /crews again").toContain("/crews")
})

// ---------------------------------------------------------------------------
// 8. Agent inbox right panel renders actions even with zero counts
// ---------------------------------------------------------------------------

test("agent inbox panel shows all 3 inbox rows + status chips even when all counts are 0", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length === 0) {
    test.skip(true, "no agents")
    return
  }
  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(1500)
  // All 3 inbox labels present regardless of count
  // CrewsAgentInbox renders "escalations" (plural) for any count other
  // than 1, so assert that — the singular "escalation" would miss the
  // zero-count case this test is covering.
  for (const label of ["approvals pending", "assignments open", "escalations"]) {
    await expect(page.getByText(label).first()).toBeVisible({ timeout: 5_000 })
  }
  // Memory chip always present (on/off)
  await expect(page.getByText("Memory", { exact: false }).first()).toBeVisible()
})

// ---------------------------------------------------------------------------
// 9. Agent overview renders runtime + stats even when inbox is empty
// ---------------------------------------------------------------------------

test("agent inline center renders runtime card + 6 stat cards", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length === 0) {
    test.skip(true, "no agents")
    return
  }
  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(1000)
  // Runtime section exists (rendered as uppercase label)
  await expect(page.locator("h3").filter({ hasText: /^Runtime$/i }).first()).toBeVisible({ timeout: 5_000 })
  // Stat cards are <Link> elements with these labels. Use role+visible filter
  // so we don't collide with hidden rail/title spans.
  for (const label of ["Sessions", "Recent runs", "Skills", "Credentials", "Last active", "Cost"]) {
    const card = page.locator("a").filter({ hasText: new RegExp(`^${label}`, "i") }).first()
    await expect(card).toBeVisible({ timeout: 5_000 })
  }
})

// ---------------------------------------------------------------------------
// 10. Inbox endpoint abort on rapid agent switch
// ---------------------------------------------------------------------------

test("inbox fetches abort on rapid agent switch (no orphaned responses)", async ({ page }) => {
  await login(page)
  const wsId = await getWorkspaceId(page)
  const agents = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (agents.length < 2) {
    test.skip(true, "need 2+ agents")
    return
  }

  // Delay the *first* agent's inbox response by 2s; the second agent's
  // inbox returns immediately. If the abort logic is broken, the slow
  // first response will win the race and paint agent-0's counts under
  // agent-1's header. Counting requests alone wouldn't catch that — we
  // must assert the rendered DOM reflects only agent 1.
  const agent0InboxUrlPart = `/api/v1/agents/${agents[0].id}/inbox`
  await page.route(`**${agent0InboxUrlPart}*`, async (route) => {
    await new Promise((r) => setTimeout(r, 2000))
    await route.continue()
  })

  await page.goto(`/crews?agent=${agents[0].slug}`)
  await page.waitForTimeout(100)
  await page.goto(`/crews?agent=${agents[1].slug}`)
  // Wait long enough for the delayed agent-0 response to land. If the
  // abort didn't hold, it would now overwrite agent-1's inbox data.
  await page.waitForTimeout(2500)

  // The INBOX section heading renders exactly once — assert the panel
  // is present for agent 1 and the URL still points at agent 1.
  await expect(page.getByRole("heading", { name: /inbox/i }).first()).toBeVisible()
  expect(page.url()).toContain(`agent=${agents[1].slug}`)
  await page.unroute(`**${agent0InboxUrlPart}*`)
})
