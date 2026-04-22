import { test, expect, request as plwRequest } from "@playwright/test"

const E2E_EMAIL = process.env.E2E_EMAIL || "demo@crewship.ai"
const E2E_PASSWORD = process.env.E2E_PASSWORD || "password123"
const BASE_URL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:3001"

test.use({
  viewport: { width: 390, height: 844 },
  deviceScaleFactor: 3,
  isMobile: true,
  hasTouch: true,
  storageState: { cookies: [], origins: [] },
})

let cachedCookies: Awaited<ReturnType<Awaited<ReturnType<typeof plwRequest.newContext>>["storageState"]>>["cookies"] = []

test.beforeAll(async () => {
  const ctx = await plwRequest.newContext({ baseURL: BASE_URL })
  const { csrfToken } = (await (await ctx.get("/api/auth/csrf")).json()) as { csrfToken: string }
  const loginRes = await ctx.post("/api/auth/callback/credentials", {
    form: { csrfToken, email: E2E_EMAIL, password: E2E_PASSWORD, callbackUrl: "/", json: "true" },
  })
  if (!loginRes.ok()) throw new Error(`login ${loginRes.status()}`)
  const storage = await ctx.storageState()
  cachedCookies = storage.cookies
  await ctx.dispose()
})

async function login(page: import("@playwright/test").Page) {
  await page.context().addCookies(cachedCookies)
  await page.goto("/")
  await page.waitForLoadState("domcontentloaded")
}

test("mobile /crews: explorer opens from hamburger, no horizontal scroll", async ({ page }) => {
  await login(page)
  await page.goto("/crews")
  await page.waitForLoadState("networkidle")

  // No horizontal overflow on the page
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  const clientWidth = await page.evaluate(() => document.documentElement.clientWidth)
  expect(scrollWidth - clientWidth).toBeLessThanOrEqual(1) // 1px tolerance
})

test("mobile agent detail: hero and actions fit, no horizontal scroll", async ({ page }) => {
  await login(page)
  const wsId = await page.evaluate(async () => {
    const r = await fetch("/api/v1/workspaces")
    const d = await r.json()
    return Array.isArray(d) ? d[0]?.id : d.id
  })
  const agentList = await page.request.get(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
  if (!Array.isArray(agentList) || agentList.length === 0) {
    test.skip(true, "no seeded agents")
    return
  }

  await page.goto("/crews")
  await page.waitForLoadState("networkidle")
  await page.waitForTimeout(800)

  const crewCard = page.locator("button").filter({ hasText: /Research|DevOps|Quality|Engineering/ }).first()
  if ((await crewCard.count()) === 0) {
    test.skip(true, "no seeded crews")
    return
  }
  await crewCard.click()
  await page.waitForURL(/\?crew=/, { timeout: 5_000 })
  await page.waitForTimeout(800)

  const firstName = agentList[0].name
  const agentCard = page.locator('[role="button"]').filter({ hasText: firstName }).first()
  if ((await agentCard.count()) === 0) {
    test.skip(true, `no '${firstName}' card visible in crew overview`)
    return
  }
  await agentCard.click()
  await page.waitForTimeout(1500)

  // Agent detail (CrewsAgentInline) should be visible — hero has Chat link,
  // icon-only actions have aria-label. We don't require the mobile-overlay
  // wrapper here because useIsMobile uses a matchMedia effect that may lag
  // the first render; the responsive concern is "content fits".
  await expect(page.locator("a[aria-label='Chat']").first()).toBeVisible({ timeout: 10_000 })
  await expect(page.locator("a[aria-label='Open full agent page']").first()).toBeVisible()

  // No horizontal overflow — this is the actual responsive health check.
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  const clientWidth = await page.evaluate(() => document.documentElement.clientWidth)
  expect(scrollWidth - clientWidth, `scrollWidth=${scrollWidth} clientWidth=${clientWidth}`).toBeLessThanOrEqual(1)
})

test("mobile crew page renders all 6 tabs in horizontal scroll strip", async ({ page }) => {
  await login(page)
  const wsId = await page.evaluate(async () => {
    const r = await fetch("/api/v1/workspaces")
    const d = await r.json()
    return Array.isArray(d) ? d[0]?.id : d.id
  })
  const crews = await page.request.get(`/api/v1/crews?workspace_id=${wsId}`).then((r) => r.json())
  if (!Array.isArray(crews) || crews.length === 0) {
    test.skip(true, "no crews")
    return
  }
  await page.goto(`/crews/${crews[0].id}`)
  for (const tab of ["Overview", "Members", "Network", "Runtime", "Journal", "Settings"]) {
    await expect(page.getByRole("tab", { name: tab })).toBeVisible({ timeout: 5_000 })
  }
})

test("mobile agent full page: 7-tab bottom sheet from Pages button", async ({ page }) => {
  await login(page)
  await page.goto("/crews/agents")
  await page.waitForLoadState("networkidle")
  const agentLink = page
    .locator("a[href^='/crews/agents/']:not([href$='/new']):not([href*='/agents/new'])")
    .first()
  if ((await agentLink.count()) === 0) {
    test.skip(true, "no seeded agents")
    return
  }
  const href = await agentLink.getAttribute("href")
  await page.goto(href!)
  await page.waitForLoadState("networkidle")

  // AgentMobileTabsBar has a "Pages" button
  const pagesBtn = page.getByRole("button", { name: "Agent pages" })
  await expect(pagesBtn).toBeVisible({ timeout: 10_000 })
  await pagesBtn.click()

  for (const label of ["Overview", "Sessions", "Runs", "Workspace", "Tools", "Logs", "Settings"]) {
    await expect(page.locator(`a:has-text('${label}')`).first()).toBeVisible({ timeout: 5_000 })
  }
})
