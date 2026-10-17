import { test, expect, type Page } from "@playwright/test"

// /crews on a phone (Harbor 2, #3398): the panel is the page while nothing is
// open, a crew or an agent is a full page with its tabs in one scrolling
// strip, the only top-bar action is Chat, and nothing scrolls sideways.
// Uses the session global-setup stores.

test.use({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true, hasTouch: true })

async function noSideScroll(page: Page) {
  const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(over, "horizontal overflow").toBeLessThanOrEqual(1)
}

async function firstCrew(page: Page): Promise<string | null> {
  await page.goto("/crews")
  const row = page.locator("[data-crew-row]").first()
  if (!(await row.waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false))) return null
  return row.getAttribute("data-crew-row")
}

test("mobile /crews: the list is the page, no horizontal scroll", async ({ page }) => {
  await page.goto("/crews")
  await expect(page.locator("[data-slot=secondary-panel][data-phone=inline]")).toBeVisible()
  await expect(page.getByPlaceholder("Search crews and agents…")).toBeVisible()
  await noSideScroll(page)
})

test("mobile: tapping a crew opens its page with the six tabs in a scrolling strip", async ({ page }) => {
  const crew = await firstCrew(page)
  test.skip(!crew, "no seeded crews")
  await page.locator(`[data-crew-row="${crew}"] [role=button]`).first().click()
  await expect(page).toHaveURL(new RegExp(`crew=${crew}`))
  await expect(page.getByTestId("crew-header")).toBeVisible()
  for (const tab of ["Overview", "Team", "Tools and access", "Environment", "Memory", "Settings"]) {
    await expect(page.getByRole("tab", { name: new RegExp(`^${tab}`) })).toHaveCount(1)
  }
  await noSideScroll(page)
})

test("mobile agent page: Chat is the only action, content fits", async ({ page }) => {
  const crew = await firstCrew(page)
  test.skip(!crew, "no seeded crews")
  await page.goto(`/crews?crew=${crew}&tab=team`)
  const row = page.locator("[data-agent-row]").first()
  test.skip(!(await row.waitFor({ state: "visible", timeout: 10_000 }).then(() => true, () => false)), "crew has no agents")
  const agent = (await row.getAttribute("data-agent-row"))!
  await page.goto(`/crews?agent=${agent}&crew=${crew}`)
  await expect(page.getByTestId("agent-header")).toBeVisible()
  await expect(page.getByRole("link", { name: /^Chat with / })).toBeVisible()
  await expect(page.getByRole("link", { name: "Activity" })).toHaveCount(0)
  await noSideScroll(page)
})
