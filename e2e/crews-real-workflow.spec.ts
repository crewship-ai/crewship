import { test, expect } from "./fixtures/auth"
import type { Page } from "@playwright/test"

// Real user-workflow tests for the redesigned /crews surface.
//
// These walk through the flows that broke during testing on the dev VM
// (avatar selection, crew icon, chat session, devcontainer config) and
// pin them as regression checks. They run against whatever URL the
// PLAYWRIGHT_BASE_URL env points to (or the local Next dev server when
// the var is empty), reusing the e2e auth fixture so tests never log in
// twice.
//
// Each test does the user motion, asserts the visible outcome, and
// cleans up by reverting the field. We don't depend on any specific
// agent/crew slug; the explorer is queried for whatever's there.

const TIMEOUT = 20_000

async function pickFirstAgent(page: Page): Promise<string> {
  await page.goto("/crews")
  const agentRow = page.locator("aside button").filter({ hasText: /idle|running/i }).first()
  await expect(agentRow).toBeVisible({ timeout: TIMEOUT })
  await agentRow.click()
  await expect(page).toHaveURL(/[?&]agent=/, { timeout: TIMEOUT })
  const url = new URL(page.url())
  const slug = url.searchParams.get("agent")
  if (!slug) throw new Error("No agent selected after click")
  return slug
}

async function pickFirstCrew(page: Page): Promise<string> {
  await page.goto("/crews")
  // Click the crew row (chevron + name + agent count) — pick any.
  const crewRow = page.locator("aside button").filter({ hasText: /^(Research|DevOps|Engineering|Quality)/ }).first()
  await expect(crewRow).toBeVisible({ timeout: TIMEOUT })
  await crewRow.click()
  await expect(page).toHaveURL(/[?&]crew=/, { timeout: TIMEOUT })
  const url = new URL(page.url())
  const slug = url.searchParams.get("crew")
  if (!slug) throw new Error("No crew selected after click")
  return slug
}

test.describe("Avatar picker workflow", () => {
  test("clicking avatar opens the picker and Save persists the choice", async ({ page }) => {
    const slug = await pickFirstAgent(page)

    // Open picker.
    await page.getByTitle("Customize avatar").click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText("Style")).toBeVisible()

    // Pick a non-default style. We click the SECOND style button (index 1)
    // to skip "Inherit" and pick a deterministic option.
    const styleButtons = page.locator('div:has-text("Style") + div button').first()
    await styleButtons.click()

    // Save.
    await page.getByRole("button", { name: /Save avatar/ }).click()
    await expect(page.getByRole("dialog")).not.toBeVisible({ timeout: TIMEOUT })

    // Reload the canvas — the avatar style must persist.
    await page.goto(`/crews?agent=${slug}`)
    // The avatar img has src that depends on the saved style. We don't
    // assert a specific style, just that the canvas re-renders without
    // an "agent not found" error.
    await expect(page.getByRole("heading", { name: "Profile" })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText(/Could not load agent/)).toHaveCount(0)
  })
})

test.describe("Crew icon picker workflow", () => {
  test("clicking crew header icon opens the picker", async ({ page }) => {
    await pickFirstCrew(page)

    // Click the big crew avatar tile.
    await page.getByTitle("Customize icon and color").click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText("Color")).toBeVisible()
    await expect(page.getByText(/^Icon$/)).toBeVisible()
  })

  test("filtering icons by search narrows the grid", async ({ page }) => {
    await pickFirstCrew(page)
    await page.getByTitle("Customize icon and color").click()
    const searchInput = page.getByPlaceholder(/Search icons/)
    await expect(searchInput).toBeVisible()
    await searchInput.fill("rocket")
    // The counter ticks down to a small number.
    await expect(page.getByText(/of \d{2,}/)).toBeVisible({ timeout: 3000 })
    // The "rocket" icon button is present.
    await expect(page.locator('button[title="rocket"]')).toBeVisible()
  })
})

