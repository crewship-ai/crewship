import AxeBuilder from "@axe-core/playwright"
import type { Page } from "@playwright/test"
import { test, expect } from "./fixtures/auth"

// Full wcag2a + wcag2aa scan — no rules disabled. The historic
// exclusions (select-name, color-contrast, label, button-name) were
// removed after fixing the underlying violations:
// - color-contrast: --primary-foreground is now the background navy
//   (5.22:1 on #1E7BFE; white was 3.95:1), brand-tinted chips use
//   text-primary-hover (≥4.95:1 on bg-primary/15–20), and the old
//   text-muted-foreground/40–/70 alpha pattern (1.74–3.21:1) was
//   replaced on every scanned surface with text-muted-foreground
//   (5.64:1 bg / 5.36:1 card) or the dedicated --muted-foreground-soft
//   token (4.99:1 bg / 4.74:1 card).
// - select-name / label: native selects, inputs and textareas carry
//   aria-label or an explicit <label htmlFor> association.
// - button-name: icon-only buttons carry aria-label.
//
// NOTE: agent creation is no longer a routed page — the old
// /crews/agents/new tree was deleted with the selection-driven canvas
// redesign and now 404s. The surface lives in CreateAgentDialog on
// /crews; the dedicated test below opens it via [data-crews-add-agent].
const PAGES = [
  { name: "Dashboard", path: "/" },
  { name: "Crews", path: "/crews" },
  { name: "Routines", path: "/routines" },
  { name: "Credentials", path: "/credentials" },
  { name: "Settings", path: "/settings" },
  { name: "Admin", path: "/admin" },
]

async function scan(page: Page, testInfo: import("@playwright/test").TestInfo) {
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

// Resolve a real agent slug via the API — agent-bound routes are
// dynamic. Returns null on an empty workspace so callers can skip
// instead of hard-failing (parity between the agent-overview and chat
// tests; previously only chat skipped).
async function resolveAgentSlug(page: Page): Promise<string | null> {
  return page.evaluate(async () => {
    const ws = await fetch("/api/v1/workspaces").then((r) => r.json())
    const list = Array.isArray(ws) ? ws : [ws]
    const wsId = list[0]?.id
    if (!wsId) return null
    const agents = await fetch(`/api/v1/agents?workspace_id=${wsId}`).then((r) => r.json())
    return Array.isArray(agents) && agents.length > 0 ? (agents[0].slug as string) : null
  })
}

for (const { name, path } of PAGES) {
  test(`${name} page has no critical a11y violations`, async ({ page }, testInfo) => {
    await page.goto(path)
    await page.waitForLoadState("networkidle")

    const critical = await scan(page, testInfo)
    expect(critical).toHaveLength(0)
  })
}

test("New Agent dialog has no critical a11y violations", async ({ page }, testInfo) => {
  await page.goto("/crews")
  await page.waitForLoadState("networkidle")

  // Subbar CTA opens CreateAgentDialog (works even with zero crews —
  // the crew picker just starts empty).
  const addAgent = page.locator("[data-crews-add-agent]")
  await expect(addAgent).toBeVisible({ timeout: 15_000 })
  await addAgent.click()
  await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10_000 })

  const critical = await scan(page, testInfo)
  expect(critical).toHaveLength(0)
})

test("Agent overview has no critical a11y violations", async ({ page }, testInfo) => {
  await page.goto("/")
  const slug = await resolveAgentSlug(page)
  test.skip(!slug, "no agents seeded")

  // Selection-driven canvas: /crews?agent=<slug> is the agent
  // overview surface (the old /agents/* card links no longer exist).
  await page.goto(`/crews?agent=${encodeURIComponent(slug as string)}`)
  await page.waitForLoadState("networkidle")

  const critical = await scan(page, testInfo)
  expect(critical).toHaveLength(0)
})

test("Chat page has no critical a11y violations", async ({ page }, testInfo) => {
  await page.goto("/")
  const slug = await resolveAgentSlug(page)
  test.skip(!slug, "no agents seeded")

  await page.goto(`/chat/${encodeURIComponent(slug as string)}`)
  await page.waitForLoadState("networkidle")

  const critical = await scan(page, testInfo)
  expect(critical).toHaveLength(0)
})
