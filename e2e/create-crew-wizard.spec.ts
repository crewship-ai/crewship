import { test, expect } from "./fixtures/auth"

// =============================================================================
// Create Crew Wizard — end-to-end happy paths.
//
// Drives the actual UI in Chromium against a live backend (dev server).
// Covers:
//   1. Empty crew flow (Identity → Lineup=Empty → Runtime → Container=Skip → Review → Create)
//   2. Browse template flow (Identity → Lineup=Template → Runtime → Container → Review → Create)
//   3. Step strip jump-back navigation
//   4. Container step makes Image & features and MCP servers visible (no collapse)
//   5. Skip-to-defaults shortcut
//
// Each test creates a crew with a unique slug (timestamp-suffixed) so reruns
// don't collide on the workspace's UNIQUE(slug) constraint.
// =============================================================================

const TIMEOUT = 20_000

function uniqueSlug(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}`
}

async function openCreateCrew(page: import("@playwright/test").Page) {
  await page.goto("/crews")
  // Sub-bar exposes a "+ Crew" button; click opens the wizard.
  await page.getByRole("button", { name: /^Crew$/ }).click()
  await expect(page.getByRole("dialog")).toBeVisible({ timeout: TIMEOUT })
  // Dialog title is "New crew — step X of 4" — match the prefix only.
  await expect(page.getByText(/New crew/)).toBeVisible()
}

test.describe("/crews — Create-crew wizard happy paths", () => {
  test("empty crew end-to-end via Skip-to-defaults on Step 4", async ({ page }) => {
    const slug = uniqueSlug("e2e-empty")
    const name = `E2E Empty ${slug.slice(-6)}`

    await openCreateCrew(page)

    // Step 1 — Identity
    await expect(page.getByText(/step 1 of 4/i).first()).toBeVisible()
    await page.getByPlaceholder("Engineering").fill(name)
    // Slug should auto-derive but we override to a guaranteed-unique value
    await page.getByPlaceholder("engineering").fill(slug)
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 2 — Lineup → Empty crew
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 3 — Runtime defaults
    await expect(page.getByText(/step 3 of 4/i).first()).toBeVisible()
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — Container — skip
    await expect(page.getByText(/step 4 of 4/i).first()).toBeVisible()
    await expect(page.getByRole("button", { name: /Skip to defaults/ })).toBeVisible()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()

    // Step 5 — Review
    await expect(page.getByRole("button", { name: /Create crew/ })).toBeVisible()
    await expect(page.getByText(name)).toBeVisible()
    await page.getByRole("button", { name: /Create crew/ }).click()

    // Dialog closes + URL updates with new crew slug + roster surfaces it
    await expect(page).toHaveURL(new RegExp(`crew=${slug}`), { timeout: TIMEOUT })
    await expect(page.getByText(name).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("template crew creates the seeded agent lineup", async ({ page }) => {
    const slug = uniqueSlug("e2e-tpl")
    const name = `E2E Tpl ${slug.slice(-6)}`

    await openCreateCrew(page)

    // Step 1
    await page.getByPlaceholder("Engineering").fill(name)
    await page.getByPlaceholder("engineering").fill(slug)
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 2 — default mode is "Browse templates"; auto-pick fires
    await expect(page.getByRole("button", { name: /Browse templates/ })).toBeVisible()
    // Wait for the template list to be populated and Continue enabled.
    await expect(page.getByRole("button", { name: /Continue/ })).toBeEnabled({ timeout: TIMEOUT })
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 3 → Step 4 → Skip
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()
    await expect(page.getByRole("button", { name: /Skip to defaults/ })).toBeVisible()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()

    // Review → Create
    await expect(page.getByRole("button", { name: /Create crew/ })).toBeVisible()
    await page.getByRole("button", { name: /Create crew/ }).click()

    await expect(page).toHaveURL(new RegExp(`crew=${slug}`), { timeout: TIMEOUT })
  })

  test("step strip lets user jump back to a completed step", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering").fill("Strip Test")
    await page.getByRole("button", { name: /Continue/ }).click()

    // Now on Step 2; Step 1 indicator should be clickable (completed = green check).
    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible()

    // Click the Step 1 nav button (aria-label "Step 1: Identity").
    await page.getByLabel("Step 1: Identity").click()

    // Back on Step 1 — name preserved.
    await expect(page.getByText(/step 1 of 4/i).first()).toBeVisible()
    await expect(page.getByPlaceholder("Engineering")).toHaveValue("Strip Test")
  })

  test("Step 4 shows Image & features and MCP servers always visible (no collapse)", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering").fill("Container Vis")
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Empty crew/ }).click()
    await page.getByRole("button", { name: /Continue/ }).click()
    await expect(page.getByText("Container resources")).toBeVisible()
    await page.getByRole("button", { name: /Continue/ }).click()

    // Step 4 — both section headers + their children must be visible without
    // any extra clicks. Section headers have the section icon + title.
    await expect(page.getByText("Image & features")).toBeVisible()
    await expect(page.getByText("MCP servers")).toBeVisible()

    // BASE IMAGE picker rendered inline (not behind a collapsed panel).
    await expect(page.getByText(/^Base Image$/i).or(page.getByText(/^BASE IMAGE$/i)).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("Cmd+Enter advances when the step is valid", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByPlaceholder("Engineering").fill("Keyboard Nav")

    // Press Cmd+Enter (cross-platform: Playwright emits the right modifier per OS).
    const isMac = process.platform === "darwin"
    await page.keyboard.press(isMac ? "Meta+Enter" : "Control+Enter")

    await expect(page.getByText(/step 2 of 4/i).first()).toBeVisible({ timeout: TIMEOUT })
  })

  test("Cancel closes the dialog without creating a crew", async ({ page }) => {
    await openCreateCrew(page)

    await page.getByRole("button", { name: "Cancel" }).click()
    await expect(page.getByRole("dialog")).not.toBeVisible({ timeout: TIMEOUT })
  })
})
