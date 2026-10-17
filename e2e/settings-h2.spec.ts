import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Settings (#3388): the card and row anatomy measured
// in a real browser, the phone list → section → back, the save bar on a typed
// edit, the destructive confirmations and the old ?tab= links.
//
// Run as an ADMIN or OWNER of a seeded workspace (global-setup signs in; the
// dev slots use admin-baseline, an ADMIN). The owner-only Danger zone is
// checked twice: hidden for the signed-in ADMIN, and, with the role the
// browser sees raised to OWNER, the confirmation itself (cancelled, so the
// server is never asked). Nothing here is saved: edits are discarded,
// dialogs are cancelled, a created token is revoked again.

// The card anatomy (32 px fields, 50 px card header) is specified at the
// Small text size; pin it so a device or account that stored Standard does
// not change the measurement. Sync off keeps the account's stored size out.
const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0", "crewship-text-size": "small" }

async function seed(page: Page, entries: Record<string, string>) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, entries)
}

/** Shows the signed-in user as the OWNER of every workspace, in the browser
 *  only, and collects the workspaces' slugs. */
async function asOwner(page: Page) {
  const slugs: string[] = []
  await page.route("**/api/v1/workspaces", async (route) => {
    if (route.request().method() !== "GET") return route.continue()
    const res = await route.fetch()
    const rows = (await res.json()) as { slug: string; currentUserRole: string }[]
    for (const r of rows) { r.currentUserRole = "OWNER"; slugs.push(r.slug) }
    await route.fulfill({ response: res, json: rows })
  })
  return slugs
}

