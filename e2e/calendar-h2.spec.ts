import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for the Calendar page (F-01, #3411). The routine list and
// the routine calendar are answered from fixtures, so the checks do not depend
// on what a dev environment happens to have scheduled; everything else (shell,
// session, preferences) is the real app.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page, entries: Record<string, string>) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, entries)
}

const ROUTINES = [
  { id: "r1", slug: "e2e-scan", name: "E2E weekly scan", status: "active", dsl_version: "1", definition_hash: "h", ephemeral: false, workspace_visible: true, invocation_count: 0, authored_via: "user_api", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
  { id: "r2", slug: "e2e-triage", name: "E2E nightly triage", status: "active", dsl_version: "1", definition_hash: "h", ephemeral: false, workspace_visible: true, invocation_count: 0, authored_via: "user_api", created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z" },
]

/** Local day + hour → the UTC instant the API would send. */
const iso = (day: Date, hour: number, min = 0) => new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour, min).toISOString()

async function fixtures(page: Page, opts: { failCalendar?: boolean } = {}) {
  const today = new Date()
  const calls: string[] = []
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/pipelines\?/, (route) =>
    route.fulfill({ json: ROUTINES }),
  )
  await page.route(/\/api\/v1\/workspaces\/[^/]+\/pipelines\/calendar\?/, (route) => {
    calls.push(route.request().url())
    if (opts.failCalendar) return route.fulfill({ status: 503, json: { error: "unavailable" } })
    return route.fulfill({
      json: {
        truncated: false,
        events: [
          { id: "s1:a", kind: "planned", at: iso(today, 8), slug: "e2e-scan", name: "E2E weekly scan", timezone: "Europe/Prague" },
          { id: "p1", kind: "pending", at: iso(today, 14), slug: "e2e-scan", name: "E2E weekly scan" },
          { id: "run1", kind: "run", at: iso(today, 2), slug: "e2e-triage", name: "E2E nightly triage", status: "failed" },
          { id: "run2", kind: "run", at: iso(today, 9, 30), slug: "e2e-triage", name: "E2E nightly triage", status: "waiting" },
        ],
      },
    })
  })
  return calls
}

