import { test, expect } from "./fixtures/auth"

const SCREENSHOT_OPTS = {
  animations: "disabled" as const,
  mask: [] as any[],
}

async function getFirstAgentHref(page: any): Promise<string> {
  await expect(page.getByText("All Agents")).toBeVisible({ timeout: 15_000 })
  const card = page.locator("a[href^='/agents/']").filter({ hasText: /Idle|Running|Stopped|Error/ }).first()
  await expect(card).toBeVisible({ timeout: 10_000 })
  return await card.getAttribute("href")
}

test.describe("Visual Regression", () => {
  test("dashboard", async ({ page }) => {
    await page.goto("/")
    await expect(page.getByText("All Agents")).toBeVisible({ timeout: 15_000 })
    // Wait for agent cards to load
    await expect(page.locator("a[href^='/agents/']").filter({ hasText: /Idle|Running|Stopped|Error/ }).first()).toBeVisible({ timeout: 10_000 })
    await expect(page).toHaveScreenshot("dashboard.png", {
      ...SCREENSHOT_OPTS,
      fullPage: true,
    })
  })

  test("agent overview", async ({ page }) => {
    const href = await getFirstAgentHref(page)
    await page.goto(href)
    await expect(page.getByText("CLI Adapter")).toBeVisible({ timeout: 10_000 })
    await expect(page).toHaveScreenshot("agent-overview.png", SCREENSHOT_OPTS)
  })

  test("agent settings - runtime section", async ({ page }) => {
    const href = await getFirstAgentHref(page)
    await page.goto(`${href}/settings`)
    await expect(page.getByRole("button", { name: /Claude Code/ })).toBeVisible({ timeout: 10_000 })
    await page.getByText("Runtime", { exact: true }).scrollIntoViewIfNeeded()
    await expect(page).toHaveScreenshot("agent-settings-runtime.png", SCREENSHOT_OPTS)
  })

  test("agent credentials", async ({ page }) => {
    const href = await getFirstAgentHref(page)
    await page.goto(`${href}/credentials`)
    await expect(page.getByText("AES-256-GCM encrypted")).toBeVisible({ timeout: 10_000 })
    await expect(page).toHaveScreenshot("agent-credentials.png", SCREENSHOT_OPTS)
  })

  test("new agent page", async ({ page }) => {
    await page.goto("/fleet/agents/new")
    await expect(page.getByRole("button", { name: /Claude Code/ })).toBeVisible({ timeout: 10_000 })
    await expect(page).toHaveScreenshot("new-agent.png", {
      ...SCREENSHOT_OPTS,
      fullPage: true,
    })
  })
})
