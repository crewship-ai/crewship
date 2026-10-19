import { test, expect, type BrowserContext, type Locator, type Page } from "@playwright/test"

// Phone pilot: Inbox + Settings (MOBIL-DOPORUCENI-2026-10-19 §2a,
// PILOT-TELEFON-STAV). Whole user flows on a narrow and a common phone
// (360 × 740, 390 × 844, touch), and the same screens at 1024 and 1440 as a
// regression check.
//
// Data: the Inbox is served by a stateful route mock (serveInbox below), not
// read from the environment. The Inbox rows come from routine runs, mission
// reviews and agent replies, which only a run with a model produces; the CI
// seed (`crewship seed --offline-demo --with-users`) starts no run, so its
// Inbox is empty, and a spec that needs rows cannot depend on them being
// there (same reason harbor2-inbox.spec.ts serves its feed). Sessions, roles,
// the shell and every other API stay real. Opening a row marks it read and
// "Mark unread" puts it back in the mock, so nothing is written to the server
// and the spec needs no E2E_ALLOW_WRITES. The approval detail's layout is
// checked on the mock's approval served as pending. Appearance is set with
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

type Row = Record<string, unknown> & { id: string; kind: string; state: string; title: string; created_at: string }

const NOW = Date.now()
const minutesAgo = (n: number) => new Date(NOW - n * 60_000).toISOString()

/** [title, body, sender] of the digest messages routines and the mission engine leave in the Inbox. */
const MESSAGES: [string, string, string][] = [
  ["ENG-4 ready for review", "Follow up on the Brightwell Hotels trial order", "Mission engine"],
  ["QUA-8 ready for review", "Plan the autumn newsletter", "Mission engine"],
  ["Operations health digest", "All connected services answered in the last hour.", "operations-health-digest"],
  ["Operations health digest", "One integration needs a new token.", "operations-health-digest"],
  ["Low stock: Oak storage box, large", "4 left, the weekly average is 9.", "low-stock-watch"],
  ["Week in sales: EUR 12,480", "Up 8 % on the week before.", "weekly-sales-summary"],
  ["26 new newsletter sign-ups", "26 people signed up since yesterday.", "newsletter-signup-digest"],
  ["31 new newsletter sign-ups", "31 people signed up since yesterday.", "newsletter-signup-digest"],
  ["Payment reminders sent", "3 reminders went out, EUR 1,840 outstanding.", "overdue-invoice-reminders"],
  ...Array.from({ length: 12 }, (_, i): [string, string, string] => [`Orders overnight: ${9 + i} new`, `${9 + i} orders came in since 18:00.`, "morning-order-digest"]),
]

/**
 * The Inbox feed, served from memory for every page of `context`: the list
 * (with the server's state / space / q filters), the counts, one row, and the
 * state PATCH that opening a row and "Mark unread" send. `approvalPending`
 * turns the one approval from decided into waiting.
 */