async function height(page: Page, selector: string) {
  const b = await page.locator(selector).first().boundingBox()
  expect(b, selector).not.toBeNull()
  return Math.round(b!.height)
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  for (const width of [1440, 1024]) {
    test(`T01 profile at ${width}: section head, cards and rows`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await seed(page, PINNED)
      await page.goto("/settings?tab=profile")
      const head = page.locator("[data-slot=settings-page-head]")
      await expect(head.getByRole("heading", { level: 2, name: "Profile" })).toBeVisible()
      await expect(page.getByRole("region", { name: "Account" })).toBeVisible()
      await expect(page.getByRole("region", { name: "Workspace" })).toBeVisible()
    })
  }

  test("T06 card anatomy: 32 px fields, 10 px card radius, 50 px card header", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=general")
    const identity = page.getByRole("region", { name: "Identity" })
    await expect(identity).toBeVisible()
    await expect(identity).toHaveCSS("border-top-left-radius", "10px")
    expect(await height(page, "[data-slot=settings-card-header]")).toBeGreaterThanOrEqual(49)
    expect(await height(page, "[data-slot=settings-card-header]")).toBeLessThanOrEqual(52)
    expect(Math.round((await identity.getByLabel("Workspace name").boundingBox())!.height)).toBe(32)
  })

  test("T06 save bar appears on name edit, Discard restores", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=general")
    const name = page.getByLabel("Workspace name")
    const saved = await name.inputValue()
    await name.fill(`${saved} (edited)`)
    const bar = page.getByRole("region", { name: /unsaved changes/i })
    await expect(bar).toContainText("1 unsaved change")
    await bar.getByRole("button", { name: "Discard" }).click()
    await expect(name).toHaveValue(saved)
    await expect(bar).toBeHidden()
  })

  test("T06 the Danger zone is the owner's alone", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=general")
    await expect(page.getByRole("region", { name: "Privileged credentials" })).toBeVisible()
    await expect(page.getByRole("region", { name: "Danger zone" })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Delete workspace…" })).toHaveCount(0)
  })

  test("T06 delete workspace asks for the slug and can be cancelled", async ({ page }) => {
    await seed(page, PINNED)
    const slugs = await asOwner(page)
    await page.goto("/settings?tab=general")
    await page.getByRole("region", { name: "Danger zone" }).getByRole("button", { name: "Delete workspace…" }).click()
    const dialog = page.getByRole("alertdialog")
    const confirm = dialog.getByRole("button", { name: "Delete workspace" })
    await expect(confirm).toBeDisabled()
    const slug = (await dialog.locator("label code").textContent())!.trim()
    expect(slugs).toContain(slug)
    await dialog.getByLabel(/to confirm/).fill(`${slug}-not`)
    await expect(confirm).toBeDisabled()
    await dialog.getByLabel(/to confirm/).fill(slug)
    await expect(confirm).toBeEnabled()
    await dialog.getByRole("button", { name: "Cancel" }).click()
    await expect(dialog).toBeHidden()
  })

  test("T04 create token reveals once, revoke confirms and waits", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=sessions")
    const tokens = page.getByRole("region", { name: "CLI tokens" })
    await tokens.getByRole("button", { name: "New token" }).click()
    const name = `e2e-h2-${Date.now()}`
    await tokens.getByLabel("Token name").fill(name)
    await tokens.getByRole("button", { name: "Create token" }).click()
    await expect(tokens.locator("[data-slot=new-token]")).toContainText("won’t be shown again")
    await tokens.getByRole("button", { name: `Revoke ${name}` }).click()
    const dialog = page.getByRole("alertdialog")
    await dialog.getByRole("button", { name: "Revoke" }).click()
    await expect(dialog).toBeHidden()
    await expect(tokens.getByRole("button", { name: `Revoke ${name}` })).toBeHidden()
  })

  test("T05 ?tab=privacy lands on Profile", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=privacy")
    await expect(page.locator("[data-slot=settings-page-head]").getByRole("heading", { name: "Profile" })).toBeVisible()
  })

  test("T11 filters in the URL survive a reload", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/members?role=ADMIN")
    await expect(page.getByRole("button", { name: /Remove filter Admin/ })).toBeVisible()
    await page.reload()
    await expect(page.getByRole("button", { name: /Remove filter Admin/ })).toBeVisible()
  })

  test("T16 the journal chain is verified on request", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/audit")
    const banner = page.locator("[data-slot=journal-verify]")
    await expect(banner).toHaveAttribute("data-state", "idle")
    await banner.getByRole("button", { name: "Verify now" }).click()
    await expect(banner).toHaveAttribute("data-state", /ok|broken|error/)
  })

  const MOVED: [string, RegExp][] = [
    // Integrations rewrites its old ?tab=&section= links to ?space=&view=
    // once its Harbor 2 layout is in (#3400); either form is the same place.
    ["notifications", /\/integrations\?(tab=notifications&section=connections|space=channels(?!&view=)(&|$))/],
    ["notification-prefs", /\/integrations\?(tab=notifications&section=preferences|space=channels&view=preferences)/],
    ["crews", /\/crews/],
    ["connections", /\/settings\/crew-links/],
    ["members", /\/settings\/members/],
    ["audit", /\/settings\/audit/],
    ["hooks", /\/settings\/hooks/],
  ]
  for (const [tab, target] of MOVED) {
    test(`Z12–Z18 ?tab=${tab} moves on`, async ({ page }) => {
      await seed(page, PINNED)
      await page.goto(`/settings?tab=${tab}`)
      await expect(page).toHaveURL(target)
    })
  }
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("T01 /settings is the list; a section opens with Back to the list", async ({ page }) => {
    await page.goto("/settings")
    const list = page.locator("[data-slot=secondary-panel][data-phone=inline]")
    await expect(list.getByRole("button", { name: "Profile" })).toBeVisible()
    await list.getByRole("button", { name: "Profile" }).click()
    await expect(page.getByRole("region", { name: "Account" })).toBeVisible()
    await page.getByRole("button", { name: "Back to Settings" }).click()
    await expect(list.getByRole("button", { name: "General" })).toBeVisible()
  })

  // The last card an ADMIN sees on General, well below the fold at 390 x 844
  // (the Danger zone is owner-only and absent for the dev slots' ADMIN).
  test("T09 ?card= scrolls to and focuses the card at 390", async ({ page }) => {
    await page.goto("/settings?tab=general&card=privileged-credentials")
    const card = page.getByRole("region", { name: "Privileged credentials" })
    await expect(card).toHaveAttribute("data-focused", "true")
    await expect(card).toBeInViewport()
  })

  test("rows wrap their control under the label instead of overflowing", async ({ page }) => {
    await page.goto("/settings?tab=general")
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})
