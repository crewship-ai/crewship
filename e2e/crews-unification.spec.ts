import { test, expect } from "./fixtures/auth"

test.describe("Crews Unification", () => {
  test("legacy /agents redirects to /crews/agents", async ({ page }) => {
    await page.goto("/agents")
    await page.waitForURL(/\/crews\/agents/, { timeout: 10_000 })
    expect(page.url()).toContain("/crews/agents")
  })

  test("legacy /fleet redirects to /crews", async ({ page }) => {
    await page.goto("/fleet")
    await page.waitForURL(/\/crews(\?|$)/, { timeout: 10_000 })
    expect(page.url()).toMatch(/\/crews(\?|$)/)
  })

  test("/crews renders explorer and 3 top-level tabs (exact so Bottom Drawer labels don't match)", async ({ page }) => {
    await page.goto("/crews")
    await page.waitForLoadState("networkidle")
    await expect(page.getByRole("button", { name: "Overview", exact: true })).toBeVisible({ timeout: 10_000 })
    await expect(page.getByRole("button", { name: "Activity", exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: "Health", exact: true })).toBeVisible()
    await expect(page.getByRole("button", { name: "Connections", exact: true })).toHaveCount(0)
  })

  test("clicking a crew card navigates to the full crew page", async ({ page }) => {
    await page.goto("/crews")
    await page.waitForLoadState("networkidle")
    // AllCrewsOverview renders crew cards as <button>s; click now drills
    // into /crews/<id> rather than setting a preview query param, because
    // crew config (network, runtime, containers, MCP, avatar) warrants the
    // full tabbed page.
    const crewItem = page.locator("button").filter({ hasText: /Research|DevOps|Quality|Engineering/ }).first()
    if (await crewItem.count() === 0) {
      test.skip(true, "no seeded crews in dev workspace")
      return
    }
    await crewItem.click()
    await page.waitForURL(/\/crews\/[a-zA-Z0-9]+(\?|$)/, { timeout: 5_000 })
    expect(page.url()).toMatch(/\/crews\/[a-zA-Z0-9]+/)
  })

  test("agent full page has 7-tab rail", async ({ page }) => {
    // Go to /crews/agents list where agent cards are real <a> links.
    await page.goto("/crews/agents")
    await page.waitForLoadState("networkidle")
    const agentLink = page
      .locator("a[href^='/crews/agents/']:not([href$='/new']):not([href*='/agents/new'])")
      .first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents in dev workspace")
      return
    }
    await agentLink.click()
    await page.waitForURL(/\/crews\/agents\/[^/]+/, { timeout: 10_000 })

    const labels = ["Overview", "Sessions", "Runs", "Workspace", "Tools", "Logs", "Settings"]
    for (const label of labels) {
      await expect(page.locator(`a[title='${label}'], a:has-text('${label}')`).first()).toBeVisible({ timeout: 5_000 })
    }
  })

  test("crew detail renders 6 tabs", async ({ page }) => {
    // Resolve crew ID directly via API — /crews shell renders crew cards as
    // <button>s, not <a> links, so an href-based selector would miss them
    // (or pick an agent sub-link, since /crews/agents also starts with /crews/).
    const wsId = await page.evaluate(async () => {
      const r = await fetch("/api/v1/workspaces")
      const d = await r.json()
      return Array.isArray(d) ? d[0]?.id : d.id
    })
    const crews = await page.request.get(`/api/v1/crews?workspace_id=${wsId}`).then((r) => r.json())
    if (!Array.isArray(crews) || crews.length === 0) {
      test.skip(true, "no seeded crews")
      return
    }
    await page.goto(`/crews/${crews[0].id}`)

    const tabs = ["Overview", "Members", "Network", "Runtime", "Journal", "Settings"]
    for (const label of tabs) {
      await expect(page.getByRole("tab", { name: label })).toBeVisible({ timeout: 5_000 })
    }
  })

  test("workspace sub-strip switches between Files and Terminal", async ({ page }) => {
    await page.goto("/crews/agents")
    await page.waitForLoadState("networkidle")
    const agentLink = page
      .locator("a[href^='/crews/agents/']:not([href$='/new']):not([href*='/agents/new'])")
      .first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents")
      return
    }
    const href = await agentLink.getAttribute("href")
    expect(href, "agent card link is missing href — selector regressed").toBeTruthy()
    await page.goto(`${href!}/workspace`)
    await expect(page.getByRole("tab", { name: "Files" })).toBeVisible({ timeout: 5_000 })
    await expect(page.getByRole("tab", { name: "Terminal" })).toBeVisible()
    await page.getByRole("tab", { name: "Terminal" }).click()
    await page.waitForURL(/pane=terminal/, { timeout: 5_000 })
  })

  test("tools sub-strip has Skills Credentials MCP", async ({ page }) => {
    await page.goto("/crews/agents")
    await page.waitForLoadState("networkidle")
    const agentLink = page
      .locator("a[href^='/crews/agents/']:not([href$='/new']):not([href*='/agents/new'])")
      .first()
    if (await agentLink.count() === 0) {
      test.skip(true, "no seeded agents")
      return
    }
    const href = await agentLink.getAttribute("href")
    expect(href, "agent card link is missing href — selector regressed").toBeTruthy()
    await page.goto(`${href!}/tools`)
    await expect(page.getByRole("tab", { name: "Skills" })).toBeVisible({ timeout: 5_000 })
    await expect(page.getByRole("tab", { name: "Credentials" })).toBeVisible()
    await expect(page.getByRole("tab", { name: "MCP" })).toBeVisible()
  })
})
