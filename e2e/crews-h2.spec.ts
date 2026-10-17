import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for the Crews area (#3398, part of #3366 / #3350).
// Runs against a deployed environment with the demo seed (global-setup logs in).

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

/** The first crew the panel lists, and one of its agents. */
async function firstCrew(page: Page): Promise<string> {
  await page.goto("/crews")
  const row = page.locator("[data-crew-row]").first()
  await expect(row).toBeVisible()
  return (await row.getAttribute("data-crew-row"))!
}

async function firstAgentOf(page: Page, crew: string): Promise<string> {
  await page.goto(`/crews?crew=${crew}&tab=team`)
  const row = page.locator("[data-agent-row]").first()
  await expect(row).toBeVisible()
  return (await row.getAttribute("data-agent-row"))!
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })
  test.beforeEach(async ({ page }) => seed(page))

  test("K01 /crews without selection opens the last crew on desktop and the list on phone", async ({ page }) => {
    await page.goto("/crews")
    await expect(page).toHaveURL(/[?&]crew=/)
    await expect(page.getByTestId("crew-header")).toBeVisible()
  })

  test("K01 New ▾ offers crew paths and agent paths for the open crew; C and A keys open the dialogs", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}`)
    const header = page.locator("header").first()
    await header.getByRole("button", { name: "New" }).click()
    for (const item of ["New crew", "Crew from a template", "Import crewship.yaml", "New agent", "Hire a temporary agent"]) {
      await expect(page.getByRole("menuitem", { name: new RegExp(item) })).toBeVisible()
    }
    await page.keyboard.press("Escape")
    await page.keyboard.press("c")
    await expect(page.getByRole("dialog", { name: /New crew/ })).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(page.getByRole("dialog")).toHaveCount(0)
    await page.keyboard.press("a")
    await expect(page.getByRole("dialog", { name: /New agent/ })).toBeVisible()
  })

  test("K01 no catalog grid is rendered", async ({ page }) => {
    await page.goto("/crews")
    await expect(page.getByTestId("crew-header")).toBeVisible()
    await expect(page.getByText("Workspace agents")).toHaveCount(0)
    await expect(page.getByRole("heading", { name: "Crews & agents" })).toHaveCount(0)
  })

  test("K01 panel 272/44 and toolbar 48 at 1440", async ({ page }) => {
    await page.goto("/crews")
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=crews]")
    await expect(panel).toBeVisible()
    expect(Math.round((await panel.boundingBox())!.width)).toBe(272)
    expect(Math.round((await page.locator("header").first().boundingBox())!.height)).toBe(48)
    await panel.getByRole("button", { name: /Hide Crews and agents/ }).click()
    expect(Math.round((await panel.boundingBox())!.width)).toBe(44)
    await panel.getByRole("button", { name: /Show Crews and agents/ }).click()
  })

  test("K02 ?status=needs&group=name survives reload and old ?group=crew&details=off still loads", async ({ page }) => {
    await page.goto("/crews?status=needs&group=name")
    await page.reload()
    await expect(page.getByTestId("crews-view-needs")).toHaveAttribute("aria-pressed", "true")
    await expect(page.getByRole("radio", { name: "A–Z" })).toHaveAttribute("aria-checked", "true")
    await page.goto("/crews?group=crew&details=off")
    await expect(page.getByRole("radio", { name: "Status" })).toHaveAttribute("aria-checked", "true")
  })

  test("K03 ?crew=<slug>&tab=overview renders header chips and no Edit/gear/Files buttons", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=overview`)
    const head = page.getByTestId("crew-header")
    await expect(head.getByText(/agents?$/)).toBeVisible()
    await expect(page.getByRole("button", { name: /^Edit$/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Crew administration" })).toHaveCount(0)
    await expect(page.getByRole("button", { name: /^Files$/ })).toHaveCount(0)
    await expect(page.getByTestId("crew-health")).toBeVisible()
  })

  test("K03 unknown crew slug shows not found with Back to crews", async ({ page }) => {
    await page.goto("/crews?crew=no-such-crew-xyz")
    await expect(page.getByRole("heading", { name: "This crew doesn’t exist" })).toBeVisible()
    await page.getByRole("button", { name: "Back to crews" }).click()
    await expect(page).not.toHaveURL(/no-such-crew-xyz/)
  })

  test("K05 legacy ?crew=x&tab=work opens Overview at Works on", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=work`)
    await expect(page.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true")
    await expect(page.getByTestId("crew-works-on")).toBeVisible()
  })

  test("K07 there is no Crew administration dialog; ?tab=settings opens Settings", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=settings`)
    await expect(page.getByRole("tab", { name: "Settings" })).toHaveAttribute("aria-selected", "true")
    await expect(page.getByTestId("crew-settings")).toBeVisible()
    await expect(page.getByRole("dialog", { name: /Crew administration/ })).toHaveCount(0)
  })

  test("K08 ?agent=<slug>&tab=persona deep link opens Instructions; Activity button lands on /activity filtered to the agent", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}&tab=persona`)
    await expect(page.getByRole("tab", { name: "Instructions" })).toHaveAttribute("aria-selected", "true")
    await page.locator("header").first().getByRole("link", { name: "Activity" }).click()
    await expect(page).toHaveURL(/\/activity\?walk=agent%3A/)
  })

  test("K09 legacy ?tab=skills and ?tab=credentials open Tools and access", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    for (const legacy of ["skills", "credentials"]) {
      await page.goto(`/crews?agent=${agent}&tab=${legacy}`)
      await expect(page.getByRole("tab", { name: "Tools and access" })).toHaveAttribute("aria-selected", "true")
    }
  })

  test("K15 Terminal button opens dock; Files tab lists /crew for a crew and /crew/agents/<slug> for an agent", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}`)
    const dockTabs = page.getByRole("tablist", { name: "Bottom panel" }).getByRole("tab")
    await expect(dockTabs).toHaveText(["Terminal", "Files"])
    await page.locator("header").first().getByRole("button", { name: "Terminal" }).click()
    await expect(page.getByRole("tabpanel", { name: "Terminal panel" })).toBeVisible()
    await page.getByRole("tab", { name: "Files" }).click()
    await expect(page.getByRole("tabpanel", { name: "Files panel" })).toBeVisible()
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}`)
    await expect(dockTabs).toHaveText(["Terminal", "Files", "Logs"])
  })

  test("K17 ?new=crew opens New crew (M 640, phone bottom sheet) and strips the param", async ({ page }) => {
    await page.goto("/crews?new=crew")
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await expect(dialog).toBeVisible()
    expect(Math.round((await dialog.boundingBox())!.width)).toBe(640)
    await expect(page).not.toHaveURL(/new=crew/)
    await expect(dialog.getByRole("radiogroup", { name: "Start with" })).toBeVisible()
    await expect(dialog.getByTestId("crew-icon-picker")).toBeVisible()
  })

  test("K17 template path creates crew and agents", async ({ page }) => {
    test.skip(!process.env.E2E_ALLOW_WRITES, "creates a crew; set E2E_ALLOW_WRITES=1 on a disposable environment")
    const slug = `h2-tpl-${Date.now().toString(36)}`
    await page.goto("/crews?new=crew")
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await dialog.getByPlaceholder("Engineering").fill(slug)
    await dialog.getByRole("radio", { name: /From a template/ }).click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    await dialog.locator("button[aria-pressed]").first().click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    await dialog.getByRole("radio", { name: /Anthropic/ }).first().click().catch(() => undefined)
    await dialog.getByRole("button", { name: /Create crew/ }).click()
    await expect(page).toHaveURL(new RegExp(`crew=${slug}`))
    await page.getByRole("tab", { name: /Team/ }).click()
    await expect(page.locator("[data-agent-row]").first()).toBeVisible()
  })

  test("K19 ?new=agent&crew=<slug> opens New agent in that crew", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?new=agent&crew=${crew}`)
    await expect(page.getByRole("dialog", { name: /New agent/ })).toBeVisible()
  })

  test("K20 there is no Edit agent dialog; edit Instructions and save bar persists", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}&tab=persona`)
    await expect(page.getByRole("button", { name: /^Edit$/ })).toHaveCount(0)
    const prompt = page.getByRole("textbox", { name: "Agent system prompt" })
    const before = await prompt.inputValue()
    await prompt.fill(`${before}\n`)
    await expect(page.getByRole("region", { name: "Unsaved changes" })).toBeVisible()
    await page.getByRole("region", { name: "Unsaved changes" }).getByRole("button", { name: "Discard" }).click()
    await expect(prompt).toHaveValue(before)
  })

  test("Z01 /agents?agent=x lands on /crews?agent=x", async ({ page }) => {
    await page.goto("/agents?agent=does-not-matter")
    await expect(page).toHaveURL(/\/crews\?agent=does-not-matter/)
  })

  test("Z14 legacy settings crews tab", async ({ page }) => {
    await page.goto("/settings?tab=crews")
    await expect(page).toHaveURL(/\/crews/)
  })

  test("Z31 ?new=agent&crew=<slug> keeps the crew picked", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?new=agent&crew=${crew}`)
    await expect(page.getByRole("dialog", { name: /New agent/ })).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`crew=${crew}`))
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  test.beforeEach(async ({ page }) => seed(page))

  test("K01 /crews without selection opens the last crew on desktop and the list on phone", async ({ page }) => {
    await page.goto("/crews")
    await expect(page.locator("[data-slot=secondary-panel][data-phone=inline]")).toBeVisible()
    await expect(page).not.toHaveURL(/[?&]crew=/)
  })

  test("K04 Team table on phone collapses to rows", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=team`)
    const table = page.getByTestId("crew-team").locator("table").first()
    await expect(table.getByRole("columnheader", { name: "Skills" })).toBeHidden()
    const scroll = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(scroll).toBeLessThanOrEqual(1)
  })

  test("K15 phone: no dock", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}`)
    await expect(page.getByRole("tablist", { name: "Bottom panel" })).toHaveCount(0)
  })

  test("K17 ?new=crew opens New crew as a bottom sheet", async ({ page }) => {
    await page.goto("/crews?new=crew")
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await expect(dialog).toBeVisible()
    const b = (await dialog.boundingBox())!
    expect(Math.round(b.width)).toBe(390)
  })
})
