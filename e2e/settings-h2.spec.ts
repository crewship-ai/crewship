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

  // ── #3487 finishing items ────────────────────────────────────────────

  test("SET-01 T12 a member opens in the 480 px Peek and J moves to the next person", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/members")
    const rows = page.locator("tr[data-member]")
    await expect(rows.first()).toBeVisible()
    test.skip((await rows.count()) < 2, "environment prerequisite: the workspace needs two members")
    await rows.first().locator("[title]").first().click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    expect(Math.round((await peek.boundingBox())!.width)).toBe(480)
    const detail = peek.locator("[data-slot=member-detail]")
    const first = await detail.getAttribute("aria-label")
    await peek.press("j")
    await expect(detail).not.toHaveAttribute("aria-label", first!)
    await expect(peek.locator("[data-slot=member-cli]")).toContainText("crewship workspace member capabilities list")
  })

  test("SET-02 T13–T15 crew links: keys 1–3 switch the spaces; a matrix cell opens the small link dialog", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/crew-links")
    const spaces = page.getByRole("tablist", { name: "Spaces" })
    await expect(spaces.getByRole("tab", { name: /By crew/ })).toHaveAttribute("aria-selected", "true")
    await page.keyboard.press("2")
    await expect(page).toHaveURL(/view=all/)
    await page.keyboard.press("3")
    await expect(page).toHaveURL(/view=matrix/)
    const cell = page.getByRole("button", { name: /\. Edit$/ }).first()
    test.skip((await cell.count()) === 0, "environment prerequisite: the workspace needs two crews")
    await cell.click()
    const dialog = page.getByRole("dialog", { name: / → / })
    await expect(dialog).toBeVisible()
    await expect(dialog).toContainText("The other direction is set separately.")
    expect((await dialog.boundingBox())!.width).toBeLessThanOrEqual(480)
    // The kit dialog has a header ✕ (Close) and a footer Cancel; either dismisses.
    await dialog.getByRole("button", { name: /^(Cancel|Close)$/ }).last().click()
    await expect(dialog).toBeHidden()
    await page.keyboard.press("1")
    await expect(page).not.toHaveURL(/view=/)
  })

  test("SET-10 T13 crew link facets open inside the panel, not as a popover", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/crew-links")
    const panel = page.getByRole("complementary", { name: "Crew links navigation" })
    await panel.getByRole("button", { name: /Filter/ }).click()
    const block = panel.locator("[data-slot=facet-block]")
    await expect(block).toBeVisible()
    await expect(page.locator("[data-slot=popover-content]")).toHaveCount(0)
    await block.getByRole("button", { name: /^Network/ }).click()
    await expect(block.getByRole("button", { name: /^Restricted/ })).toBeVisible()
  })

  test("SET-03 T16 the range is one segmented control on the top bar; a row opens its detail", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/audit")
    const range = page.getByRole("radiogroup", { name: "Time range" })
    await expect(range.getByRole("radio")).toHaveCount(6)
    await range.getByRole("radio", { name: "Last 30 days" }).click()
    await expect(page).toHaveURL(/audit_range=30d/)
    await expect(page.locator("[data-slot=audit-display]").getByRole("switch", { name: "Group repeated events" })).toBeVisible()
    const row = page.locator("[data-audit-weight] > button[aria-controls]").first()
    test.skip((await row.count()) === 0, "environment prerequisite: no audit events in the last 30 days")
    await row.click()
    await expect(page.locator(`#${await row.getAttribute("aria-controls")}`)).toContainText("This record is immutable.")
  })

  test("SET-04 T09 ?card= marks the card with a 2 px outline that follows its corners", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=general&card=identity")
    const card = page.getByRole("region", { name: "Identity" })
    await expect(card).toHaveAttribute("data-focused", "true")
    await expect(card).toHaveCSS("outline-style", "solid")
    await expect(card).toHaveCSS("outline-width", "2px")
    await expect(card).toHaveCSS("border-top-left-radius", "10px")
    await expect(card).not.toHaveAttribute("data-focused", "true", { timeout: 2500 })
  })

  test("SET-05 T07 classification counts open the Vault filtered by class", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=access-secrets")
    const card = page.getByRole("region", { name: "Classification" })
    for (const cls of ["STANDARD", "RESTRICTED", "SEALED"]) {
      await expect(card.locator(`a[href="/credentials?class=${cls}"]`)).toHaveCount(1)
    }
  })

  test("SET-06 small controls in a Settings card are the 28 px token at Small", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/members")
    const exportCsv = page.getByRole("button", { name: "Export CSV" })
    await expect(exportCsv).toBeVisible()
    const h = Math.round((await exportCsv.boundingBox())!.height)
    expect(h).toBeGreaterThanOrEqual(27)
    expect(h).toBeLessThanOrEqual(29)
  })

  test("SET-07 drill pages say what they are once, in the section head", async ({ page }) => {
    await seed(page, PINNED)
    for (const [path, sentence] of [
      ["/settings/members", "Who is in this workspace"],
      ["/settings/crew-links", "Who hands work to whom"],
      ["/settings/audit", "Every state-changing action, immutably recorded"],
    ] as const) {
      await page.goto(path)
      await expect(page.locator("[data-slot=drill-head]")).toContainText(sentence)
      await expect(page.getByText(sentence)).toHaveCount(1)
    }
  })

  test("SET-07 kit DrillPage: the top bar is the path; the panel has Back above the toolbar, then facets, then spaces", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings/crew-links")
    await expect(page.getByRole("heading", { level: 1 })).toContainText(/Settings\s*\/?\s*Crew links/)
    const panel = page.locator("[data-slot=secondary-panel]")
    const back = panel.locator("[data-slot=drill-back-row]")
    await expect(back).toBeVisible()
    const y = async (loc: ReturnType<Page["locator"]>) => (await loc.first().boundingBox())!.y
    const backY = await y(back)
    const searchY = await y(panel.getByRole("searchbox").or(panel.getByPlaceholder(/search/i)))
    const spacesY = await y(panel.locator("[data-slot=panel-spaces]"))
    expect(backY).toBeLessThan(searchY)
    expect(searchY).toBeLessThan(spacesY)
    // The back row leads to the parent.
    await back.getByRole("link", { name: "Back to Settings" }).click()
    await expect(page).toHaveURL(/\/settings(\?|$)/)
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

  test("SET-01/02/03 drill pages at 390: no horizontal overflow; the member Peek is a bottom sheet", async ({ page }) => {
    for (const path of ["/settings/members", "/settings/crew-links", "/settings/audit"]) {
      await page.goto(path)
      await expect(page.locator("[data-slot=drill-head]")).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow, path).toBeLessThanOrEqual(0)
    }
    await page.goto("/settings/members")
    const person = page.locator("[data-slot=members-list] [title]").first()
    await expect(person).toBeVisible()
    await person.click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    expect(Math.round((await peek.boundingBox())!.width)).toBe(390)
  })

  test("SET-07 a drill page at 390: the arrow Back is on the top bar and goes to Settings", async ({ page }) => {
    await page.goto("/settings/members")
    const back = page.getByRole("link", { name: "Back to Settings" })
    await expect(back).toBeVisible()
    const box = (await back.boundingBox())!
    expect(box.height).toBeGreaterThanOrEqual(28)
    // The panel is a sheet on a phone: no panel column, no back row inside the page.
    await expect(page.locator("aside[data-slot=secondary-panel]")).toHaveCount(0)
    await back.click()
    await expect(page).toHaveURL(/\/settings(\?|$)/)
  })

  test("rows wrap their control under the label instead of overflowing", async ({ page }) => {
    await page.goto("/settings?tab=general")
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})

test.describe("tablet", () => {
  test.use({ viewport: { width: 1024, height: 768 } })

  test("SET-07 a drill page at 1024: Back with the parent's name on the top bar, the path, the head, no horizontal overflow", async ({ page }) => {
    for (const path of ["/settings/members", "/settings/crew-links", "/settings/audit"]) {
      await page.goto(path)
      const back = page.getByRole("link", { name: "Back to Settings" })
      await expect(back).toBeVisible()
      await expect(back).toContainText("Settings")
      await expect(page.locator("[data-slot=drill-head]")).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow, path).toBeLessThanOrEqual(0)
    }
    await page.goto("/settings/members")
    await page.getByRole("link", { name: "Back to Settings" }).click()
    await expect(page).toHaveURL(/\/settings(\?|$)/)
  })
})
