import { test, expect } from "./fixtures/auth"

test.describe("Cruise Unification", () => {
  test("legacy /agents redirects to /cruise/agents", async ({ page }) => {
    await page.goto("/agents")
    await page.waitForURL("**/cruise/agents", { timeout: 10_000 })
    expect(page.url()).toContain("/cruise/agents")
  })

  test("legacy /crews redirects to /cruise/crews", async ({ page }) => {
    await page.goto("/crews")
    await page.waitForURL("**/cruise/crews", { timeout: 10_000 })
    expect(page.url()).toContain("/cruise/crews")
  })

  test("/cruise renders explorer and 3 top-level tabs", async ({ page }) => {
    await page.goto("/cruise")
    await page.waitForLoadState("networkidle")
    await expect(page.getByRole("button", { name: "Overview" })).toBeVisible({ timeout: 10_000 })
    await expect(page.getByRole("button", { name: "Activity" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Health" })).toBeVisible()
    await expect(page.getByRole("button", { name: "Connections" })).toHaveCount(0)
  })

  test("clicking a crew in explorer sets ?crew= URL param", async ({ page }) => {
    await page.goto("/cruise")
    await page.waitForLoadState("networkidle")
    const crewItem = page.locator("button").filter({ hasText: /Research|DevOps|Quality|Engineering/ }).first()
    if (await crewItem.count() === 0) {
      test.skip(true, "no seeded crews in dev workspace")
      return
    }
    await crewItem.click()
    await page.waitForURL(/\?crew=[a-z0-9-]+/, { timeout: 5_000 })
    expect(page.url()).toMatch(/\?crew=/)
  })

  test("agent full page has 7-tab rail", async ({ page }) => {
    await page.goto("/cruise")
    await page.waitForLoadState("networkidle")
    const agentLink = page.locator("a[href^='/cruise/agents/']").filter({ hasNotText: "new" }).first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents in dev workspace")
      return
    }
    await agentLink.click()
    await page.waitForURL(/\/cruise\/agents\/[^/]+/, { timeout: 10_000 })

    const rail = page.locator("a").filter({ has: page.locator("svg") })
    const labels = ["Overview", "Sessions", "Runs", "Workspace", "Tools", "Logs", "Settings"]
    for (const label of labels) {
      await expect(page.locator(`a[title='${label}'], a:has-text('${label}')`).first()).toBeVisible({ timeout: 5_000 })
    }
  })

  test("crew detail renders 6 tabs", async ({ page }) => {
    await page.goto("/cruise/crews")
    await page.waitForLoadState("networkidle")
    const crewLink = page.locator("a[href^='/cruise/crews/']").filter({ hasNotText: "new" }).first()
    if (await crewLink.count() === 0) {
      test.skip(true, "no seeded crews")
      return
    }
    await crewLink.click()
    await page.waitForURL(/\/cruise\/crews\/[^/]+/, { timeout: 10_000 })

    const tabs = ["Overview", "Members", "Network", "Runtime", "Journal", "Settings"]
    for (const label of tabs) {
      await expect(page.getByRole("tab", { name: label })).toBeVisible({ timeout: 5_000 })
    }
  })

  test("workspace sub-strip switches between Files and Terminal", async ({ page }) => {
    await page.goto("/cruise")
    await page.waitForLoadState("networkidle")
    const agentLink = page.locator("a[href^='/cruise/agents/']").filter({ hasNotText: "new" }).first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents")
      return
    }
    const href = await agentLink.getAttribute("href")
    if (!href) return
    await page.goto(`${href}/workspace`)
    await expect(page.getByRole("tab", { name: "Files" })).toBeVisible({ timeout: 5_000 })
    await expect(page.getByRole("tab", { name: "Terminal" })).toBeVisible()
    await page.getByRole("tab", { name: "Terminal" }).click()
    await page.waitForURL(/pane=terminal/, { timeout: 5_000 })
  })

  test("tools sub-strip has Skills Credentials MCP", async ({ page }) => {
    await page.goto("/cruise")
    await page.waitForLoadState("networkidle")
    const agentLink = page.locator("a[href^='/cruise/agents/']").filter({ hasNotText: "new" }).first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents")
      return
    }
    const href = await agentLink.getAttribute("href")
    if (!href) return
    await page.goto(`${href}/tools`)
    await expect(page.getByRole("tab", { name: "Skills" })).toBeVisible({ timeout: 5_000 })
    await expect(page.getByRole("tab", { name: "Credentials" })).toBeVisible()
    await expect(page.getByRole("tab", { name: "MCP" })).toBeVisible()
  })
})
