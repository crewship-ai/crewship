import { test, expect, type Locator, type Page } from "@playwright/test"

// Phone pilot: Inbox + Settings (MOBIL-DOPORUCENI-2026-10-19 §2a,
// PILOT-TELEFON-STAV). Whole user flows on a narrow and a common phone
// (360 × 740, 390 × 844, touch), and the same screens at 1024 and 1440 as a
// regression check. Data: the ui-tour demo (Acme Company).
//
// Writes: opening an unread Inbox row marks it read and "Mark unread" puts it
// back, so the Inbox flows run only with E2E_ALLOW_WRITES=1 on a disposable
// environment. The approval detail's layout is checked on a pending copy of a
// decided approval served by a route mock (read-only). Appearance is set with
// sync off, so it stays in this browser.
//
// The on-screen keyboard cannot be opened in Playwright (visualViewport does
// not shrink); the keyboard checks assert how the layout reacts to focus.
// A real-device keyboard check is still required.

const PHONES = [
  { name: "360", viewport: { width: 360, height: 740 } },
  { name: "390", viewport: { width: 390, height: 844 } },
] as const
const DESKTOPS = [
  { name: "1024", viewport: { width: 1024, height: 768 } },
  { name: "1440", viewport: { width: 1440, height: 900 } },
] as const

const writes = Boolean(process.env.E2E_ALLOW_WRITES)

async function seed(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem("crewship-appearance-sync", "0")
    localStorage.setItem("theme", "dark")
  })
}

const header = (page: Page) => page.locator("header").first()
const inboxSearch = (page: Page) => page.getByRole("textbox", { name: "Search inbox…" })
const rows = (page: Page) => page.getByTestId("inbox-row")
const listScroller = (page: Page) => page.locator("[data-inbox-list-scroller]")

async function openUpdates(page: Page) {
  await page.goto("/inbox?view=updates")
  await expect(rows(page).first()).toBeVisible({ timeout: 60_000 })
}

/** elementFromPoint at the centre and 4 px inside each edge hits `target`. */
async function expectHits(target: Locator) {
  const misses = await target.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const points: [string, number, number][] = [
      ["centre", r.left + r.width / 2, r.top + r.height / 2],
      ["left", r.left + 4, r.top + r.height / 2],
      ["right", r.right - 4, r.top + r.height / 2],
      ["top", r.left + r.width / 2, r.top + 4],
      ["bottom", r.left + r.width / 2, r.bottom - 4],
    ]
    return points
      .filter(([, x, y]) => {
        const hit = document.elementFromPoint(x, y)
        return !(hit && (hit === el || el.contains(hit)))
      })
      .map(([p]) => p)
  })
  expect(misses, `${await target.getAttribute("aria-label")} is hit everywhere`).toEqual([])
  const box = (await target.boundingBox())!
  expect(box.width).toBeGreaterThanOrEqual(44)
  expect(box.height).toBeGreaterThanOrEqual(44)
}

