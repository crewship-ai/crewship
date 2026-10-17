import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Integrations (#3395): four spaces in the panel, the
// lists, the object pages at ?id= and every old ?tab=&section= link, measured
// in a real browser against a seeded workspace.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=integrations]")
const h1 = (page: Page) => page.locator("h1:visible").first()

for (const [label, width, height] of [["1440", 1440, 1000], ["1024", 1024, 800], ["390", 390, 844]] as const) {
  test.describe(`at ${label}`, () => {
    test.use({ viewport: { width, height } })

    test(`N01 channels at ${label}`, async ({ page }) => {
      await seed(page)
      await page.goto("/integrations?space=channels")
      await expect(h1(page)).toHaveText("Channels")
      // A table on desktop and tablet, rows on a phone; never a sideways scroll.
      if (width >= 768) await expect(panel(page)).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
      await expect(page.getByRole("button", { name: "Add channel" })).toBeVisible()
    })

    test(`N09 tools at ${label}`, async ({ page }) => {
      await seed(page)
      await page.goto("/integrations")
      await expect(h1(page)).toHaveText(/Tools|Catalog/)
      await expect(page.getByRole("button", { name: "Connect an app" })).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
    })
  })
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("N05 triggers at 1440", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=triggers")
    await expect(h1(page)).toHaveText("Triggers")
    await expect(page.getByRole("button", { name: "New trigger" }).first()).toBeVisible()
    await expect(page.getByText("Kept for replay")).toHaveCount(0)
    await expect(page.getByText("App events")).toHaveCount(0)
  })

  test("panel shows the four spaces and 1–4 switch them", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations")
    const spaces = panel(page).getByRole("tablist", { name: "Spaces" })
    await expect(spaces.getByRole("tab")).toHaveText([/Tools/, /Triggers/, /Channels/, /Webhooks/])
    await page.locator("body").press("3")
    await expect(h1(page)).toHaveText("Channels")
    await page.locator("body").press("4")
    await expect(h1(page)).toHaveText("Webhooks")
    const toolbar = await panel(page).locator(".secondary-panel-toolbar").boundingBox()
    expect(Math.round(toolbar!.height)).toBe(48)
  })

  test("N02 ?id= opens the channel page; reload keeps it", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=channels")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "the seeded workspace has no channel")
    await first.click()
    await expect(page).toHaveURL(/[?&]id=/)
    const name = await h1(page).innerText()
    await page.reload()
    await expect(h1(page)).toHaveText(name)
    for (const tab of ["Overview", "Events", "Deliveries", "Settings"]) await expect(page.getByRole("tab", { name: tab })).toBeVisible()
    await expect(page.getByRole("button", { name: /Send again/ })).toHaveCount(0)
  })

  test("N01 MEMBER sees workspace channels read-only", async ({ browser }) => {
    const email = process.env.E2E_MEMBER_EMAIL
    test.skip(!email, "set E2E_MEMBER_EMAIL / E2E_MEMBER_PASSWORD for a MEMBER account")
    const ctx = await browser.newContext()
    const page = await ctx.newPage()
    await page.goto("/login")
    await page.getByLabel(/email/i).fill(email!)
    await page.getByLabel(/password/i).fill(process.env.E2E_MEMBER_PASSWORD ?? "")
    await page.getByRole("button", { name: /sign in|log in/i }).click()
    await page.goto("/integrations?space=channels")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "no channel to open")
    await first.click()
    await page.getByRole("tab", { name: "Settings" }).click()
    await expect(page.getByRole("switch", { name: "Paused" })).toBeDisabled()
    await expect(page.getByRole("button", { name: "Add channel" })).toBeEnabled()
    await ctx.close()
  })

  test("Z12 lands on Channels", async ({ page }) => {
    await seed(page)
    await page.goto("/settings?tab=notifications")
    await expect(page).toHaveURL(/space=channels/)
    await expect(h1(page)).toHaveText("Channels")
  })

  test("Z13 /settings?tab=notification-prefs lands on My notifications", async ({ page }) => {
    await seed(page)
    await page.goto("/settings?tab=notification-prefs")
    await expect(page).toHaveURL(/space=channels/)
    await expect(page).toHaveURL(/view=preferences/)
    await expect(h1(page)).toHaveText("My notifications")
  })

  test("N06 ?tab=incoming&section=routine&target=<id> opens filtered triggers", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=incoming&section=routine&target=nope")
    await expect(page).toHaveURL(/space=triggers/)
    await expect(page).toHaveURL(/starts=routine/)
    await expect(h1(page)).toHaveText("Triggers of routines")
  })

  for (const [section, title] of [
    ["catalog", "Catalog"],
    ["accounts", /Tools|Catalog/],
    ["agents", /Tools|Catalog/],
    ["tools", /Tools|Catalog/],
    ["triggers", /Tools|Catalog/],
    ["mcp", "MCP endpoints"],
    ["crew-tools", "Crew MCP servers"],
  ] as const) {
    test(`legacy tools sections: ${section}`, async ({ page }) => {
      await seed(page)
      await page.goto(`/integrations?tab=tools&section=${section}`)
      await expect(page).toHaveURL(/space=tools/)
      await expect(h1(page)).toHaveText(title)
    })
  }

  test("N12 old ?section=tools lands on the app list", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=tools")
    await expect(page).toHaveURL(/space=tools/)
  })

  test("N14 ?section=mcp lands on Tools without error", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=mcp")
    await expect(h1(page)).toHaveText("MCP endpoints")
    await expect(page.locator("[data-state=error]")).toHaveCount(0)
  })

  test("N15 ?section=crew-tools&server=<id> keeps the server", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?tab=tools&section=crew-tools&server=srv_none")
    await expect(page).toHaveURL(/server=srv_none/)
    await expect(h1(page)).toHaveText("Crew MCP servers")
  })

  test("N10 ?id= opens the app account", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=tools")
    const first = page.locator("[data-slot=integrations-table] tbody tr").first()
    test.skip((await first.count()) === 0, "no Composio account connected")
    await first.click()
    await expect(page).toHaveURL(/[?&]id=/)
    for (const tab of ["Overview", "Access", "Tools", "Triggers", "Settings"]) await expect(page.getByRole("tab", { name: tab })).toBeVisible()
  })

  test("N16 Add channel is three steps; Add webhook starts at step two", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations?space=channels")
    await page.getByRole("button", { name: "Add channel" }).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog.getByText("Where it posts")).toBeVisible()
    await expect(dialog.getByText("What it sends")).toBeVisible()
    await dialog.getByRole("radio", { name: /Email/ }).click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    await expect(dialog.getByLabel("Email address")).toBeVisible()
    await page.keyboard.press("Escape")
    await page.goto("/integrations?space=webhooks")
    await page.getByRole("button", { name: "Add webhook" }).click()
    await expect(page.getByRole("dialog").getByLabel("URL")).toBeVisible()
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test("spaces are a segment above the content", async ({ page }) => {
    await seed(page)
    await page.goto("/integrations")
    const seg = page.getByRole("tablist", { name: "Spaces" })
    await seg.getByRole("tab", { name: "Webhooks" }).click()
    await expect(h1(page)).toHaveText("Webhooks")
  })
})
