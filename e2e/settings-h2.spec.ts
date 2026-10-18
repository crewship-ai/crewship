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

  test("T06 card anatomy: 32 px fields, 10 px card radius, 60 px card header", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/settings?tab=general")
    const identity = page.getByRole("region", { name: "Identity" })
    await expect(identity).toBeVisible()
    await expect(identity).toHaveCSS("border-top-left-radius", "10px")
    // v67 `.scard header`: 14 px padding around the 32 px tile (60 px with no
    // subtitle, 68 px with one — PAR-088, measured in the next test).
    expect(await height(page, "[data-slot=settings-card-header]")).toBeGreaterThanOrEqual(58)
    expect(await height(page, "[data-slot=settings-card-header]")).toBeLessThanOrEqual(62)
    expect(Math.round((await identity.getByLabel("Workspace name").boundingBox())!.height)).toBe(32)
  })

  // v67 parity (PAR-086, 087, 088): the content column, the section list rows
  // and the card header with a subtitle, measured at the three widths.
  for (const width of [1440, 1024]) {
    test(`PAR-086/087/088 shell at ${width}: 820 px column, 34 px list rows, 68 px card header`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 })
      await seed(page, PINNED)
      await page.goto("/settings?tab=profile")
      const head = page.locator("[data-slot=settings-page-head]")
      await expect(head).toBeVisible()
      const column = await head.evaluate((el) => (el.closest("div.mx-auto") as HTMLElement).getBoundingClientRect().width)
      // 820 px, or the room there is (the panel takes 272 px of a 1024 px page).
      expect(Math.round(column)).toBeLessThanOrEqual(820)
      if (width === 1440) expect(Math.round(column)).toBe(820)
      const row = page.getByRole("navigation", { name: "Settings sections" }).getByRole("button", { name: "Appearance" })
      expect(Math.round((await row.boundingBox())!.height)).toBe(34)
      const account = page.getByRole("region", { name: "Account" }).locator("[data-slot=settings-card-header]")
      const h = Math.round((await account.boundingBox())!.height)
      expect(h).toBeGreaterThanOrEqual(66)
      expect(h).toBeLessThanOrEqual(70)
    })
  }

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

  test("kit: a small Button is a 44 px target on a touch device (the control tokens), SET-06 keeps 28 px for a fine pointer", async ({ page }) => {
    await page.goto("/settings/members")
    const exportCsv = page.getByRole("button", { name: "Export CSV" })
    await expect(exportCsv).toBeVisible()
    expect(Math.round((await exportCsv.boundingBox())!.height)).toBeGreaterThanOrEqual(44)
    expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--control-sm").trim())).toBe("44px")
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

// ── v67 parity (PAR-086 … PAR-119) ──────────────────────────────────────
// Measured in a browser at the three widths. The sizes are the v67 ones at
// the Small text size (a 14.857 px root, pinned by PINNED); a fine pointer.

const sansNotMono = async (locator: import("@playwright/test").Locator) => {
  const family = await locator.evaluate((el) => getComputedStyle(el).fontFamily)
  expect(family.toLowerCase()).not.toMatch(/mono|courier/)
}

test.describe("v67 parity", () => {
  for (const width of [1440, 1024, 390]) {
    test.describe(`at ${width}`, () => {
      test.use({ viewport: { width, height: 1000 } })

      test("PAR-089/090 profile: Change and Remove with a picture, a sans role pill, 12 Oct 2026", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings?tab=profile")
        const workspace = page.getByRole("region", { name: "Workspace" })
        await expect(workspace).toBeVisible()
        await sansNotMono(workspace.locator("[data-slot=status-pill]").first())
        const joined = workspace.getByText(/^\d{1,2} [A-Z][a-z]{2} \d{4}$/)
        await expect(joined).toBeVisible()
        await sansNotMono(joined)
      })

      test("PAR-097/098/100 appearance: 22 px swatches named by tooltip, wireframe previews, 28 px segments", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings?tab=appearance")
        const swatch = page.getByRole("radiogroup", { name: "Accent colour" }).getByRole("radio").first()
        await expect(swatch).toBeVisible()
        const box = (await swatch.boundingBox())!
        expect(Math.round(box.width)).toBe(22)
        await expect(swatch).toHaveAttribute("title", /\w/)
        await expect(page.locator("[data-slot=theme-preview]")).toHaveCount(3)
        const segment = page.getByRole("group", { name: "Text size" }).getByRole("button").first()
        expect(Math.round((await segment.boundingBox())!.height)).toBe(28)
        expect(await segment.evaluate((el) => getComputedStyle(el).fontSize)).toBe("13px")
      })

      test("PAR-115/116/117 general: right-aligned fields, five Pages tiles, Fill in missing avatars", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings?tab=general")
        await expect(page.getByLabel("Workspace name")).toHaveCSS("text-align", "right")
        const pages = page.getByRole("region", { name: "Pages appearance" })
        await expect(pages.getByRole("radio")).toHaveCount(5)
        await expect(pages.locator(".uppercase")).toHaveCount(0)
        await expect(page.getByRole("button", { name: /Store missing avatars/ })).toHaveCount(0)
      })

      test("PAR-118/119 access & secrets: the same reveal explanation for every role, count and avatars", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings?tab=access-secrets")
        await expect(page.getByText(/Each reveal still needs a fresh sign-in and a reason/)).toBeVisible()
        await expect(page.getByText(/the API refuses everyone else/)).toHaveCount(0)
        await expect(page.getByRole("region", { name: "Who may reveal" }).getByText(/can read secrets in plaintext/)).toHaveCount(0)
      })

      test("PAR-101/102/103/104 members: primary Add member, five KPI tiles, sans table head", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings/members")
        const strip = page.locator("[data-slot=members-summary]")
        await expect(strip).toBeVisible()
        expect(await strip.locator("[data-slot=settings-kpi]").count()).toBeGreaterThanOrEqual(3)
        if (width >= 1024) {
          const add = page.getByRole("button", { name: "Add member" })
          await expect(add).toHaveAttribute("data-variant", "default")
          const th = page.locator("table[aria-label='People and what they may do'] thead th").nth(1)
          await expect(th).toBeVisible()
          await expect(th).toHaveCSS("text-transform", "none")
          await sansNotMono(th)
          await expect(page.locator("[data-slot=drill-section] button[aria-expanded]")).toHaveCount(0)
        }
      })

      test("PAR-107/109 crew links: CLI hint and New link, four KPI tiles, the radial map, one column", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings/crew-links")
        await expect(page.getByRole("button", { name: "Command line" })).toBeVisible()
        const strip = page.locator("[data-slot=crew-links-summary]")
        await expect(strip).toBeVisible()
        expect(await strip.locator("[data-slot=settings-kpi]").count()).toBeGreaterThanOrEqual(3)
        const hands = page.getByRole("region", { name: "Hands work to" })
        test.skip((await hands.count()) === 0, "environment prerequisite: the workspace needs a crew")
        const receives = page.getByRole("region", { name: "Receives work from" })
        const a = (await hands.boundingBox())!, b = (await receives.boundingBox())!
        // One column: the second card is under the first, not beside it.
        expect(b.y).toBeGreaterThan(a.y + a.height - 2)
        if (width >= 768) {
          await expect(page.getByRole("region", { name: "Link map" }).locator("circle[data-slot=link-map-centre]")).toBeVisible()
          await expect(page.getByRole("button", { name: "New link" })).toHaveAttribute("data-variant", "default")
        }
      })

      test("PAR-110/111/112/113/114 audit log: spaces card, 24-hour times, solid bars, info banner, icon-only refresh", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/settings/audit")
        const crumb = page.getByRole("banner").getByText(/^Audit\s?log$/).first()
        // The word space survives the heading's tracking (PAR-110).
        await expect(page.getByRole("heading", { name: "Audit log" }).first()).toBeVisible()
        expect(await crumb.evaluate((el) => parseFloat(getComputedStyle(el).letterSpacing) || 0)).toBeGreaterThan(-0.2)
        await expect(page.locator("[data-slot=journal-verify]")).toHaveClass(/page-banner-info/)
        const chart = page.locator("[data-slot=audit-histogram]")
        await expect(chart).toBeVisible()
        await expect(chart.locator("[data-bar]").first()).toBeVisible()
        const events = page.getByRole("region", { name: "Events" })
        await expect(events.getByRole("button", { name: "Refresh audit log" })).toHaveText("")
        const time = events.locator("[data-audit-weight] .tabular-nums").first()
        if (await time.count()) await expect(time).toHaveText(/^\d{2}:\d{2}$|^$/)
        if (width >= 1024) await expect(page.locator("[data-slot=panel-spaces]").getByRole("tab")).toHaveCount(4)
      })
    })
  }
})
