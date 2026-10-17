import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for Activity (#3389): one list of work and changes in the
// shared SecondaryPanel layout, at the three widths, with the Peek and the
// empty state. Runs against a deployed environment with seeded activity.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

async function visibleColumns(page: Page) {
  const head = page.getByRole("table", { name: "Activity" }).getByRole("columnheader")
  const names: string[] = []
  for (const h of await head.all()) if (await h.isVisible()) names.push((await h.textContent())?.trim() ?? "")
  return names.filter(Boolean)
}

test.describe("desktop", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("1440 panel + table columns", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=activity]")
    await expect(panel).toBeVisible()
    expect(Math.round((await panel.boundingBox())!.width)).toBe(272)
    await expect(panel.getByRole("button", { name: /Everything/ })).toBeVisible()
    await expect(panel.getByRole("button", { name: /Needs a person/ })).toBeVisible()
    await expect(page.getByRole("group", { name: "Time range" })).toBeVisible()
    await expect(page.getByTestId("activity-summary")).toContainText("items")
    // Progress and Cost wait for the chain index to send them (see the PR).
    expect(await visibleColumns(page)).toEqual(["Work", "Agent", "Took", "Started"])
  })

  test("/activity?run=<id> opens Peek, Esc closes, URL drops run", async ({ page }) => {
    await seed(page)
    // A run id from the chain index the page itself reads. The first row of
    // the list may be a change (that one is written as ?entry=), and the run
    // may sit outside the default range, so the id is not taken from a row.
    const chains = page.waitForResponse((r) => /\/api\/v1\/chains\?/.test(r.url()) && r.ok())
    await page.goto("/activity")
    const { chains: list } = (await (await chains).json()) as { chains: { origin: string }[] }
    test.skip(list.length === 0, "needs at least one recorded run")
    const id = list[0].origin
    const row = page.locator(`[data-row-id="${id}"]`)
    if (await row.count()) {
      await row.click()
      await expect(page.locator("[data-slot=peek]")).toBeVisible()
      await expect(page).toHaveURL(new RegExp(`run=${id}`))
      await page.reload()
    } else {
      await page.goto(`/activity?run=${id}`)
    }
    await expect(page.locator("[data-slot=peek]")).toBeVisible()
    await page.keyboard.press("Escape")
    await expect(page.locator("[data-slot=peek]")).toBeHidden()
    await expect(page).not.toHaveURL(/run=/)
  })

  test("key 1–4 switch views", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByTestId("activity-summary")).toBeVisible()
    await page.keyboard.press("4")
    await expect(page.getByRole("heading", { level: 1, name: "Could not finish" })).toBeVisible()
    await expect(page).toHaveURL(/view=failed/)
    await page.keyboard.press("1")
    await expect(page.getByRole("heading", { level: 1, name: "Everything" })).toBeVisible()
  })

  test("old links land on the list", async ({ page }) => {
    await seed(page)
    await page.goto("/activity?status=failed")
    await expect(page.getByRole("heading", { level: 1, name: "Could not finish" })).toBeVisible()
    await page.goto("/activity?lens=agents")
    await expect(page).toHaveURL(/group=who/)
  })

  test("empty workspace shows No activity yet", async ({ page }) => {
    await seed(page)
    await page.route("**/api/v1/chains?**", (r) => r.fulfill({ json: { chains: [], has_more: false, has_unrecorded_runs: false } }))
    await page.route("**/api/v1/journal?**", (r) => r.fulfill({ json: { entries: [], next_cursor: null } }))
    // The live stream replays the newest changes to a fresh client; an empty
    // workspace has none to replay.
    await page.route("**/api/v1/journal/stream?**", (r) => r.fulfill({ status: 200, contentType: "text/event-stream", body: ": empty\n\n" }))
    await page.goto("/activity")
    await expect(page.getByText("No activity yet")).toBeVisible()
    await expect(page.getByText("crewship activity")).toBeVisible()
  })
})

