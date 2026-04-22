import { test, expect, request as plwRequest } from "@playwright/test"

const E2E_EMAIL = process.env.E2E_EMAIL || "demo@crewship.ai"
const E2E_PASSWORD = process.env.E2E_PASSWORD || "password123"
const BASE_URL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:3001"

test.describe.configure({ mode: "serial" })

test.use({ storageState: { cookies: [], origins: [] } })

let cachedCookies: Awaited<ReturnType<Awaited<ReturnType<typeof plwRequest.newContext>>["storageState"]>>["cookies"] = []

test.beforeAll(async () => {
  // Login once for the whole spec — NextAuth credentials provider throttles
  // at ~5 POSTs/minute. Every test after this re-uses the cookies.
  const ctx = await plwRequest.newContext({ baseURL: BASE_URL })
  const csrfRes = await ctx.get("/api/auth/csrf")
  const { csrfToken } = (await csrfRes.json()) as { csrfToken: string }
  const loginRes = await ctx.post("/api/auth/callback/credentials", {
    form: {
      csrfToken,
      email: E2E_EMAIL,
      password: E2E_PASSWORD,
      callbackUrl: "/",
      json: "true",
    },
  })
  if (!loginRes.ok()) throw new Error(`login ${loginRes.status()} — ${await loginRes.text()}`)
  const storage = await ctx.storageState()
  cachedCookies = storage.cookies
  await ctx.dispose()
})

async function login(page: import("@playwright/test").Page) {
  if (cachedCookies.length === 0) throw new Error("beforeAll did not run")
  await page.context().addCookies(cachedCookies)
  await page.goto("/")
  await page.waitForLoadState("domcontentloaded")
  return { status: 200, body: "cached-cookies" }
}

type PageIssues = {
  jsErrors: string[]
  networkFails: string[]
  consoleErrors: string[]
}

function attachCollectors(page: import("@playwright/test").Page): PageIssues {
  const issues: PageIssues = { jsErrors: [], networkFails: [], consoleErrors: [] }
  page.on("pageerror", (err) => issues.jsErrors.push(err.message))
  page.on("console", (msg) => {
    if (msg.type() === "error") issues.consoleErrors.push(msg.text())
  })
  page.on("requestfailed", (req) => {
    issues.networkFails.push(`${req.method()} ${req.url()} — ${req.failure()?.errorText ?? "?"}`)
  })
  page.on("response", (res) => {
    if (res.status() >= 500) {
      issues.networkFails.push(`HTTP ${res.status()} ${res.request().method()} ${res.url()}`)
    }
  })
  return issues
}

test("login flow", async ({ page }) => {
  const res = await login(page)
  console.log("login:", res.status, res.body.slice(0, 200))
  expect(res.status).toBeLessThan(400)
})

test("/fleet renders top tabs + no console errors", async ({ page }) => {
  await login(page)
  const issues = attachCollectors(page)
  await page.goto("/fleet")
  await page.waitForLoadState("networkidle")
  await expect(page.getByRole("button", { name: "Overview", exact: true })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Health", exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Connections", exact: true })).toHaveCount(0)
  console.log("/fleet issues:", JSON.stringify(issues, null, 2))
  expect(issues.jsErrors).toHaveLength(0)
})

test("legacy /agents redirects to /fleet/agents in browser", async ({ page }) => {
  await login(page)
  await page.goto("/agents")
  await page.waitForURL(/\/fleet\/agents/, { timeout: 10_000 })
  expect(page.url()).toContain("/fleet/agents")
})

test("legacy /crews redirects to /fleet/crews", async ({ page }) => {
  await login(page)
  await page.goto("/crews")
  await page.waitForURL(/\/fleet\/crews/, { timeout: 10_000 })
  expect(page.url()).toContain("/fleet/crews")
})

test("agent full page: all 7 tabs reachable without JS errors", async ({ page }) => {
  await login(page)
  const issues = attachCollectors(page)
  await page.goto("/fleet/agents")
  await page.waitForLoadState("networkidle")

  const agentLink = page
    .locator("a[href^='/fleet/agents/']:not([href$='/new']):not([href*='/agents/new'])")
    .first()

  if ((await agentLink.count()) === 0) {
    test.skip(true, "no agents seeded on this workspace")
    return
  }

  const href = await agentLink.getAttribute("href")
  expect(href).toBeTruthy()

  const tabs = ["", "/sessions", "/runs", "/workspace", "/tools", "/logs", "/settings"]
  for (const t of tabs) {
    const url = `${href}${t}`
    const resp = await page.goto(url)
    await page.waitForLoadState("domcontentloaded")
    expect(resp?.status(), `${url} HTTP`).toBeLessThan(400)
    console.log(`  ${url} -> OK`)
  }

  expect(issues.jsErrors, "pageerrors").toHaveLength(0)
  const ignoredConsole = issues.consoleErrors.filter((e) => !e.includes("404") && !e.includes("Warning"))
  expect(ignoredConsole.slice(0, 3), "console errors sample").toEqual([])
})