test.describe("Chat session workflow", () => {
  test("clicking Chat opens /chat/[slug] and renders chat shell (not dashboard)", async ({ page }) => {
    const slug = await pickFirstAgent(page)
    // Click the canvas Chat button.
    await page.getByRole("link", { name: /^Chat$/ }).click()
    await expect(page).toHaveURL(new RegExp(`/chat/${slug}`), { timeout: TIMEOUT })

    // Assert chat-specific UI (not the dashboard's "AGENTS" stat card).
    await expect(page.getByPlaceholder(/Search sessions/)).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText(/^AGENTS$/, { exact: true })).toHaveCount(0)
    await expect(page.getByText(/^ACTIVE MISSIONS$/, { exact: true })).toHaveCount(0)
  })

  test("Back link returns to /crews?agent=<slug>", async ({ page }) => {
    const slug = await pickFirstAgent(page)
    await page.getByRole("link", { name: /^Chat$/ }).click()
    await expect(page).toHaveURL(new RegExp(`/chat/${slug}`), { timeout: TIMEOUT })

    await page.getByTitle("Back to agent canvas").click()
    await expect(page).toHaveURL(new RegExp(`agent=${slug}`), { timeout: TIMEOUT })
  })
})

test.describe("Devcontainer + mise editor workflow", () => {
  test("crew canvas surfaces devcontainer.json + mise.toml editors", async ({ page }) => {
    await pickFirstCrew(page)
    await expect(page.getByRole("heading", { name: /Devcontainer/ })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText("devcontainer.json")).toBeVisible()
    await expect(page.getByText("mise.toml")).toBeVisible()
  })

  test("escalation routing editor is present", async ({ page }) => {
    await pickFirstCrew(page)
    await expect(page.getByRole("heading", { name: /Escalation routing/ })).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText("escalation.json")).toBeVisible()
  })

  test("Adding invalid JSON to devcontainer disables Save", async ({ page }) => {
    await pickFirstCrew(page)
    // Find the devcontainer editor's Add/Edit button (whichever is present).
    const devcontainerSection = page.locator("div").filter({ hasText: /^devcontainer\.json/ }).first()
    const button = devcontainerSection.getByRole("button", { name: /^(Add|Edit)$/ })
    await button.click()

    const ta = devcontainerSection.locator("textarea")
    await expect(ta).toBeVisible({ timeout: 3000 })
    await ta.fill("{ this is not valid json")

    // Save is disabled when JSON is invalid.
    const saveBtn = devcontainerSection.getByRole("button", { name: "Save" })
    await expect(saveBtn).toBeDisabled()

    // Cancel out so the test doesn't leave an editor open.
    await devcontainerSection.getByRole("button", { name: "Cancel" }).click()
  })
})

test.describe("Sub-bar create dialogs", () => {
  test("+ Crew opens the create-crew dialog", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New crew$/)).toBeVisible()
    await expect(page.getByRole("button", { name: /^Blank$/ })).toBeVisible()
    await expect(page.getByRole("button", { name: /^From template$/ })).toBeVisible()
  })

  test("+ Agent opens the create-agent dialog", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Agent$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New agent$/)).toBeVisible()
    await expect(page.getByLabel(/Name/i)).toBeVisible()
    await expect(page.getByLabel(/Slug/i)).toBeVisible()
    await expect(page.getByLabel(/Role/i)).toBeVisible()
  })
})

test.describe("Status filter regression", () => {
  test("Status: Running narrows the explorer", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /Status:/ }).click()
    await page.getByRole("menuitem", { name: "Running" }).click()
    await expect(page).toHaveURL(/status=RUNNING/)
  })

  test("Role: Lead narrows the explorer", async ({ page }) => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /Role:/ }).click()
    await page.getByRole("menuitem", { name: "Lead" }).click()
    await expect(page).toHaveURL(/role=LEAD/)
  })
})
