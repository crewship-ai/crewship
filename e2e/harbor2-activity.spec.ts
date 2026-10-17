import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Activity (#3389): one list of work and changes in the
// shared SecondaryPanel layout, at the three widths, with the Peek and the
// empty state. Runs against a deployed environment with seeded activity.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

async function visibleColumns(page: Page) {
  const head = page.getByRole("table", { name: "Activity" }).getByRole("columnheader")
  const names: string[] = []
  for (const h of await head.all()) if (await h.isVisible()) names.push((await h.textContent())?.trim() ?? "")
  return names.filter(Boolean)
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("1440 panel + table columns", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=activity]")
    await expect(panel).toBeVisible()
    expect(Math.round((await panel.boundingBox())!.width)).toBe(272)
    await expect(panel.getByRole("button", { name: /Everything/ })).toBeVisible()
    await expect(panel.getByRole("button", { name: /Needs a person/ })).toBeVisible()
    await expect(page.getByRole("group", { name: "Time range" })).toBeVisible()
    await expect(page.getByTestId("activity-summary")).toContainText("items")
    // Progress and Cost wait for the chain index to send them (see the PR).
    expect(await visibleColumns(page)).toEqual(["Work", "Agent", "Took", "Started"])
  })

  test("/activity?run=<id> opens Peek, Esc closes, URL drops run", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    const first = page.getByRole("table", { name: "Activity" }).locator("[data-row-id]").first()
    await first.click()
    const id = await first.getAttribute("data-row-id")
    await expect(page.locator("[data-slot=peek]")).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`run=${id}`))
    await page.reload()
    await expect(page.locator("[data-slot=peek]")).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(page.locator("[data-slot=peek]")).toBeHidden()
    await expect(page).not.toHaveURL(/run=/)
  })

  test("key 1–4 switch views", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByTestId("activity-summary")).toBeVisible()
    await page.keyboard.press("4")
    await expect(page.getByRole("heading", { level: 1, name: "Could not finish" })).toBeVisible()
    await expect(page).toHaveURL(/view=failed/)
    await page.keyboard.press("1")
    await expect(page.getByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
  })

  test("old links land on the list", async ({ page }) => {
    await seed(page)
    await page.goto("/activity?status=failed")
    await expect(page.getByRole("heading", { level: 1, name: "Could not finish" })).toBeVisible()
    await page.goto("/activity?lens=agents")
    await expect(page).toHaveURL(/group=who/)
  })

  test("empty workspace shows No activity yet", async ({ page }) => {
    await seed(page)
    await page.route("**/api/v1/chains?**", (r) => r.fulfill({ json: { chains: [], has_more: false, has_unrecorded_runs: false } }))
    await page.route("**/api/v1/journal?**", (r) => r.fulfill({ json: { entries: [], next_cursor: null } }))
    await page.goto("/activity")
    await expect(page.getByText("No activity yet")).toBeVisible()
    await expect(page.getByText("crewship activity")).toBeVisible()
  })
})

test.describe("tablet", () => {
  test.use({ viewport: { width: 1024, height: 800 } })

  // v67 also closes the panel at this width; that is the shared panel's call
  // (kit request in the PR), so only the columns are asserted here.
  test("1024 fewer columns", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByTestId("activity-summary")).toBeVisible()
    expect(await visibleColumns(page)).toEqual(["Work", "Agent", "Took"])
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("390 chips + list, Peek full screen", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByRole("button", { name: /Everything/ }).first()).toBeVisible()
    await expect(page.getByRole("button", { name: /Last 7 days/ })).toBeVisible()
    await expect(page.getByRole("group", { name: "Time range" })).toHaveCount(0)
    const row = page.getByRole("table", { name: "Activity" }).locator("[data-row-id]").first()
    await row.click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    const b = (await peek.boundingBox())!
    expect(Math.round(b.width)).toBe(390)
    // No horizontal page scroll.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})
