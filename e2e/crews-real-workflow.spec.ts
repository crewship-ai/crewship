import { test, expect } from "./fixtures/auth"
import type { Page } from "@playwright/test"

// Real user-workflow tests for the Harbor 2 /crews surface.
//
// These walk through the flows that broke during testing on the dev VM
// (avatar selection, crew icon, chat entry, the crew's environment, the
// create dialogs) and pin them as regression checks. They run against
// whatever URL the PLAYWRIGHT_BASE_URL env points to, reusing the e2e auth
// fixture so tests never log in twice.
//
// The pre-Harbor 2 version clicked rows in an <aside> explorer, a "+ Crew" /
// "+ Agent" sub-bar and Status: / Role: chips. The panel now lists crews with
// the open crew's agents under it, New ▾ is the one create action, and the
// facets were removed with v67 (ODCH-CR-02); Views replace them.

const TIMEOUT = 20_000
// The demo seed's crew with agents; pr-contract.spec.ts relies on the same slug.
const CREW = "engineering"

async function pickFirstAgent(page: Page): Promise<string> {
  await page.goto(`/crews?crew=${CREW}`)
  const agentRow = page.locator(`[data-crew-row="${CREW}"] > [role=button] ~ [role=button]`).first()
  await expect(agentRow).toBeVisible({ timeout: TIMEOUT })
  await agentRow.click()
  await expect(page).toHaveURL(/[?&]agent=/, { timeout: TIMEOUT })
  const slug = new URL(page.url()).searchParams.get("agent")
  if (!slug) throw new Error("No agent selected after click")
  return slug
}

async function pickFirstCrew(page: Page): Promise<string> {
  await page.goto("/crews")
  const crewRow = page.locator("[data-crew-row] > [role=button]").first()
  await expect(crewRow).toBeVisible({ timeout: TIMEOUT })
  await crewRow.click()
  await expect(page).toHaveURL(/[?&]crew=/, { timeout: TIMEOUT })
  const slug = new URL(page.url()).searchParams.get("crew")
  if (!slug) throw new Error("No crew selected after click")
  await expect(page.getByTestId("crew-header")).toBeVisible({ timeout: TIMEOUT })
  return slug
}

test.describe("Avatar picker workflow", () => {
  test("clicking the avatar opens the picker and Save avatar persists the seed", async ({ page }) => {
    const slug = await pickFirstAgent(page)

    await page.getByTestId("agent-header").getByRole("button", { name: "Change avatar" }).click()
    const dialog = page.getByRole("dialog", { name: /Avatar/ })
    await expect(dialog).toBeVisible()

    // A quick pick is deterministic: its seed is in its name.
    const pick = dialog.getByTestId("avatar-quick-pick").getByRole("button").first()
    const seed = (await pick.getAttribute("aria-label"))!.replace("Use avatar seed ", "")
    await pick.click()
    await expect(dialog.getByRole("textbox", { name: "Avatar seed" })).toHaveValue(seed)
    await dialog.getByRole("button", { name: "Save avatar" }).click()
    await expect(dialog).toBeHidden({ timeout: TIMEOUT })

    // Reload: the saved seed is what the picker opens with.
    await page.goto(`/crews?agent=${slug}&crew=${CREW}`)
    await expect(page.getByText(/could not be loaded/)).toHaveCount(0)
    await page.getByTestId("agent-header").getByRole("button", { name: "Change avatar" }).click()
    await expect(page.getByRole("dialog", { name: /Avatar/ }).getByRole("textbox", { name: "Avatar seed" })).toHaveValue(seed)
  })
})

test.describe("Crew icon picker workflow", () => {
  test("clicking the crew icon opens the picker", async ({ page }) => {
    await pickFirstCrew(page)
    await page.getByTestId("crew-header").getByRole("button", { name: "Change icon and colour" }).click()
    const dialog = page.getByRole("dialog", { name: /Icon/ })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("radiogroup", { name: "Colour" })).toBeVisible()
    await expect(dialog.getByRole("radiogroup", { name: "Choices" })).toBeVisible()
  })

  test("searching icons narrows the grid", async ({ page }) => {
    await pickFirstCrew(page)
    await page.getByTestId("crew-header").getByRole("button", { name: "Change icon and colour" }).click()
    const dialog = page.getByRole("dialog", { name: /Icon/ })
    const choices = dialog.getByRole("radiogroup", { name: "Choices" }).getByRole("radio")
    await expect(choices.first()).toBeVisible()
    const all = await choices.count()
    await dialog.getByRole("textbox", { name: "Search icons…" }).fill("rocket")
    await expect(dialog.getByRole("radio", { name: /rocket/i })).toBeVisible()
    await expect.poll(() => choices.count()).toBeLessThan(all)
  })
})