// The Mine/Workspace choice is a per-user server preference, so it carries
// over from whichever test (or person) last touched it. The fixture routines
// have no author, so under Mine they are all filtered away; a test that needs
// the grid picks Workspace itself instead of inheriting it.
async function workspaceScope(page: Page) {
  await page.getByRole("button", { name: /^Workspace/ }).first().click()
  await expect(page.getByTestId("calendar-summary")).toContainText("Workspace")
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("CAL-01 nav shows Calendar under Work", async ({ page }) => {
    // Lands with the kit's nav change (lib/nav-sections.ts); until then this fails by design.
    await seed(page, PINNED)
    await page.goto("/issues")
    const nav = page.locator("[data-slot=sidebar-container]")
    await expect(nav.getByRole("link", { name: "Calendar" })).toHaveAttribute("href", "/calendar")
  })

  test("CAL-01 Mine/Workspace switch keeps ?view and date in URL", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    await page.goto("/calendar?view=month&date=2027-03-10")
    await expect(page.getByRole("heading", { level: 1, name: "Calendar" })).toBeVisible()
    await workspaceScope(page)
    await expect(page.getByTestId("calendar-summary")).toContainText("Workspace")
    await page.getByRole("button", { name: /^Mine/ }).first().click()
    await expect(page.getByTestId("calendar-summary")).toContainText("Mine")
    expect(new URL(page.url()).searchParams.get("view")).toBe("month")
    expect(new URL(page.url()).searchParams.get("date")).toBe("2027-03-10")
  })

  test("CAL-02 week renders at 1440 and 1024", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    for (const width of [1440, 1024]) {
      await page.setViewportSize({ width, height: 900 })
      await page.goto("/calendar?view=week")
      await workspaceScope(page)
      await expect(page.getByRole("grid", { name: "Week" })).toBeVisible()
      await expect(page.locator("[data-hour-label]").first()).toBeVisible()
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow, `page scrolls sideways at ${width}`).toBeLessThanOrEqual(0)
    }
  })

  test("CAL-04 a month grid loads in at most two bounded requests", async ({ page }) => {
    await seed(page, PINNED)
    const calls = await fixtures(page)
    await page.goto("/calendar?view=month&date=2027-03-10")
    await workspaceScope(page)
    await expect(page.getByRole("grid", { name: "Month" })).toBeVisible()
    await expect.poll(() => calls.length).toBeGreaterThan(0)
    expect(calls.length).toBeLessThanOrEqual(2)
    for (const u of calls) {
      const q = new URL(u).searchParams
      expect(Date.parse(q.get("to")!) - Date.parse(q.get("from")!)).toBeLessThanOrEqual(32 * 86_400_000)
    }
  })

  test("CAL-06 click opens 480 peek, J/K moves, Esc closes", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    await page.goto("/calendar?view=week")
    await workspaceScope(page)
    await page.locator("[data-calendar-item='run:run1']").click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    expect(Math.round((await peek.boundingBox())!.width)).toBe(480)
    await expect(peek).toContainText("At risk")
    await peek.press("j")
    await expect(peek).not.toContainText("At risk")
    await peek.press("k")
    await expect(peek).toContainText("At risk")
    await page.keyboard.press("Escape")
    await expect(peek).toBeHidden()
  })

  test("CAL-07 click on an empty slot opens Schedule a start prefilled", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    await page.goto("/calendar?view=week")
    await workspaceScope(page)
    const slot = page.getByRole("button", { name: /^Schedule a start on .* at 14:00$/ }).last()
    await slot.focus()
    await slot.click()
    const dialog = page.getByRole("dialog", { name: /Schedule a start/ })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByLabel("Start time")).toHaveValue("14:00")
  })

  // CAL-02 (#3495): One time / Repeating is the kit Segmented. Without the
  // right to add schedules Repeating is a disabled segment with its reason on
  // screen; with it, Repeating is enabled and no reason shows. The E2E user's
  // role decides which branch runs — both are the contract.
  test("CAL-07 Repeating is a kit segment, disabled with its reason without the right", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    await page.goto("/calendar?view=week")
    await workspaceScope(page)
    await page.getByRole("button", { name: /^Schedule a start on .* at 14:00$/ }).last().click()
    const dialog = page.getByRole("dialog", { name: /Schedule a start/ })
    const group = dialog.getByRole("radiogroup", { name: "How often" })
    await expect(group).toHaveAttribute("data-slot", "segmented")
    const repeat = group.getByRole("radio", { name: "Repeating" })
    if (await repeat.isDisabled()) {
      await expect(repeat).toHaveAttribute("title", /Managers and above/)
      await expect(dialog.getByTestId("repeat-blocked")).toBeVisible()
      await expect(group.getByRole("radio", { name: "One time" })).toHaveAttribute("aria-checked", "true")
    } else {
      await expect(dialog.getByTestId("repeat-blocked")).toHaveCount(0)
      await repeat.click()
      await expect(repeat).toHaveAttribute("aria-checked", "true")
    }
  })

  // CAL-03 (#3495, PNL-02): Filter opens the facets inside the secondary
  // panel, under the toolbar, not in a floating popover.
  test("CAL-01 Filter opens the facet block inside the panel", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page)
    await page.goto("/calendar?view=week")
    await workspaceScope(page)
    const panel = page.locator("[data-slot=secondary-panel]")
    const filter = panel.getByRole("button", { name: "Filter" })
    await filter.click()
    await expect(filter).toHaveAttribute("aria-expanded", "true")
    const block = panel.getByRole("group", { name: "Filter calendar" })
    await expect(block).toBeVisible()
    await expect(page.locator("[data-radix-popper-content-wrapper]")).toHaveCount(0)
    await block.getByRole("button", { name: "Status" }).click()
    await block.getByRole("button", { name: /At risk/ }).click()
    await expect(block.getByRole("button", { name: "Clear Status" })).toBeVisible()
    // The week below stays in view while the block is open.
    await expect(page.getByRole("grid", { name: "Week" })).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(block).toBeHidden()
    await expect(filter).toBeFocused()
  })

  test("CAL-08 route abort shows Retry and keeps the view", async ({ page }) => {
    await seed(page, PINNED)
    await fixtures(page, { failCalendar: true })
    await page.goto("/calendar?view=month")
    await expect(page.getByText("The calendar could not be loaded")).toBeVisible()
    await page.unroute(/\/api\/v1\/workspaces\/[^/]+\/pipelines\/calendar\?/)
    await fixtures(page)
    await page.getByRole("button", { name: "Retry" }).click()
    await expect(page.getByRole("grid", { name: "Month" })).toBeVisible()
    expect(new URL(page.url()).searchParams.get("view")).toBe("month")
  })

  test("CAL-09 routines calendar links land on /calendar with view, date and the routine layer", async ({ page }) => {
    await seed(page, PINNED)
    // Fixtures, so the month is not empty on a dev environment with nothing
    // scheduled in January 2026 (an empty range shows "Nothing scheduled").
    await fixtures(page)
    await page.goto("/routines?tab=calendar&calendar=year&date=2026-01-01")
    await page.getByRole("link", { name: "Open in Calendar" }).click()
    await expect(page).toHaveURL(/\/calendar\?view=month&date=2026-01-01&layers=routine/)
    await workspaceScope(page)
    await expect(page.getByRole("heading", { level: 2, name: "January 2026" })).toBeVisible()
    await expect(page.getByRole("grid", { name: "Month" })).toBeVisible()
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("CAL-01 phone 390 defaults to Agenda and opens the panel as a sheet", async ({ page }) => {
    await fixtures(page)
    await page.goto("/calendar")
    await expect(page.getByLabel("Agenda", { exact: true }).or(page.getByText("Nothing matches these filters"))).toBeVisible()
    await page.getByRole("button", { name: /· filters$/ }).click()
    await expect(page.locator("[data-slot=secondary-panel-sheet]")).toBeVisible()
  })

  test("CAL-03 phone agenda rows are ≥44 tall with pointer:coarse", async ({ page }) => {
    await fixtures(page)
    await page.goto("/calendar?view=agenda")
    await page.getByRole("button", { name: /· filters$/ }).click()
    await page.locator("[data-slot=secondary-panel-sheet]").getByRole("button", { name: /^Workspace/ }).click()
    const rows = page.locator("[aria-label=Agenda] [data-calendar-item]")
    await expect(rows.first()).toBeVisible()
    for (const b of await rows.all()) expect(Math.round((await b.boundingBox())!.height)).toBeGreaterThanOrEqual(44)
  })
})
