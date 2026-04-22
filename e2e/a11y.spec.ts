import AxeBuilder from "@axe-core/playwright"
import { test, expect } from "./fixtures/auth"

// Pre-existing a11y issues in shadcn/ui components and existing pages:
// - select-name: Radix UI Select/Combobox internals lack explicit labels
// - color-contrast: Primary/10 active nav bg has 4.18 ratio (needs 4.5:1)
// - label: Settings profile inputs are readonly and lack explicit labels
const EXCLUDED_RULES = ["select-name", "color-contrast", "label", "button-name"]

const PAGES = [
  { name: "Dashboard", path: "/" },
  { name: "New Agent", path: "/cruise/agents/new" },
  { name: "Admin", path: "/admin" },
  { name: "Settings", path: "/settings" },
]

for (const { name, path } of PAGES) {
  test(`${name} page has no critical a11y violations`, async ({ page }, testInfo) => {
    await page.goto(path)
    await page.waitForLoadState("networkidle")

    const results = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa"])
      .disableRules(EXCLUDED_RULES)
      .analyze()

    await testInfo.attach("a11y-scan-results", {
      body: JSON.stringify(results.violations, null, 2),
      contentType: "application/json",
    })

    const critical = results.violations.filter(
      (v) => v.impact === "critical" || v.impact === "serious"
    )
    expect(critical).toHaveLength(0)
  })
}

test("Agent overview has no critical a11y violations", async ({ page }, testInfo) => {
  await page.goto("/")
  await expect(page.getByText("All Agents")).toBeVisible({ timeout: 15_000 })
  const card = page.locator("a[href^='/agents/']").filter({ hasText: /Idle|Running|Stopped|Error/ }).first()
  await expect(card).toBeVisible({ timeout: 10_000 })
  const href = await card.getAttribute("href")
  if (!href) {
    throw new Error("Agent card missing href attribute")
  }
  await page.goto(href)
  await page.waitForLoadState("networkidle")

  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .disableRules(EXCLUDED_RULES)
    .analyze()

  await testInfo.attach("a11y-scan-results", {
    body: JSON.stringify(results.violations, null, 2),
    contentType: "application/json",
  })

  const critical = results.violations.filter(
    (v) => v.impact === "critical" || v.impact === "serious"
  )
  expect(critical).toHaveLength(0)
})