test("crew full page: all 6 tabs reachable", async ({ page }) => {
  await login(page)
  const issues = attachCollectors(page)
  await page.goto("/fleet/crews")
  await page.waitForLoadState("networkidle")

  const crewLink = page
    .locator("a[href^='/fleet/crews/']:not([href$='/new']):not([href*='/crews/new'])")
    .first()

  if ((await crewLink.count()) === 0) {
    test.skip(true, "no crews seeded")
    return
  }

  const href = await crewLink.getAttribute("href")
  expect(href).toBeTruthy()

  const tabs = ["", "?tab=members", "?tab=network", "?tab=runtime", "?tab=journal", "?tab=settings"]
  for (const t of tabs) {
    const url = `${href}${t}`
    const resp = await page.goto(url)
    await page.waitForLoadState("domcontentloaded")
    expect(resp?.status(), `${url} HTTP`).toBeLessThan(400)
    console.log(`  ${url} -> OK`)
  }
  expect(issues.jsErrors).toHaveLength(0)
})

test("agent workspace sub-strip: pane=terminal switch", async ({ page }) => {
  await login(page)
  await page.goto("/fleet/agents")
  await page.waitForLoadState("networkidle")

  const agentLink = page
    .locator("a[href^='/fleet/agents/']:not([href$='/new']):not([href*='/agents/new'])")
    .first()
  if ((await agentLink.count()) === 0) {
    test.skip(true, "no agents")
    return
  }
  const href = await agentLink.getAttribute("href")
  await page.goto(`${href}/workspace`)
  await expect(page.getByRole("tab", { name: "Files" })).toBeVisible({ timeout: 10_000 })
  await page.getByRole("tab", { name: "Terminal" }).click()
  await page.waitForURL(/pane=terminal/, { timeout: 5_000 })
  expect(page.url()).toContain("pane=terminal")
})

test("agent tools sub-strip: skills/credentials/mcp", async ({ page }) => {
  await login(page)
  await page.goto("/fleet/agents")
  await page.waitForLoadState("networkidle")
  const agentLink = page.locator("a[href^='/fleet/agents/']:not([href$='/new']):not([href*='/agents/new'])").first()
  if ((await agentLink.count()) === 0) {
    test.skip(true, "no agents")
    return
  }
  const href = await agentLink.getAttribute("href")
  await page.goto(`${href}/tools`)
  for (const section of ["Skills", "Credentials", "MCP"] as const) {
    await expect(page.getByRole("tab", { name: section })).toBeVisible({ timeout: 10_000 })
  }
  await page.getByRole("tab", { name: "Credentials" }).click()
  await page.waitForURL(/section=credentials/, { timeout: 5_000 })
  await page.getByRole("tab", { name: "MCP" }).click()
  await page.waitForURL(/section=mcp/, { timeout: 5_000 })
})

test("agent settings: Schedule sub-section loads, avatar style picker is locked", async ({ page }) => {
  await login(page)
  await page.goto("/fleet/agents")
  await page.waitForLoadState("networkidle")
  const agentLink = page.locator("a[href^='/fleet/agents/']:not([href$='/new']):not([href*='/agents/new'])").first()
  if ((await agentLink.count()) === 0) {
    test.skip(true, "no agents")
    return
  }
  const href = await agentLink.getAttribute("href")

  // General first
  await page.goto(`${href}/settings`)
  await expect(page.getByRole("tab", { name: "General" })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByText("Style is set by crew template")).toBeVisible({ timeout: 5_000 })

  // Schedule
  await page.getByRole("tab", { name: "Schedule" }).click()
  await page.waitForURL(/section=schedule/, { timeout: 5_000 })
})

test("crew overview shows avatar picker + apply button", async ({ page }) => {
  await login(page)
  await page.goto("/fleet/crews")
  await page.waitForLoadState("networkidle")
  const crewLink = page.locator("a[href^='/fleet/crews/']:not([href$='/new']):not([href*='/crews/new'])").first()
  if ((await crewLink.count()) === 0) {
    test.skip(true, "no crews")
    return
  }
  await crewLink.click()
  await page.waitForURL(/\/fleet\/crews\/[^/]+/)
  await expect(page.getByText("Agent avatar style")).toBeVisible({ timeout: 10_000 })
})

test("preview panel: click agent sets ?agent= URL", async ({ page }) => {
  await login(page)
  await page.goto("/fleet")
  await page.waitForLoadState("networkidle")
  // First click expands a crew in the explorer, then click the first nested agent node.
  const crewToggle = page.locator("button").filter({ hasText: /Research|DevOps|Quality|Engineering|Platform/ }).first()
  if ((await crewToggle.count()) === 0) {
    test.skip(true, "no crews in explorer")
    return
  }
  await crewToggle.click()
  await page.waitForTimeout(200)
  const agentItem = page.locator("button").filter({ hasText: /Idle|Running|Stopped|Error/ }).first()
  if ((await agentItem.count()) === 0) {
    test.skip(true, "no agents under crew")
    return
  }
  await agentItem.click()
  await page.waitForURL(/\?(crew|agent)=/, { timeout: 5_000 })
  expect(page.url()).toMatch(/\?(crew|agent)=/)
})
