import { expect, type Page } from "@playwright/test"

/**
 * Kit acceptance for the inline facet block (COM-01, #3481; PRD PNL-02), for
 * the area specs that adopt it (Routines RT-01, Issues ISS-15, Activity
 * ACT-06, Vault VLT-12, Calendar CAL-03, Settings › Crew links SET-10):
 *
 *   await expectInlineFacets(page, "Filter routines")
 *
 * Opens the panel's Filter button and checks the block is IN the panel (not
 * a floating layer), every open facet lists at most six values plus
 * "All N…", and Escape closes it with focus back on Filter.
 */
export async function expectInlineFacets(page: Page, blockLabel: string) {
  const panel = page.locator("[data-slot=secondary-panel]").first()
  const trigger = panel.getByRole("button", { name: /^Filter/ }).first()
  await trigger.click()
  const block = panel.getByRole("group", { name: blockLabel })
  await expect(block).toBeVisible()
  await expect(trigger).toHaveAttribute("aria-expanded", "true")

  // Inside the panel's box, in the flow.
  const [p, b] = [await panel.boundingBox(), await block.boundingBox()]
  expect(b!.x).toBeGreaterThanOrEqual(p!.x)
  expect(b!.x + b!.width).toBeLessThanOrEqual(p!.x + p!.width + 1)
  expect(await block.evaluate((el) => getComputedStyle(el).position)).toBe("static")
  await expect(page.getByTestId("sidebar-filter-dismiss")).toHaveCount(0)

  // Open each facet; at most six values each.
  const headers = block.locator("[data-slot=facet] > div > button[aria-expanded]")
  for (let i = 0; i < (await headers.count()); i++) {
    const header = headers.nth(i)
    if ((await header.getAttribute("aria-expanded")) === "false") await header.click()
  }
  const facets = block.locator("[data-slot=facet] [role=group]")
  for (let i = 0; i < (await facets.count()); i++) {
    const values = facets.nth(i).locator("button[aria-pressed]")
    const all = facets.nth(i).getByRole("button", { name: /^All \d+…$/ })
    const n = await values.count()
    if ((await all.count()) > 0) expect(n).toBeLessThanOrEqual(7) // six, plus a chosen value past them
    else expect(n).toBeLessThanOrEqual(6)
  }

  await headers.first().press("Escape")
  await expect(block).toHaveCount(0)
  await expect(trigger).toBeFocused()
}