for (const phone of PHONES) {
  test.describe(`phone ${phone.name}`, () => {
    test.use({ viewport: phone.viewport, isMobile: true, hasTouch: true })
    test.beforeEach(async ({ page }) => { await seed(page) })

    test.describe("Inbox", () => {
      test.skip(!writes, "opening a row marks it read; set E2E_ALLOW_WRITES=1 on a disposable environment")

      test("find → open → act → Back to the same filtered list", async ({ page }) => {
        await openUpdates(page)
        // The list header: sail, the area's name, global search; no tile.
        await expect(header(page).locator("[data-slot=area-sail]")).toBeVisible()
        await expect(header(page).locator(".area-tile")).toHaveCount(0)
        await expect(inboxSearch(page)).toHaveAttribute("placeholder", "Search inbox…")

        await inboxSearch(page).fill("newsletter")
        await page.getByRole("button", { name: /^Filter/ }).click()
        await page.getByRole("button", { name: "Source type", exact: true }).click()
        await page.getByRole("group", { name: "Source type" }).getByRole("button", { name: /^Message/ }).click()
        await page.getByRole("button", { name: /^Filter/ }).click()
        await expect(page.getByRole("button", { name: "Remove filter Source type: Message" })).toBeVisible()
        await expect(page.getByRole("button", { name: "Clear filters" }).first()).toBeVisible()

        const row = rows(page).filter({ hasText: "newsletter" }).first()
        const title = (await row.locator("span[title]").first().getAttribute("title"))!
        await row.click()
        await expect(page).toHaveURL(/item=/)
        // Detail header: Back, the item's name (two lines at most), ⋯.
        const back = header(page).getByRole("button", { name: "Back to inbox" })
        await expect(back).toBeVisible()
        await expect(header(page).locator("[data-slot=sub-bar-detail-title]")).toHaveText(title)
        await expect(header(page).getByRole("button", { name: "Next (J)" })).toHaveCount(0)
        // Act: Mark unread from ⋯ (the open marked it read).
        await header(page).getByRole("button", { name: "More actions" }).click()
        await page.getByRole("dialog", { name: "More actions" }).getByRole("button", { name: "Mark unread" }).click()
        await back.click()
        await expect(page).not.toHaveURL(/item=/)
        await expect(inboxSearch(page)).toHaveValue("newsletter")
        await expect(page.getByRole("button", { name: "Remove filter Source type: Message" })).toBeVisible()
        await expect(rows(page).filter({ hasText: title }).first()).toBeVisible()
      })

      test("Back restores the list's scroll position", async ({ page }) => {
        await openUpdates(page)
        const scroller = listScroller(page)
        const max = await scroller.evaluate((el) => el.scrollHeight - el.clientHeight)
        test.skip(max < 40, "the list is not long enough to scroll at this height")
        await scroller.evaluate((el) => { el.scrollTop = el.scrollHeight })
        const before = await scroller.evaluate((el) => el.scrollTop)
        await rows(page).last().click()
        await expect(page).toHaveURL(/item=/)
        await header(page).getByRole("button", { name: "Back to inbox" }).click()
        await expect(rows(page).first()).toBeVisible()
        await expect.poll(() => listScroller(page).evaluate((el) => el.scrollTop)).toBeGreaterThan(before - 6)
      })

      test("browser Back returns to the filtered list", async ({ page }) => {
        await openUpdates(page)
        await inboxSearch(page).fill("newsletter")
        await rows(page).filter({ hasText: "newsletter" }).first().click()
        await expect(page).toHaveURL(/item=/)
        await page.goBack()
        await expect(page).not.toHaveURL(/item=/)
        await expect(inboxSearch(page)).toHaveValue("newsletter")
      })

      test("reload on the detail, then Back, returns to the filtered list", async ({ page }) => {
        await openUpdates(page)
        await inboxSearch(page).fill("newsletter")
        await rows(page).filter({ hasText: "newsletter" }).first().click()
        await expect(page).toHaveURL(/item=/)
        await page.reload()
        await header(page).getByRole("button", { name: "Back to inbox" }).click()
        await expect(page).toHaveURL(/\/inbox(\?view=updates)?$/)
        await expect(inboxSearch(page)).toHaveValue("newsletter")
      })

      test("a direct link in a fresh page: Back goes to the inbox, not out of the app", async ({ page, context }) => {
        test.setTimeout(120_000)
        await openUpdates(page)
        await rows(page).first().click()
        await expect(page).toHaveURL(/item=/)
        const link = page.url()
        await page.close() // the link opens alone: no list under it in any history
        const fresh = await context.newPage()
        await fresh.bringToFront()
        await fresh.goto(link)
        await fresh.getByRole("button", { name: "Back to inbox" }).first().click({ timeout: 60_000 })
        await expect(fresh).toHaveURL(/\/inbox(\?view=updates)?$/)
        await expect(fresh.getByTestId("inbox-row").first()).toBeVisible()
        await fresh.close()
      })

      test("detail header targets are 44 × 44 and hit where they are drawn", async ({ page }) => {
        await openUpdates(page)
        await rows(page).first().click()
        await expect(page).toHaveURL(/item=/)
        for (const name of ["Back to inbox", "More actions", "Search"]) {
          await expectHits(header(page).getByRole("button", { name, exact: true }))
        }
      })

      test("focusing the search hides the tab bar", async ({ page }) => {
        await openUpdates(page)
        const tabs = page.getByRole("navigation", { name: "Primary" })
        await expect(tabs).toBeVisible()
        await inboxSearch(page).focus()
        await expect(tabs).toBeHidden()
        await inboxSearch(page).blur()
        await expect(tabs).toBeVisible()
      })
    })

    test("a workspace switch does not bring back the other workspace's search", async ({ page }) => {
      // A second workspace the list could leak into, served by a route mock;
      // what is checked is only that the list starts clean there.
      await page.route(/\/api\/v1\/workspaces(\?|$)/, async (route) => {
        if (route.request().method() !== "GET") return route.continue()
        const res = await route.fetch()
        const list = (await res.json()) as Array<Record<string, unknown>>
        const twin = { ...list[0], id: "pilot-ws-b", name: "Pilot Beta", slug: "pilot-beta" }
        await route.fulfill({ response: res, json: [...list, twin] })
      })
      await page.goto("/inbox?view=updates")
      await expect(inboxSearch(page)).toBeVisible({ timeout: 60_000 })
      await inboxSearch(page).fill("newsletter")
      // Control: a reload in the same workspace brings the search back.
      await page.waitForTimeout(400)
      await page.reload()
      await expect(inboxSearch(page)).toHaveValue("newsletter", { timeout: 60_000 })
      await inboxSearch(page).blur() // the tab bar comes back once the field lets go
      await page.getByRole("navigation", { name: "Primary" }).getByRole("button", { name: "More" }).click()
      await page.getByRole("button", { name: /Current workspace/ }).click()
      await page.getByRole("menuitem", { name: /Pilot Beta/ }).click()
      await expect.poll(() => page.evaluate(() => Object.entries(localStorage).some(([k, v]) => k.startsWith("crewship.workspaceId") && v === "pilot-ws-b"))).toBe(true)
      await page.keyboard.press("Escape")
      // A reload in the other workspace — which in Acme brought the search
      // back — starts clean.
      await page.reload()
      await expect(inboxSearch(page)).toBeVisible({ timeout: 60_000 })
      await expect(inboxSearch(page)).toHaveValue("")
    })

    test.describe("Inbox approval detail (pending copy, read-only)", () => {
      async function pendingApproval(page: Page): Promise<boolean> {
        let id: string | null = null
        await page.route(/\/api\/v1\/inbox(\/[^/?]+)?(\?|$)/, async (route) => {
          const url = route.request().url()
          if (route.request().method() !== "GET" || url.includes("/counts")) return route.continue()
          const res = await route.fetch()
          const body = await res.json()
          const pend = (it: Record<string, unknown>) => {
            if (it?.kind === "waitpoint" && it.state === "resolved" && (id === null || it.id === id)) {
              id = String(it.id)
              Object.assign(it, { state: "unread", resolved_at: null, resolved_action: null, resolved_by_user_id: null, read_at: null })
            }
          }
          if (Array.isArray(body?.rows)) body.rows.forEach(pend)
          else pend(body)
          await route.fulfill({ response: res, json: body })
        })
        await page.goto("/inbox?view=action")
        const row = rows(page).first()
        await row.waitFor({ timeout: 60_000 }).catch(() => {})
        if (!(await row.isVisible())) return false
        await row.click()
        await expect(page.getByTestId("decision-card")).toBeVisible()
        return true
      }

      for (const size of ["default", "large"] as const) {
        test(`no horizontal overflow, header inside the screen (text ${size})`, async ({ page }) => {
          if (size === "large") await page.addInitScript(() => document.documentElement.setAttribute("data-text-size", "large"))
          test.skip(!(await pendingApproval(page)), "no decided approval in the demo data to copy")
          await page.waitForTimeout(1500) // the run's steps load
          const wide = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, w: window.innerWidth }))
          expect(wide.sw).toBeLessThanOrEqual(wide.w)
          for (const el of [header(page).locator("[data-slot=sub-bar-detail-title]"), page.getByTestId("inbox-detail-frame").locator("h1")]) {
            const b = (await el.boundingBox())!
            expect(b.x).toBeGreaterThanOrEqual(0)
            expect(b.x + b.width).toBeLessThanOrEqual(wide.w)
          }
          // Run steps: the step's name on one line with its kind.
          const step = page.getByText("approve", { exact: true }).first()
          if (await step.isVisible()) expect((await step.boundingBox())!.height).toBeLessThan(24)
        })
      }

      test("decision dock: the note above, the buttons side by side, Approve last", async ({ page }) => {
        test.skip(!(await pendingApproval(page)), "no decided approval in the demo data to copy")
        const dock = page.getByTestId("decision-dock")
        const buttons = dock.getByRole("button")
        const names = await buttons.allInnerTexts()
        expect(names.at(-1)).toMatch(/Approve/)
        const note = dock.getByText("Buttons named by the routine author")
        if (await note.isVisible()) {
          const n = (await note.boundingBox())!
          const first = (await buttons.first().boundingBox())!
          expect(n.y + n.height).toBeLessThanOrEqual(first.y + 1)
        }
        const [a, b] = [(await buttons.nth(0).boundingBox())!, (await buttons.nth(1).boundingBox())!]
        expect(Math.abs(a.y - b.y)).toBeLessThan(2) // one row
        const wide = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
        expect(wide).toBe(true)
      })
    })

    test.describe("Settings", () => {
      test("Appearance: change the accent, it stays after a reload, Back to the section list", async ({ page }) => {
        await page.goto("/settings")
        await expect(header(page).locator("[data-slot=area-sail]")).toBeVisible({ timeout: 60_000 })
        await page.getByRole("button", { name: "Appearance" }).click()
        await expect(page).toHaveURL(/tab=appearance/)
        const swatches = page.getByRole("radiogroup", { name: "Accent colour" }).getByRole("radio")
        const target = swatches.nth(1)
        const label = (await target.getAttribute("aria-label"))!
        await target.click()
        await expect(target).toHaveAttribute("aria-checked", "true")
        await page.reload()
        await expect(page.getByRole("radiogroup", { name: "Accent colour" }).getByRole("radio", { name: label, exact: true })).toHaveAttribute("aria-checked", "true")
        await header(page).getByRole("button", { name: "Back to Settings" }).click()
        await expect(page).toHaveURL(/\/settings$/)
        await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible()
        await expectHits(header(page).getByRole("button", { name: "Search", exact: true }))
      })

      test("a deep link to Appearance opens Appearance; Back goes to the list", async ({ context }) => {
        const fresh = await context.newPage()
        await fresh.bringToFront()
        await seed(fresh)
        await fresh.goto("/settings?tab=appearance")
        await expect(fresh.getByRole("heading", { name: "Appearance", exact: true })).toBeVisible({ timeout: 60_000 })
        await expect(fresh.getByRole("radiogroup", { name: "Accent colour" })).toBeVisible()
        await expectHits(fresh.locator("header").first().getByRole("button", { name: "Back to Settings" }))
        await fresh.locator("header").first().getByRole("button", { name: "Back to Settings" }).click()
        await expect(fresh).toHaveURL(/\/settings$/)
        await expect(fresh.getByRole("navigation", { name: "Settings sections" })).toBeVisible()
        await fresh.close()
      })

      test("browser Back from a section returns to the list, not out of Settings", async ({ page }) => {
        await page.goto("/settings")
        await page.getByRole("button", { name: "Appearance" }).click({ timeout: 60_000 })
        await expect(page).toHaveURL(/tab=appearance/)
        await page.goBack()
        await expect(page).toHaveURL(/\/settings$/)
        await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible()
      })

      test("General: an unsaved edit asks before leaving; the save bar sits above the tab bar", async ({ page }) => {
        await page.goto("/settings?tab=general")
        const name = page.getByRole("textbox", { name: "Workspace name" })
        await expect(name).toBeVisible({ timeout: 60_000 })
        const tabs = page.getByRole("navigation", { name: "Primary" })
        await name.click()
        await expect(tabs).toBeHidden() // keyboard up: the tab bar steps aside
        await name.fill(`${await name.inputValue()} (pilot)`)
        const bar = page.getByRole("region", { name: "Unsaved changes" })
        await expect(bar).toBeVisible()
        await name.blur()
        await expect(tabs).toBeVisible()
        const [b, t] = [(await bar.boundingBox())!, (await tabs.boundingBox())!]
        expect(b.y + b.height).toBeLessThanOrEqual(t.y + 1)
        // Leaving asks.
        await tabs.getByRole("link", { name: "Inbox" }).click()
        const ask = page.getByRole("alertdialog", { name: /Leave with 1 unsaved change/ })
        await expect(ask).toBeVisible()
        await ask.getByRole("button", { name: "Stay" }).click()
        await expect(page).toHaveURL(/tab=general/)
        // Back asks too; Discard leaves without saving.
        await header(page).getByRole("button", { name: "Back to Settings" }).click()
        await expect(ask).toBeVisible()
        await ask.getByRole("button", { name: "Discard" }).click()
        await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible()
      })
    })
  })
}