test.describe("tablet", () => {
  test.use({ viewport: { width: 1024, height: 800 } })

  // v67 also closes the panel at this width; that is the shared panel's call
  // (kit request in the PR), so only the columns are asserted here.
  test("1024 fewer columns", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByTestId("activity-summary")).toBeVisible()
    expect(await visibleColumns(page)).toEqual(["Work", "Agent", "Took"])
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("390 chips + list, Peek full screen", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    await expect(page.getByRole("button", { name: /Everything/ }).first()).toBeVisible()
    await expect(page.getByRole("button", { name: /Last 7 days/ })).toBeVisible()
    await expect(page.getByRole("group", { name: "Time range" })).toHaveCount(0)
    const row = page.getByRole("table", { name: "Activity" }).locator("[data-row-id]").first()
    await row.click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    const b = (await peek.boundingBox())!
    expect(Math.round(b.width)).toBe(390)
    // No horizontal page scroll.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})

// ACT-01…ACT-05 (#3486). The data a check needs is served by page.route, so
// each runs on any seeded workspace; the live stream is answered empty so it
// replays nothing over the routed rows.
const DAY = 86_400_000
const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()
const yesterdayAt = (h: number) => {
  const d = new Date()
  d.setDate(d.getDate() - 1)
  d.setHours(h, 0, 0, 0)
  return d.toISOString()
}
function chainRow(origin: string, msAgo: number) {
  return {
    origin,
    started_by_kind: "schedule",
    started_by: "nightly",
    runs: 1,
    max_chain_depth: 0,
    failed_runs: 0,
    failed: false,
    first_activity: iso(msAgo),
    last_activity: iso(msAgo - 60_000),
    duration_ms: 42_000,
    issue_count: 0,
    agent_count: 0,
    routine_slug: "e2e-routine",
  }
}
async function routeFeed(page: Page, chains: unknown[], entries: unknown[]) {
  await page.route("**/api/v1/chains?**", (r) => r.fulfill({ json: { chains, has_more: false, has_unrecorded_runs: false } }))
  await page.route("**/api/v1/journal?**", (r) => r.fulfill({ json: { entries, next_cursor: null } }))
  await page.route("**/api/v1/journal/stream?**", (r) => r.fulfill({ status: 200, contentType: "text/event-stream", body: ": empty\n\n" }))
}

test.describe("finish (#3486)", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("ACT-01 a run page keeps exactly one h1 while it loads and after", async ({ page }) => {
    await seed(page)
    const chains = page.waitForResponse((r) => /\/api\/v1\/chains\?/.test(r.url()) && r.ok())
    await page.goto("/activity")
    const { chains: list } = (await (await chains).json()) as { chains: { origin: string }[] }
    test.skip(list.length === 0, "needs at least one recorded run")
    await page.goto(`/activity?walk=workflow:${list[0].origin}`)
    await expect(page.locator("h1")).toHaveCount(1)
    await page.waitForLoadState("networkidle")
    await expect(page.locator("h1")).toHaveCount(1)
  })

  test("ACT-01 a record page keeps exactly one h1", async ({ page }) => {
    await seed(page)
    await routeFeed(page, [], [
      { id: "e2e-e1", workspace_id: "ws", ts: iso(30 * 60_000), entry_type: "mission.status_change", severity: "info", actor_type: "user", summary: "E2E moved to In review", payload: { from: "In progress", to: "In review" } },
    ])
    await page.goto("/activity?entry=e2e-e1")
    await page.locator("[data-slot=peek]").getByRole("button", { name: "Open full record" }).click()
    await expect(page.locator("h1")).toHaveCount(1)
  })

  test("ACT-02 ?run= outside the range widens the range and lists the run", async ({ page }) => {
    await seed(page)
    await routeFeed(page, [chainRow("run_e2e_old", 10 * DAY)], [])
    await page.goto("/activity?run=run_e2e_old")
    await expect(page.locator("[data-slot=peek]")).toBeVisible()
    await expect(page).toHaveURL(/range=30d/)
    await page.keyboard.press("Escape")
    await expect(page.getByRole("table", { name: "Activity" }).locator('[data-row-id="run_e2e_old"]')).toBeVisible()
  })

  test("ACT-03 three updates to one issue on one day are one story row with its Peek", async ({ page }) => {
    await seed(page)
    const upd = (id: string, h: number, from: string, to: string) => ({
      id, workspace_id: "ws", ts: yesterdayAt(h), entry_type: "mission.status_change", severity: "info", actor_type: "user",
      summary: `E2E-1 moved to ${to}`, mission_id: "e2e-m1", payload: { from, to, identifier: "E2E-1" },
    })
    await routeFeed(page, [], [upd("s3", 11, "In progress", "In review"), upd("s2", 10, "Todo", "In progress"), upd("s1", 9, "Backlog", "Todo")])
    await page.goto("/activity")
    const row = page.getByRole("table", { name: "Activity" }).getByRole("button", { name: /3 updates/ })
    await expect(row).toHaveCount(1)
    await expect(row).toContainText("Todo → In progress → In review")
    await row.click()
    const peek = page.getByTestId("activity-peek-story")
    await expect(peek).toContainText("What happened, in order")
    await expect(peek.getByRole("button", { name: /Status changed/ })).toHaveCount(3)
  })

  test("ACT-04 Spend: one h1, the KPI row, by crew, most expensive work, the note; no budget", async ({ page }) => {
    await seed(page)
    await page.goto("/activity?mode=spend")
    await expect(page.locator("h1")).toHaveCount(1)
    await expect(page.getByRole("heading", { level: 1, name: "Spend" })).toBeVisible()
    const month = new Date().toLocaleDateString("en-GB", { month: "long" })
    for (const label of ["Spend · 24 h", `Spend · ${month}`, "Model calls · 24 h", "Most expensive"]) {
      await expect(page.locator("[data-kpi]").filter({ hasText: label })).toBeVisible()
    }
    await expect(page.getByRole("heading", { name: `By crew · ${month}` })).toBeVisible()
    await expect(page.getByRole("heading", { name: "Most expensive work · 24 h" })).toBeVisible()
    await expect(page.getByText("Admins only. On subscription plans the cost is an estimate, not money spent.")).toBeVisible()
    await expect(page.getByText(/budget/i)).toHaveCount(0)
    await expect(page.getByRole("combobox")).toHaveCount(0)
  })

  test("ACT-05 the palette goes straight to Activity, never to /journal", async ({ page }) => {
    await seed(page)
    await page.goto("/activity")
    const input = page.getByRole("combobox").filter({ visible: true })
    await expect(async () => {
      await page.keyboard.press("ControlOrMeta+k")
      await expect(input.first()).toBeVisible({ timeout: 1_000 })
    }).toPass({ timeout: 10_000 })
    await expect(page.locator("[cmdk-item]").first()).toBeVisible()
    await expect(page.locator("[cmdk-item][data-href^='/journal']")).toHaveCount(0)
    await page.locator("[cmdk-item][data-href='/activity?what=work']").click()
    await expect(page).toHaveURL(/\/activity\?what=work/)
  })

  test("ACT-05 a slug in an Activity link narrows the list to that agent", async ({ page }) => {
    await seed(page)
    const agents = page.waitForResponse((r) => /\/api\/v1\/agents\?/.test(r.url()) && r.ok())
    await page.goto("/activity")
    const body = (await (await agents).json()) as { id: string; slug: string }[] | { agents?: { id: string; slug: string }[] }
    const list = Array.isArray(body) ? body : (body.agents ?? [])
    test.skip(list.length === 0, "needs at least one agent")
    await page.goto(`/activity?agent=${encodeURIComponent(list[0].slug)}`)
    await expect(page).toHaveURL(new RegExp(`agent=${list[0].id}`))
  })
})

test.describe("finish (#3486) phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("ACT-04 Spend KPIs sit 2 × 2 and the page does not scroll sideways", async ({ page }) => {
    await seed(page)
    await page.goto("/activity?mode=spend")
    const kpis = page.locator("[data-kpi]")
    await expect(kpis).toHaveCount(4)
    const [a, b, c] = await Promise.all([0, 1, 2].map(async (i) => (await kpis.nth(i).boundingBox())!))
    expect(Math.round(a.y)).toBe(Math.round(b.y))
    expect(c.y).toBeGreaterThan(a.y)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})