async function serveInbox(context: BrowserContext) {
  const ws = { id: "" }
  const feed = { approvalPending: false }
  const rows: Row[] = MESSAGES.map(([title, body, sender], i) => ({
    id: `ibx_pilot_message_${i}`, kind: "message", source_id: `pilot_message_${i}`, target_role: "MANAGER",
    title, body_md: body, sender_type: sender === "Mission engine" ? "system" : "agent", sender_name: sender,
    state: "unread", priority: "medium", blocking: false, payload: {},
    created_at: minutesAgo(10 + i * 7), updated_at: minutesAgo(10 + i * 7),
  } as Row))
  const approval: Row = {
    id: "ibx_pilot_waitpoint_0", kind: "waitpoint", source_id: "pilot_waitpoint_0", target_role: "MANAGER",
    title: "Send 3 payment reminders",
    body_md: "3 invoices overdue, EUR 1,840 in total. The oldest is INV-2041 from Marlow & Finch, 21 days late.\n\nApprove to send a friendly reminder to each customer.",
    sender_type: "pipeline", sender_name: "Approval required", state: "resolved", priority: "high", blocking: true,
    payload: {
      decision_form: { actions: [{ approved: true, id: "approve", label: "Approve" }, { approved: false, id: "hold", label: "Not now" }], fields: [] },
      invoking_crew_id: "", pipeline_run_id: "run_pilot_0", risk_level: "normal", step_id: "approve",
      timeout_at: new Date(NOW + 7 * 86_400_000).toISOString(), trust_offer: { eligible: false }, who_can_act: ["role:MANAGER"],
    },
    resolved_at: minutesAgo(300), resolved_by_user_id: "pilot-user", resolved_action: "denied", read_at: minutesAgo(310),
    created_at: minutesAgo(320), updated_at: minutesAgo(300),
  } as Row
  const all = (): Row[] => {
    const a = { ...approval, workspace_id: ws.id }
    if (feed.approvalPending) Object.assign(a, { state: "unread", resolved_at: null, resolved_action: null, resolved_by_user_id: null, read_at: null })
    return [a, ...rows.map((r) => ({ ...r, workspace_id: ws.id }))] as Row[]
  }
  const space = (r: Row) => (r.state === "resolved" ? "history" : r.kind === "message" ? "updates" : "action")

  await context.route(/\/api\/v1\/inbox(\/[^/?]*)?(\?|$)/, async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const tail = url.pathname.replace(/^\/api\/v1\/inbox\/?/, "")
    ws.id = url.searchParams.get("workspace_id") ?? ws.id
    if (tail === "counts") {
      const spaces = { action: 0, updates: 0, history: 0 }
      const by_kind: Record<string, Record<string, number>> = {}
      for (const r of all()) {
        spaces[space(r) as keyof typeof spaces]++
        by_kind[r.kind] ??= { action: 0, updates: 0, history: 0 }
        by_kind[r.kind][space(r)]++
      }
      const decisions = all().filter((r) => r.kind === "waitpoint" && r.state !== "resolved").length
      return route.fulfill({
        json: {
          total: all().length, unread: all().filter((r) => r.state === "unread").length, spaces, by_kind,
          by_attention: { decision: decisions, input: 0, repair: 0, review: 0 }, by_deadline: { hour: 0, none: all().length, soon: 0, today: 0 },
        },
      })
    }
    if (tail === "count") return route.fulfill({ json: { unread_count: all().filter((r) => r.state === "unread").length } })
    if (tail === "") {
      if (req.method() !== "GET") return route.continue()
      const state = url.searchParams.get("state") ?? "all"
      const sp = url.searchParams.get("space")
      const q = (url.searchParams.get("q") ?? "").trim().toLowerCase()
      const out = all().filter((r) => {
        if (state === "active" ? r.state === "resolved" : state !== "all" && r.state !== state) return false
        if (sp && space(r) !== sp) return false
        return !q || `${r.title} ${r.body_md ?? ""} ${r.sender_name ?? ""}`.toLowerCase().includes(q)
      })
      return route.fulfill({ json: { rows: out, count: out.length, unread_count: out.filter((r) => r.state === "unread").length, has_more: false, decision_count: out.filter((r) => r.kind === "waitpoint" && r.state !== "resolved").length } })
    }
    if (tail.includes("/")) return route.continue() // /act and the like are not used here
    const id = decodeURIComponent(tail)
    const row = all().find((r) => r.id === id)
    if (!row) return route.fulfill({ status: 404, json: { error: "not found" } })
    if (req.method() === "GET") return route.fulfill({ json: row })
    if (req.method() === "PATCH") {
      const target = rows.find((r) => r.id === id)
      const { state } = req.postDataJSON() as { state: string }
      if (target) Object.assign(target, { state, read_at: state === "read" ? new Date().toISOString() : target.read_at })
      return route.fulfill({ json: { ok: true } })
    }
    return route.continue()
  })
  return feed
}

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
    let feed: Awaited<ReturnType<typeof serveInbox>>
    test.beforeEach(async ({ page, context }) => { feed = await serveInbox(context); await seed(page) })

    test.describe("Inbox", () => {
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

    test.describe("Inbox approval detail (pending approval)", () => {
      // The mock's approval, waiting for a decision.
      async function pendingApproval(page: Page) {
        feed.approvalPending = true
        await page.goto("/inbox?view=action")
        await rows(page).first().click({ timeout: 60_000 })
        await expect(page.getByTestId("decision-card")).toBeVisible()
      }

      for (const size of ["default", "large"] as const) {
        test(`no horizontal overflow, header inside the screen (text ${size})`, async ({ page }) => {
          if (size === "large") await page.addInitScript(() => document.documentElement.setAttribute("data-text-size", "large"))
          await pendingApproval(page)
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
        await pendingApproval(page)
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
    test.beforeEach(async ({ page, context }) => { await serveInbox(context); await seed(page) })

    test("Inbox detail keeps its desktop header and the list beside it", async ({ page }) => {
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