for (const desk of DESKTOPS) {
  test.describe(`regression ${desk.name}`, () => {
    test.use({ viewport: desk.viewport })
    test.beforeEach(async ({ page }) => { await seed(page) })

    test("Inbox detail keeps its desktop header and the list beside it", async ({ page }) => {
      test.skip(!writes, "opening a row marks it read; set E2E_ALLOW_WRITES=1 on a disposable environment")
      await openUpdates(page)
      await rows(page).first().click()
      await expect(page).toHaveURL(/item=/)
      await expect(header(page).getByRole("button", { name: "Next (J)" })).toBeVisible()
      await expect(header(page).getByRole("button", { name: "More actions" })).toHaveCount(0)
      await expect(header(page).locator("[data-slot=area-sail]")).toHaveCount(0)
      await expect(rows(page).first()).toBeVisible()
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    })

    test("Settings deep link opens Appearance beside the section list", async ({ page }) => {
      await page.goto("/settings?tab=appearance")
      await expect(page.getByRole("heading", { name: "Appearance", exact: true })).toBeVisible({ timeout: 60_000 })
      await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible()
      await expect(page.getByRole("button", { name: "Back to Settings" })).toHaveCount(0)
      await page.getByRole("button", { name: "General" }).click()
      await expect(page).toHaveURL(/tab=general/)
      await expect(page.getByRole("textbox", { name: "Workspace name" })).toBeVisible()
    })
  })
}
