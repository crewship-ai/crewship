import { test, expect } from "./fixtures/auth"
import * as fs from "fs"

test.describe("Dashboard", () => {
  test("loads with agent card", async ({ page }) => {
    await expect(page.getByText("All Agents")).toBeVisible({ timeout: 15_000 })
    const agentCard = page.locator("a").filter({ hasText: "AGENT" }).first()
    await expect(agentCard).toBeVisible()
  })
})

test.describe("Agent Overview", () => {
  test("shows CLI adapter icon and label", async ({ page }) => {
    await page.locator("a[href*='/fleet/agents/']").first().click()
    await page.waitForURL("**/fleet/agents/**")
    await expect(page.getByText("CLI Adapter")).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText("Claude Code")).toBeVisible()
  })

  test("shows provider and model info", async ({ page }) => {
    await page.locator("a[href*='/fleet/agents/']").first().click()
    await page.waitForURL("**/fleet/agents/**")
    await expect(page.getByText("Provider")).toBeVisible({ timeout: 10_000 })
    await expect(page.getByText("Model", { exact: true }).first()).toBeVisible()
  })
})

test.describe("Agent Settings", () => {
  async function goToAgentSettings(page: any) {
    // Wait for dashboard to fully load with agent cards
    await expect(page.getByText("All Agents")).toBeVisible({ timeout: 15_000 })
    // Wait for an agent card to render (contains status badge like Idle/Running)
    const agentCard = page.locator("a[href^='/fleet/agents/']").filter({ hasText: /Idle|Running|Stopped|Error/ }).first()
    await expect(agentCard).toBeVisible({ timeout: 10_000 })
    const href = await agentCard.getAttribute("href")
    await page.goto(`${href}/settings`)
    await expect(page.locator("button:has-text('Claude Code')")).toBeVisible({ timeout: 10_000 })
  }

  test("has 4 CLI adapter cards", async ({ page }) => {
    await goToAgentSettings(page)
    await expect(page.locator("button:has-text('Claude Code')")).toBeVisible()
    await expect(page.locator("button:has-text('OpenCode')")).toBeVisible()
    await expect(page.locator("button:has-text('Codex CLI')")).toBeVisible()
    await expect(page.locator("button:has-text('Gemini CLI')")).toBeVisible()
  })

  test("Claude Code card is selected by default", async ({ page }) => {
    await goToAgentSettings(page)
    const claudeCard = page.locator("button:has-text('Claude Code')")
    await expect(claudeCard).toHaveClass(/border-primary/)
  })

  test("has model select dropdown", async ({ page }) => {
    await goToAgentSettings(page)
    await expect(page.getByRole("combobox").first()).toBeVisible()
  })

  test("switching adapter changes model options", async ({ page }) => {
    await goToAgentSettings(page)
    // Close any open dropdowns first
    await page.keyboard.press("Escape")
    await page.locator("button:has-text('Gemini CLI')").click()
    // Click the Model combobox (find by the "Model" label nearby)
    const modelSection = page.locator("text=Model").first().locator("..")
    await modelSection.getByRole("combobox").click()
    await expect(page.getByRole("option", { name: /gemini/i }).first()).toBeVisible({ timeout: 5_000 })
  })
})

test.describe("Agent Credentials", () => {
  test("shows provider icon in credentials table", async ({ page }) => {
    await page.locator("a[href*='/fleet/agents/']").first().click()
    await page.waitForURL("**/fleet/agents/**")
    await expect(page.getByText("CLI Adapter")).toBeVisible({ timeout: 10_000 })
    await page.locator("a[href*='/credentials']").first().click()
    await page.waitForURL("**/credentials")

    const credRow = page.locator("tr").filter({ hasText: "ANTHROPIC_API_KEY" })
    if (await credRow.isVisible()) {
      await expect(credRow.locator("svg").first()).toBeVisible()
    }
  })
})

test.describe("New Agent", () => {
  test("has adapter cards and model select", async ({ page }) => {
    await page.goto("/fleet/agents/new")
    await expect(page.locator("button:has-text('Claude Code')")).toBeVisible({ timeout: 10_000 })
    await expect(page.locator("button:has-text('OpenCode')")).toBeVisible()
    await expect(page.locator("button:has-text('Codex CLI')")).toBeVisible()
    await expect(page.locator("button:has-text('Gemini CLI')")).toBeVisible()
    await expect(page.getByText("Model", { exact: true }).first()).toBeVisible()
  })
})

test.describe("Server Health", () => {
  test("no 500 errors in Go server logs", async () => {
    const logPath = "/tmp/crewship-go.log"
    if (!fs.existsSync(logPath)) {
      test.skip()
      return
    }
    const logs = fs.readFileSync(logPath, "utf-8")
    const lines500 = logs.split("\n").filter((l) => l.includes('"status":500') || l.includes("panic"))
    expect(lines500).toHaveLength(0)
  })
})