test.describe("Chat session workflow", () => {
  test("clicking Chat opens /chat/[slug] and renders chat shell (not dashboard)", async ({ page }) => {
    const slug = await pickFirstAgent(page)
    await page.getByRole("link", { name: /^Chat with / }).click()
    await expect(page).toHaveURL(new RegExp(`/chat/${slug}`), { timeout: TIMEOUT })

    // Assert chat-specific UI (not the dashboard's "AGENTS" stat card).
    await expect(page.getByPlaceholder(/Search sessions/)).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByText(/^AGENTS$/, { exact: true })).toHaveCount(0)
    await expect(page.getByText(/^ACTIVE MISSIONS$/, { exact: true })).toHaveCount(0)
  })

  test("Back link returns to /crews?agent=<slug>", async ({ page }) => {
    const slug = await pickFirstAgent(page)
    await page.getByRole("link", { name: /^Chat with / }).click()
    await expect(page).toHaveURL(new RegExp(`/chat/${slug}`), { timeout: TIMEOUT })

    await page.getByTitle("Back to agent canvas").click()
    await expect(page).toHaveURL(new RegExp(`agent=${slug}`), { timeout: TIMEOUT })
  })
})

// devcontainer.json and mise.toml are no longer raw editors on the crew page:
// Environment shows the image and the tools, and the generated files belong to
// the Add a tool / Add a feature catalogue. Escalation routing moved to Settings.
test.describe("Crew environment and settings workflow", () => {
  test("Environment shows the container image and its tools", async ({ page }) => {
    const crew = await pickFirstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=env`)
    const env = page.getByTestId("crew-environment")
    await expect(env).toBeVisible({ timeout: TIMEOUT })
    await expect(env.getByRole("region", { name: "Image", exact: true })).toBeVisible()
    await expect(env.getByRole("region", { name: "Tools and versions", exact: true })).toBeVisible()
  })

  test("escalation routing is on the crew's Settings", async ({ page }) => {
    const crew = await pickFirstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=settings`)
    const settings = page.getByTestId("crew-settings")
    await expect(settings).toBeVisible({ timeout: TIMEOUT })
    await expect(settings.getByRole("heading", { name: "Escalations" })).toBeVisible()
  })
})

test.describe("New ▾ create dialogs", () => {
  test("New crew offers the four ways to start", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByRole("button", { name: "New", exact: true }).click()
    await page.getByRole("menuitem", { name: /^New crew/ }).click()
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await expect(dialog).toBeVisible()
    const start = dialog.getByRole("radiogroup", { name: "Start with" })
    for (const way of ["Suggest a team", "From a template", "Import crewship.yaml", "Empty crew"]) {
      await expect(start.getByRole("radio", { name: new RegExp(`^${way}`) })).toBeVisible()
    }
  })

  test("New agent opens in the open crew at step Who", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByRole("button", { name: "New", exact: true }).click()
    await page.getByRole("menuitem", { name: /^New agent/ }).click()
    const dialog = page.getByRole("dialog", { name: /New agent in / })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("navigation", { name: "Steps" }).getByRole("listitem")).toHaveText([/Who/, /Model/, /Access/, /Instructions/])
    await expect(dialog.getByPlaceholder("Filip", { exact: true })).toBeVisible()
  })
})

test.describe("Panel views", () => {
  test("Needs you narrows the panel and is kept in ?status=needs", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}`)
    await page.getByTestId("crews-view-needs").click()
    await expect(page).toHaveURL(/[?&]status=needs/)
    await expect(page.getByTestId("crews-view-needs")).toHaveAttribute("aria-pressed", "true")
  })

  test("an old Status: Running / Role: Lead link still loads, on All crews", async ({ page }) => {
    await page.goto(`/crews?crew=${CREW}&status=RUNNING&role=LEAD`)
    await expect(page.getByTestId("crew-header")).toBeVisible({ timeout: TIMEOUT })
    await expect(page.getByTestId("crews-view-all")).toHaveAttribute("aria-pressed", "true")
  })
})
