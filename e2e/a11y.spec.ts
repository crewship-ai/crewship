import AxeBuilder from "@axe-core/playwright"
import { test, expect } from "./fixtures/auth"

// Full wcag2a + wcag2aa scan — no rules disabled. The historic
// exclusions (select-name, color-contrast, label, button-name) were
// removed after fixing the underlying violations:
// - color-contrast: --primary-foreground is now the background navy
//   (5.22:1 on #1E7BFE; white was 3.95:1), and brand-tinted chips use
//   text-primary-hover (≥4.95:1 on bg-primary/15–20).
// - select-name / label: native selects, inputs and textareas carry
//   aria-label or an explicit <label htmlFor> association.
// - button-name: icon-only buttons carry aria-label.
const PAGES = [
  { name: "Dashboard", path: "/" },
  { name: "Crews", path: "/crews" },
  { name: "Routines", path: "/routines" },
  { name: "Credentials", path: "/credentials" },
  { name: "Settings", path: "/settings" },
  { name: "Admin", path: "/admin" },
  { name: "New Agent", path: "/crews/agents/new" },
]

async function scan(page: import("@playwright/test").Page, testInfo: import("@playwright/test").TestInfo) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa"])
    .analyze()

  await testInfo.attach("a11y-scan-results", {
    body: JSON.stringify(results.violations, null, 2),
    contentType: "application/json",
  })

  return results.violations.filter(
    (v) => v.impact === "critical" || v.impact === "serious"
  )
}

for (const { name, path } of PAGES) {
  test(`${name} page has no critical a11y violations`, async ({ page }, testInfo) => {
    await page.goto(path)
    await page.waitForLoadState("networkidle")

    const critical = await scan(page, testInfo)
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

  const critical = await scan(page, testInfo)
  expect(critical).toHaveLength(0)
})

test("Chat page has no critical a11y violations", async ({ page }, testInfo) => {
  // Resolve a real agent slug via the API — chat routes are dynamic.
  const slug = await page.evaluate(async () => {
    const ws = await fetch("/api/v1/workspaces").then((r) => r.json())
    const list = Array.isArray(ws) ? ws : [ws]
    const wsId = list[0]?.id
    if (!wsId) return null
    const agents = await fetch(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
    return Array.isArray(agents) && agents.length > 0 ? (agents[0].slug as string) : null
  })
  test.skip(!slug, "no agents seeded")

  await page.goto(`/chat/${encodeURIComponent(slug as string)}`)
  await page.waitForLoadState("networkidle")

  const critical = await scan(page, testInfo)
  expect(critical).toHaveLength(0)
})
