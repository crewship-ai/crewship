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

/** CRW-03: Tools and access → Add skill opens the shared Give to picker for the agent. */
async function openGiveSkills(page: Page) {
  const crew = await firstCrew(page)
  const agent = await firstAgentOf(page, crew)
  await page.goto(`/crews?agent=${agent}&tab=tools`)
  await page.getByRole("button", { name: "Add skill" }).first().click()
  const dialog = page.getByRole("dialog", { name: /Give skills to/ })
  await expect(dialog).toBeVisible()
  await expect(dialog.locator('[data-slot="give-to-picker"]')).toBeVisible()
  await expect(dialog.getByRole("textbox", { name: "Search skills" })).toBeVisible()
  await expect(dialog.getByText(/\d+ added · \d+ removed/)).toBeVisible()
  await expect(dialog.getByRole("button", { name: "Apply" })).toBeDisabled()
  return dialog
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
    const activity = page.locator("header").first().getByRole("link", { name: "Activity" })
    // The link names the agent by id; the Activity page may rewrite it to its
    // own canonical filter (?agent=<id>), which must still be this agent.
    const href = (await activity.getAttribute("href"))!
    const id = decodeURIComponent(/walk=agent%3A([^&]+)/.exec(href)?.[1] ?? "")
    expect(id).not.toBe("")
    await activity.click()
    await expect(page).toHaveURL(new RegExp(`/activity\\?(?:.*&)?(?:agent=${id}|walk=agent%3A${id})(?:&|$)`))
  })

  test("K09 legacy ?tab=skills and ?tab=credentials open Tools and access", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    for (const legacy of ["skills", "credentials"]) {
      await page.goto(`/crews?agent=${agent}&tab=${legacy}`)
      await expect(page.getByRole("tab", { name: "Tools and access" })).toHaveAttribute("aria-selected", "true")
    }
  })

  test("CRW-03 Add skill opens the shared Give to picker; Apply stays disabled until a change", async ({ page }) => {
    const dialog = await openGiveSkills(page)
    const first = dialog.getByRole("checkbox").first()
    await first.click()
    await expect(dialog.getByRole("button", { name: "Apply" })).toBeEnabled()
    await first.click()
    await expect(dialog.getByRole("button", { name: "Apply" })).toBeDisabled()
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
    // offsetWidth, not the bounding box: the box is measured mid zoom-in
    // (scale 0.95 → 1) and reads 636–638 px while the surface is 640 wide.
    expect(await dialog.evaluate((el) => (el as HTMLElement).offsetWidth)).toBe(640)
    await expect(page).not.toHaveURL(/new=crew/)
    await expect(dialog.getByRole("radiogroup", { name: "Start with" })).toBeVisible()
    await expect(dialog.getByTestId("crew-icon-picker")).toBeVisible()
  })

  test("K17 template path creates crew and agents", async ({ page }) => {
    test.skip(!process.env.E2E_ALLOW_WRITES, "creates a crew; set E2E_ALLOW_WRITES=1 on a disposable environment")
    const slug = `h2-tpl-${Date.now().toString(36)}`
    await page.goto("/crews?new=crew")
    const dialog = page.getByRole("dialog", { name: /New crew/ })
    await dialog.getByRole("textbox", { name: "Name" }).fill(slug)
    await dialog.getByRole("radio", { name: /From a template/ }).click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    // A template tile, not the "Start empty" / "Import YAML" tiles above the
    // grid: a template names its agent count, and it is never zero.
    await dialog.locator("button[aria-pressed]").filter({ hasText: /[1-9]\d* agents/ }).first().click()
    await dialog.getByRole("button", { name: "Continue" }).click()
    // A provider choice shows only when the template asks for one; a click
    // on a missing radio would wait out the whole test timeout.
    const provider = dialog.getByRole("radio", { name: /Anthropic/ })
    if (await provider.count()) await provider.first().click()
    await dialog.getByRole("button", { name: /Create crew/ }).click()
    await expect(page).toHaveURL(new RegExp(`crew=${slug}`))
    // The canvas opens the crew just created, not the one /crews had opened.
    await expect(page.getByTestId("crew-header").getByRole("heading", { level: 1 })).toHaveText(slug)
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

  // ── Follow-up #3424 ────────────────────────────────────────────────────

  test("K19 New agent is four steps (Who → Model → Access → Instructions) at M 640; Back keeps the name", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?new=agent&crew=${crew}`)
    const dialog = page.getByRole("dialog", { name: /New agent in/ })
    await expect(dialog).toBeVisible()
    expect(await dialog.evaluate((el) => (el as HTMLElement).offsetWidth)).toBe(640)
    const steps = dialog.getByRole("navigation", { name: "Steps" }).getByRole("listitem")
    await expect(steps).toHaveText([/Who/, /Model/, /Access/, /Instructions/])
    // The footer's counter (a phone-only copy sits in the footer lead, hidden here).
    await expect(dialog.getByTestId("new-agent-step")).toHaveText("Step 1 of 4")
    await expect(dialog.getByRole("radiogroup", { name: "Start from" })).toBeVisible()
    await dialog.getByPlaceholder("Filip", { exact: true }).fill("H2 wizard probe")
    await dialog.getByRole("button", { name: "Continue" }).click()
    await expect(dialog.getByTestId("new-agent-step")).toHaveText("Step 2 of 4")
    await dialog.getByRole("button", { name: "Back" }).click()
    await expect(dialog.getByPlaceholder("Filip", { exact: true })).toHaveValue("H2 wizard probe")
    await dialog.getByRole("button", { name: "Step 3: Access" }).click()
    await expect(dialog.getByText("Pays with").first()).toBeVisible()
  })

  test("K18 Environment cards follow the segment; Rebuild offers a cached and a clean build", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=env`)
    const env = page.getByTestId("crew-environment")
    for (const card of ["Container", "Image", "Tools and versions", "Features", "Network", "Resources", "Preview links", "Security", "Folders in the container"]) {
      await expect(env.getByRole("region", { name: card, exact: true })).toBeVisible()
    }
    await env.getByRole("radio", { name: "Tools" }).click()
    await expect(env.getByRole("region", { name: "Container", exact: true })).toHaveCount(0)
    await expect(env.getByRole("region", { name: "Tools and versions" })).toBeVisible()
    await env.getByRole("radio", { name: "Container and image" }).click()
    const rebuild = env.getByRole("button", { name: /Rebuild…/ })
    if (await rebuild.count()) {
      await rebuild.click()
      const group = page.getByRole("radiogroup", { name: "How to build" })
      await expect(group.getByRole("radio")).toHaveCount(2)
      await page.keyboard.press("Escape")
    }
  })

  test("K07 crew Settings no longer carries the runtime and security blocks", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=settings`)
    const settings = page.getByTestId("crew-settings")
    await expect(settings).toBeVisible()
    for (const gone of ["Runtime & security", "Container resources", "Network policy", "Container image & features", "MCP servers"]) {
      await expect(settings.getByText(gone, { exact: true })).toHaveCount(0)
    }
    await expect(settings.getByRole("heading", { name: "Escalations" })).toBeVisible()
  })

  test("K06 crew Memory shows CREW.md and every agent's AGENT.md; no review queue", async ({ page }) => {
    const crew = await firstCrew(page)
    await page.goto(`/crews?crew=${crew}&tab=memory`)
    await expect(page.getByRole("region", { name: "CREW.md · how this crew works" })).toBeVisible()
    await expect(page.getByRole("region", { name: "Shared notes and agent knowledge" })).toBeVisible()
    await expect(page.getByText(/Waiting for review/)).toHaveCount(0)
  })

  test("K10 agent Memory: Remember between runs, what it knows about you, AGENT.md", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}&tab=memory`)
    await expect(page.getByRole("switch", { name: "Remember between runs" })).toBeVisible()
    await expect(page.getByRole("region", { name: "What it knows about you" })).toBeVisible()
    await expect(page.getByRole("region", { name: "AGENT.md" })).toBeVisible()
  })

  test("K20 Instructions: source and History, crew default; no Suggest improvements", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}&tab=persona`)
    const source = page.getByTestId("agent-instructions-source")
    await expect(source).toBeVisible()
    await source.getByRole("button", { name: "History" }).click()
    await expect(page.getByRole("region", { name: /^Crew default · / })).toBeVisible()
    await expect(page.getByRole("button", { name: /Suggest improvements/ })).toHaveCount(0)
  })

  test("K08 agent Overview has Messages from teammates", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}`)
    await expect(page.getByTestId("agent-teammate-messages")).toBeVisible()
  })

  test("KIT-04 a tab switch with unsaved inline edits asks first; Stay keeps them, Discard drops them", async ({ page }) => {
    const crew = await firstCrew(page)
    const agent = await firstAgentOf(page, crew)
    await page.goto(`/crews?agent=${agent}&crew=${crew}&tab=persona`)
    const prompt = page.getByRole("textbox", { name: "Agent system prompt" })
    const before = await prompt.inputValue()
    await prompt.fill(`${before}\nprobe`)
    await page.getByRole("tab", { name: "Overview" }).click()
    const ask = page.getByRole("alertdialog")
    await expect(ask.getByText(/Leave with 1 unsaved change/)).toBeVisible()
    await ask.getByRole("button", { name: "Stay" }).click()
    await expect(page.getByRole("tab", { name: "Instructions" })).toHaveAttribute("aria-selected", "true")
    await expect(prompt).toHaveValue(`${before}\nprobe`)
    await page.getByRole("tab", { name: "Overview" }).click()
    await page.getByRole("alertdialog").getByRole("button", { name: "Discard" }).click()
    await expect(page.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true")
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

test.describe("tablet", () => {
  test.use({ viewport: { width: 1024, height: 768 } })
  test.beforeEach(async ({ page }) => seed(page))

  test("CRW-03 tablet 1024: Add skill opens the shared Give to picker as a centred 480 dialog", async ({ page }) => {
    const dialog = await openGiveSkills(page)
    const w = await dialog.evaluate((el) => (el as HTMLElement).offsetWidth)
    expect(w).toBeLessThanOrEqual(480)
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
    expect(await dialog.evaluate((el) => (el as HTMLElement).offsetWidth)).toBe(390)
  })

  test("CRW-03 phone 390: Add skill opens the shared Give to picker as a full-width bottom sheet with 44 px rows", async ({ page }) => {
    const dialog = await openGiveSkills(page)
    expect(await dialog.evaluate((el) => (el as HTMLElement).offsetWidth)).toBe(390)
    const row = dialog.locator("label").filter({ has: page.getByRole("checkbox") }).first()
    if (await row.count()) expect((await row.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  })
})
