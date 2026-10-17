import { test, expect } from "./fixtures/auth"
import type { Page } from "@playwright/test"

// /crews is the one place crews and agents live (Harbor 2, #3398): the old
// /agents entry point redirects here keeping its query, a crew and an agent
// are pages of /crews selected by ?crew= / ?agent=, each with its tab set,
// and the dock under them is the crew container's Terminal and Files.

test.use({ viewport: { width: 1440, height: 1000 } })

async function firstCrew(page: Page): Promise<string | null> {
  await page.goto("/crews")
  const row = page.locator("[data-crew-row]").first()
  if (!(await row.isVisible({ timeout: 10_000 }).catch(() => false))) return null
  return row.getAttribute("data-crew-row")
}

test.describe("Crews unification", () => {
  test("legacy /agents redirects to /crews", async ({ page }) => {
    await page.goto("/agents")
    await page.waitForURL(/\/crews(\?|$)/, { timeout: 10_000 })
  })

  test("legacy /agents?agent=<slug> keeps the agent", async ({ page }) => {
    await page.goto("/agents?agent=some-agent")
    await page.waitForURL(/\/crews\?agent=some-agent/, { timeout: 10_000 })
  })

  test("/crews shows the panel and a crew page, not a catalogue", async ({ page }) => {
    const crew = await firstCrew(page)
    test.skip(!crew, "no seeded crews")
    await expect(page.locator("[data-slot=secondary-panel][data-panel-id=crews]")).toBeVisible()
    await expect(page).toHaveURL(/[?&]crew=/)
    await expect(page.getByTestId("crew-header")).toBeVisible()
  })

  test("clicking a crew in the panel opens it on Overview", async ({ page }) => {
    const crew = await firstCrew(page)
    test.skip(!crew, "no seeded crews")
    const rows = page.locator("[data-crew-row]")
    const target = (await rows.count()) > 1 ? rows.nth(1) : rows.first()
    const slug = (await target.getAttribute("data-crew-row"))!
    await target.locator("[role=button]").first().click()
    await expect(page).toHaveURL(new RegExp(`crew=${slug}`))
    await expect(page.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true")
  })

  test("crew page renders its six tabs", async ({ page }) => {
    const crew = await firstCrew(page)
    test.skip(!crew, "no seeded crews")
    await page.goto(`/crews?crew=${crew}`)
    for (const label of ["Overview", "Team", "Tools and access", "Environment", "Memory", "Settings"]) {
      await expect(page.getByRole("tab", { name: new RegExp(`^${label}`) })).toBeVisible()
    }
  })

  test("agent page renders its six tabs and a way back to its crew", async ({ page }) => {
    const crew = await firstCrew(page)
    test.skip(!crew, "no seeded crews")
    await page.goto(`/crews?crew=${crew}&tab=team`)
    const row = page.locator("[data-agent-row]").first()
    test.skip(!(await row.isVisible({ timeout: 10_000 }).catch(() => false)), "crew has no agents")
    await row.click()
    await expect(page).toHaveURL(/[?&]agent=/)
    for (const label of ["Overview", "Instructions", "Model and limits", "Tools and access", "Memory", "Settings"]) {
      await expect(page.getByRole("tab", { name: label })).toBeVisible()
    }
    await page.getByTestId("agent-header").locator("xpath=preceding-sibling::button[1]").click()
    await expect(page).toHaveURL(new RegExp(`crew=${crew}`))
    await expect(page).not.toHaveURL(/[?&]agent=/)
  })

  test("the dock is the crew container's Terminal and Files", async ({ page }) => {
    const crew = await firstCrew(page)
    test.skip(!crew, "no seeded crews")
    await page.goto(`/crews?crew=${crew}`)
    await expect(page.getByRole("tablist", { name: "Bottom panel" }).getByRole("tab")).toHaveText(["Terminal", "Files"])
  })
})
