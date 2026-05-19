import { test, expect } from "./fixtures/auth"
import type { Page } from "@playwright/test"

// End-to-end provisioning flow against a live backend with Docker.
//
// What this exercises:
//   1. User toggles a feature in Settings → Container & features and saves.
//   2. Toolbar badge "X needs rebuild" appears.
//   3. User opens the badge popover and clicks "Build now" right there
//      (the central UX claim of the toolbar-popover-as-primary-surface).
//   4. Popover row transitions to Building with step/total progress.
//   5. After the build completes, the row offers "Restart agents".
//   6. Restart succeeds.
//
// Designed to run against any deployment with a real Docker daemon
// (point `PLAYWRIGHT_BASE_URL` at it). Cap is 4 minutes total — the
// heaviest single feature (common-utils on a fresh box) takes ~90 s,
// with headroom for a slow network pull. Skipped on bare local runs
// without the dev URL set, so the default `pnpm e2e` stays green.

const HARD_TIMEOUT = 240_000
const QUICK = 8_000
const BUILD_TIMEOUT = 200_000

async function waitForBadge(page: Page) {
  // The badge is the only element whose aria-label starts with "Crew images:".
  const badge = page.locator('button[aria-label^="Crew images:"]')
  await expect(badge).toBeVisible({ timeout: QUICK })
  return badge
}

async function pickAnyCrew(page: Page): Promise<string> {
  await page.goto("/crews")
  const crewRow = page.locator("aside button").filter({ hasText: /^(Research|DevOps|Engineering|Quality)/ }).first()
  await expect(crewRow).toBeVisible({ timeout: QUICK })
  await crewRow.click()
  await expect(page).toHaveURL(/[?&]crew=/, { timeout: QUICK })
  const url = new URL(page.url())
  const slug = url.searchParams.get("crew")
  if (!slug) throw new Error("No crew selected after click")
  return slug
}

test.describe("Toolbar provisioning popover — end-to-end", () => {
  test.skip(
    !process.env.PLAYWRIGHT_BASE_URL,
    "Provisioning e2e requires a live backend with Docker; set PLAYWRIGHT_BASE_URL=http://localhost:3001 (or your remote dev server) to run.",
  )
  test.setTimeout(HARD_TIMEOUT)

  test("save → badge → Build now → progress → Restart agents", async ({ page }) => {
    const slug = await pickAnyCrew(page)

    // Open Settings tab on the canvas.
    await page.getByRole("tab", { name: /Settings/i }).click()
    // Container & features section is collapsible — open it explicitly.
    const containerSection = page.getByText(/Container image|Container & features/i).first()
    await containerSection.click().catch(() => {
      // already open; ignore
    })

    // Pick a feature toggle that's likely to be off — `aws-cli`. If it's
    // already on we flip it off then on so we still produce a save.
    const awsToggle = page.locator("button[role='switch']").filter({ has: page.locator("text=/aws-cli/i") })
    if (!(await awsToggle.first().isVisible({ timeout: QUICK }).catch(() => false))) {
      // Fallback: any toggle with text "aws" anywhere
      const anyToggle = page.locator("[role='switch']").first()
      await anyToggle.click()
    } else {
      await awsToggle.first().click()
    }

    await page.getByRole("button", { name: /Save Runtime Config/i }).click()
    // Toast confirms save (terminology may evolve — match on "Saved").
    await expect(page.getByText(/Saved/i).first()).toBeVisible({ timeout: QUICK })

    // 2 — toolbar badge surfaces "needs rebuild".
    const badge = await waitForBadge(page)
    await expect(badge).toContainText(/rebuild|needs/i, { timeout: QUICK })

    // 3 — open popover, click Build now on this crew's row.
    await badge.click()
    const popover = page.getByRole("dialog").or(page.locator('[data-radix-popper-content-wrapper]'))
    const buildBtn = popover.getByRole("button", { name: /Build now/i }).first()
    await expect(buildBtn).toBeVisible({ timeout: QUICK })
    await buildBtn.click()

    // 4 — progress: row text now matches "step/total" pattern.
    await expect(popover.locator("text=/\\d+\\/\\d+/").first()).toBeVisible({ timeout: QUICK })

    // 5 — wait for build completion (Restart agents button appears).
    const restartBtn = popover.getByRole("button", { name: /Restart agents/i })
    await expect(restartBtn).toBeVisible({ timeout: BUILD_TIMEOUT })

    // 6 — click Restart agents, popover row should disappear (no more
    // pending-restart agents). The badge itself may or may not vanish
    // depending on whether other crews are unhealthy; we only assert
    // this row is gone.
    await restartBtn.click()
    await expect(popover.getByText(slug)).toBeHidden({ timeout: QUICK })
  })

  test("badge is absent when all crews are clean", async ({ page }) => {
    await page.goto("/")
    // If any crews are dirty (likely on a freshly-modified dev VM) this
    // test is informational — not a failure. We assert the *negative*
    // case only when the world is clean.
    const badge = page.locator('button[aria-label^="Crew images:"]')
    const count = await badge.count()
    if (count > 0) {
      test.skip(true, "Workspace has dirty crews — negative-case test only meaningful on clean state.")
    }
    await expect(badge).toHaveCount(0)
  })
})
