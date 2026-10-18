import { test, expect, request as plwRequest, type Page } from "@playwright/test"

// Harbor 2 Inbox acceptance (#3385): the panel beside the detail on desktop
// and tablet, list → detail → back on a phone, the Overview when nothing is
// open, the keys, and the Dashboard's ?attention= links. Runs against seeded
// demo data (crew reset); a check that needs a kind the seed lacks skips with
// a reason instead of passing vacuously.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=inbox]")
const rows = (page: Page) => panel(page).getByTestId("inbox-row")

test.describe("desktop 1440", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("desktop 1440 shows panel 272 + detail", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(panel(page)).toBeVisible()
    expect(Math.round((await panel(page).boundingBox())!.width)).toBe(272)
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
  })

  test("/inbox on 1440 shows Your inbox, at a glance", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(page.getByRole("heading", { level: 1, name: "Your inbox, at a glance" }).or(page.getByRole("heading", { name: "Nothing waits for you" }))).toBeVisible()
    // Nothing was opened for the reader: no decision block in the pane.
    await expect(page.getByTestId("decision-card")).toHaveCount(0)
  })

  test("key 1 opens To handle", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?view=updates")
    await expect(panel(page).getByRole("tab", { name: /Updates/ })).toHaveAttribute("aria-selected", "true")
    // A key pressed before the page has hydrated goes nowhere; press again
    // until the handler is there (1 is idempotent).
    await expect(async () => {
      await page.locator("body").press("1")
      await expect(page).toHaveURL(/view=action/, { timeout: 1000 })
    }).toPass()
    await expect(panel(page).getByRole("tab", { name: /To handle/ })).toHaveAttribute("aria-selected", "true")
  })

  test("J/K moves selection and updates 4 / 16", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    const count = await rows(page).count()
    test.skip(count < 2, "needs at least two rows in To handle")
    await page.locator("body").press("j")
    await expect(page.getByText(new RegExp(`^1 / ${count}$`))).toBeVisible()
    await page.locator("body").press("j")
    await expect(page.getByText(new RegExp(`^2 / ${count}$`))).toBeVisible()
    await page.locator("body").press("k")
    await expect(page.getByText(new RegExp(`^1 / ${count}$`))).toBeVisible()
  })

  test("E archives a read update", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?view=updates")
    const before = await rows(page).count()
    test.skip(before === 0, "needs an update to archive")
    await rows(page).first().click()
    const archive = page.getByRole("button", { name: "Archive (E)" })
    test.skip(await archive.isDisabled(), "first update is source-managed")
    await page.locator("body").press("e")
    await expect(page.getByTestId("inbox-receipt")).toContainText("Archived")
  })

  test("⌘↵ sends answer for run_needs_human", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?kind=run_needs_human")
    test.skip((await rows(page).count()) === 0, "seed has no run_needs_human card")
    await rows(page).first().click()
    await page.getByRole("button", { name: "Answer" }).click()
    await page.getByRole("textbox", { name: "Your answer" }).fill("Keep the 3-seat limit vague until legal approves.")
    await page.getByRole("textbox", { name: "Your answer" }).press("ControlOrMeta+Enter")
    await expect(page.getByTestId("act-receipt")).toBeVisible()
  })

  test("dashboard tile Decisions waiting → Inbox shows Overview filtered to decisions", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?attention=approvals")
    await expect(panel(page).getByText("Decisions waiting").first()).toBeVisible()
    await expect(page.getByTestId("inbox-focus-overview").or(page.getByRole("heading", { name: "Nothing waits for you" }))).toBeVisible()
  })

  test("run-alerts deep link", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?attention=run-alerts")
    await expect(panel(page).getByText("Run alerts").first()).toBeVisible()
  })

  test("unknown ?item shows No longer in your inbox, list stays", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?item=does-not-exist")
    await expect(page.getByRole("heading", { name: "No longer in your inbox" })).toBeVisible()
    await expect(panel(page)).toBeVisible()
  })

  test("legacy links keep their parameters", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?filter=casey")
    await expect(panel(page).getByRole("textbox", { name: "Search inbox…" })).toHaveValue("casey")
    await page.goto("/inbox-v2?item=x#y")
    await expect(page).toHaveURL(/\/inbox\?item=x#y$/)
  })
})

test.describe("wide 1600", () => {
  test.use({ viewport: { width: 1600, height: 900 } })

  test("wide 1600 shows ib-rail with Earlier from", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    test.skip((await rows(page).count()) === 0, "needs a row to open")
    await rows(page).first().click()
    await expect(page.getByTestId("inbox-rail")).toBeVisible()
  })
})

test.describe("tablet 1024", () => {
  test.use({ viewport: { width: 1024, height: 800 } })

  test("tablet 1024 shows panel + detail", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
    await expect(page.getByTestId("inbox-rail")).toBeHidden()
    // COM-05: at tablet width the panel starts closed and the top bar's panel
    // button floats it over the detail.
    await expect(panel(page)).toHaveCount(0)
    await page.getByTestId("secondary-panel-button").click()
    await expect(panel(page)).toBeVisible()
  })
})

test.describe("phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("phone 390 list → detail → back", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    const list = page.locator("[data-slot=secondary-panel][data-phone=inline]")
    await expect(list).toBeVisible()
    await expect(page.getByRole("main", { name: "Inbox detail" })).toHaveCount(0)
    const first = list.getByTestId("inbox-row").first()
    test.skip((await first.count()) === 0, "needs a row to open")
    await first.click()
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
    await page.getByRole("button", { name: "Back to inbox" }).click()
    await expect(list).toBeVisible()
  })
})

test.describe("empty", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("empty inbox shows Nothing waits for you", async ({ page }) => {
    await seed(page)
    // Every source answers with nothing.
    await page.route("**/api/v1/inbox?**", (route) => route.fulfill({ json: { rows: [], count: 0, unread_count: 0, has_more: false } }))
    await page.route("**/api/v1/approvals?**", (route) => route.fulfill({ json: { rows: [], status: "all", count: 0, has_more: false } }))
    await page.route("**/api/v1/missions?**", (route) => route.fulfill({ json: [] }))
    await page.goto("/inbox")
    await expect(page.getByRole("heading", { name: "Nothing waits for you" })).toBeVisible()
    await expect(page.getByText("crewship inbox")).toBeVisible()
  })
})

// ---------------------------------------------------------------------------
// #3485 (INB-01 … INB-05). The seed has no pending approval and no API that
// creates one (approvals are enqueued by the harbormaster inside a run), so
// the checks that need a specific decision serve the feed through
// page.route. Sessions, roles and the RBAC gate on the page stay real.

const NOW = Date.now()
const minutesAgo = (n: number) => new Date(NOW - n * 60_000).toISOString()

function inboxRow(o: Record<string, unknown>) {
  return {
    workspace_id: "ws", source_id: `src-${o.id}`, state: "read", priority: "medium", blocking: false,
    created_at: minutesAgo(10), updated_at: minutesAgo(10), ...o,
  }
}

/** Serve the Inbox's sources: inbox rows (list and detail), the approvals queue, no missions. */
async function serveFeed(page: Page, feed: { inbox?: Record<string, unknown>[]; approvals?: Record<string, unknown>[] }) {
  const inbox = feed.inbox ?? []
  await page.route(/\/api\/v1\/inbox\?/, (route) => route.fulfill({ json: { rows: inbox, count: inbox.length, unread_count: 0, has_more: false } }))
  await page.route(/\/api\/v1\/inbox\/(?!count)[^/?]+\?/, (route) => {
    const id = decodeURIComponent(new URL(route.request().url()).pathname.split("/").pop() ?? "")
    const row = inbox.find((r) => r.id === id)
    return row ? route.fulfill({ json: row }) : route.fulfill({ status: 404, json: { detail: "not found" } })
  })
  await page.route(/\/api\/v1\/missions\?/, (route) => route.fulfill({ json: [] }))
  if (feed.approvals) {
    const rows = feed.approvals
    await page.route(/\/api\/v1\/approvals\?/, (route) => route.fulfill({ json: { rows, status: "all", count: rows.length, has_more: false } }))
  }
}

test.describe("Agent facet (INB-01)", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("/inbox?agent_id=<unknown> → Agent: unknown and Nothing matches · Clear filters", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?attention=approvals&agent_id=ag_does_not_exist")
    await expect(panel(page).getByRole("button", { name: "Remove filter Agent: unknown" })).toBeVisible()
    await expect(panel(page).getByText("Nothing matches these filters.")).toBeVisible()
    await expect(rows(page)).toHaveCount(0)
    await panel(page).getByRole("button", { name: "Clear filters" }).click()
    await expect(panel(page).getByText(/^Agent:/)).toHaveCount(0)
  })

  test("?agent=<slug> from an agent page shows only that agent's rows under Agent: {name}", async ({ page }) => {
    await seed(page)
    await serveFeed(page, {
      inbox: [
        inboxRow({ id: "e-scout", kind: "escalation", blocking: true, title: "Scout needs a Linear token", sender_type: "agent", sender_name: "scout", payload: { escalation_type: "GENERAL" } }),
        inboxRow({ id: "e-riley", kind: "escalation", blocking: true, title: "Riley needs a GitHub token", sender_type: "agent", sender_name: "riley", payload: { escalation_type: "GENERAL" } }),
      ],
    })
    await page.goto("/inbox?agent=scout")
    // The roster may not know the mocked senders; the chip then names the slug.
    await expect(panel(page).getByRole("button", { name: /^Remove filter Agent: / })).toBeVisible()
    await expect(rows(page)).toHaveCount(1)
    await expect(rows(page).first()).toContainText("Scout needs a Linear token")
  })
})

test.describe("wide-desktop rail (INB-02)", () => {
  const RUN_WAITPOINT = inboxRow({
    id: "wp-rail", kind: "waitpoint", blocking: true, state: "read", source_id: "tok-rail",
    title: "Approve publishing the weekly scan", sender_type: "pipeline", sender_name: "weekly-scan",
    payload: { pipeline_run_id: "run_rail", pipeline_slug: "weekly-scan", sites: 14 },
  })

  test.describe("1600", () => {
    test.use({ viewport: { width: 1600, height: 900 } })
    test("context chips and run steps sit in the rail, opened", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: [RUN_WAITPOINT] })
      await page.goto("/inbox?item=wp-rail")
      const rail = page.getByTestId("inbox-rail")
      await expect(rail.getByRole("navigation", { name: "Where this came from" })).toBeVisible()
      await expect(rail.locator("details", { hasText: "How the run got here" })).toHaveAttribute("open", "")
      await expect(page.getByTestId("inbox-detail-frame").getByRole("navigation", { name: "Where this came from" })).toHaveCount(0)
    })
  })

  test.describe("1440", () => {
    test.use({ viewport: { width: 1440, height: 900 } })
    test("below 1536 they stay in the column and there is no rail", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: [RUN_WAITPOINT] })
      await page.goto("/inbox?item=wp-rail")
      await expect(page.getByTestId("inbox-detail-frame").getByRole("navigation", { name: "Where this came from" })).toBeVisible()
      await expect(page.getByTestId("inbox-rail")).toBeHidden()
    })
  })
})

test.describe("decision dock (INB-04)", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("Deny is the secondary button left of a primary Approve", async ({ page }) => {
    await seed(page)
    await serveFeed(page, { inbox: [inboxRow({ id: "wp-dock", kind: "waitpoint", blocking: true, title: "Approve the promote step", sender_type: "pipeline", sender_name: "docs-publish" })] })
    await page.goto("/inbox?item=wp-dock")
    const dock = page.getByTestId("decision-dock")
    await expect(dock.getByRole("button", { name: "Deny" })).toHaveAttribute("data-variant", "outline")
    await expect(dock.getByRole("button", { name: "Approve" })).toHaveAttribute("data-variant", "default")
    const deny = (await dock.getByRole("button", { name: "Deny" }).boundingBox())!
    const approve = (await dock.getByRole("button", { name: "Approve" }).boundingBox())!
    expect(deny.x).toBeLessThan(approve.x)
  })
})

// INB-05: who decides an approval. Logs in as the seeded RBAC baseline users
// (cmd/crewship/cmd_seed_data_users.go; password E2E_RBAC_PASSWORD, default
// rbac-demo-1234). The same pending approval is served to both; only the
// admin tier may load the queue (GET /api/v1/approvals is roleManage) and
// decide it.
test.describe("approval decisions by role (INB-05)", () => {
  test.use({ storageState: { cookies: [], origins: [] }, viewport: { width: 1440, height: 900 } })

  const PENDING = {
    id: "ap_e2e_merge", workspace_id: "ws", kind: "tool_call", status: "pending",
    reason: "Reviewer wants to merge acme/web#812 into main.", requested_by: "", created_at: minutesAgo(5),
    timeout_at: new Date(NOW + 45 * 60_000).toISOString(), payload: { tool: "github.merge_pr", target: "production" },
  }

  async function loginAs(page: Page, email: string) {
    const base = test.info().project.use.baseURL ?? process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:3001"
    const ctx = await plwRequest.newContext({ baseURL: base })
    const { csrfToken } = (await (await ctx.get("/api/auth/csrf")).json()) as { csrfToken: string }
    const res = await ctx.post("/api/auth/callback/credentials", {
      form: { csrfToken, email, password: process.env.E2E_RBAC_PASSWORD || "rbac-demo-1234", callbackUrl: "/", json: "true" },
    })
    test.skip(!res.ok(), `cannot log in as ${email} (seed without RBAC baseline users?)`)
    await page.context().addCookies((await ctx.storageState()).cookies)
    await ctx.dispose()
  }

  test("ADMIN decides: Approve sends the comment to the decide endpoint and shows the receipt", async ({ page }) => {
    await loginAs(page, "admin-baseline@crewship.local")
    await seed(page)
    await serveFeed(page, { inbox: [], approvals: [PENDING] })
    let decided: unknown = null
    await page.route(/\/api\/v1\/approvals\/ap_e2e_merge\/decide/, async (route) => {
      decided = route.request().postDataJSON()
      await route.fulfill({ json: { status: "approved", decided_by: "admin-baseline" } })
    })
    await page.goto("/inbox?item=ap_e2e_merge")
    const dock = page.getByTestId("decision-dock")
    await expect(dock.getByRole("button", { name: "Approve" })).toBeEnabled()
    await expect(dock.getByRole("button", { name: "Deny" })).toBeEnabled()
    await expect(page.getByTestId("inbox-role-note")).toHaveCount(0)
    await page.getByRole("textbox", { name: "Decision comment" }).fill("Checked the diff; merge.")
    await dock.getByRole("button", { name: "Approve" }).click()
    await expect(page.getByTestId("inbox-receipt")).toContainText("Approved")
    expect(decided).toEqual({ status: "approved", comment: "Checked the diff; merge." })
  })

  test("MEMBER never loads the queue and gets no Approve or Deny for it", async ({ page }) => {
    await loginAs(page, "member-baseline@crewship.local")
    await seed(page)
    const queueCalls: string[] = []
    const decideCalls: string[] = []
    page.on("request", (r) => {
      if (/\/api\/v1\/approvals(\?|$)/.test(r.url())) queueCalls.push(r.url())
      if (/\/api\/v1\/approvals\/[^/]+\/decide/.test(r.url())) decideCalls.push(r.url())
    })
    // The same pending approval is there to be served — the gate, not an
    // empty fixture, is what keeps it off a member's screen.
    await serveFeed(page, { inbox: [], approvals: [PENDING] })
    await page.goto("/inbox?attention=approvals")
    await expect(panel(page)).toBeVisible()
    await page.goto("/inbox?item=ap_e2e_merge")
    await expect(page.getByRole("heading", { name: "No longer in your inbox" })).toBeVisible()
    await expect(page.getByRole("button", { name: /^(Approve|Deny)$/ })).toHaveCount(0)
    expect(queueCalls).toEqual([])
    expect(decideCalls).toEqual([])
  })
})

// COM-03: the partial-source banner is the kit PageBanner (bad tone) at every
// width, the page's one banner, and Retry asks the failed source again.
for (const vp of [{ name: "tablet 1024", width: 1024, height: 800 }, { name: "phone 390", width: 390, height: 844 }]) {
  test.describe(`partial-source banner, ${vp.name}`, () => {
    test.use({ viewport: { width: vp.width, height: vp.height }, ...(vp.width < 768 ? { hasTouch: true, isMobile: true } : {}) })

    test(`${vp.name}: a failed approvals source shows one bad PageBanner with Retry inside the viewport`, async ({ page }) => {
      await seed(page)
      let approvalsCalls = 0
      await page.route(/\/api\/v1\/approvals\?/, (route) => { approvalsCalls++; return route.fulfill({ status: 500, json: { error: "approvals unavailable" } }) })
      await page.goto("/inbox")
      const banner = page.locator(".page-banner[data-tone=bad]")
      await expect(banner).toHaveCount(1)
      await expect(banner).toHaveAttribute("role", "alert")
      await expect(banner).toContainText("This inbox may be incomplete")
      const retry = banner.getByRole("button", { name: "Retry" })
      await expect(retry).toBeVisible()
      const box = (await banner.boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(vp.width)
      const before = approvalsCalls
      await retry.click()
      await expect.poll(() => approvalsCalls).toBeGreaterThan(before)
    })
  })
}

// PAR-019…023, 144 (#3577): the list toolbar, the Overview typography and the
// bell popovers match v67. Checked in a real browser at 1440 / 1024 / 390.
test.describe("v67 parity", () => {
  const WAITING = inboxRow({
    id: "e-parity", kind: "escalation", blocking: true, state: "unread", title: "Scout needs a Linear token",
    sender_type: "agent", sender_name: "scout", payload: { escalation_type: "GENERAL" },
  })

  test.describe("desktop 1440", () => {
    test.use({ viewport: { width: 1440, height: 900 } })

    test("toolbar: search with the / hint, Filter, select, collapse — and no Sort button", async ({ page }) => {
      await seed(page)
      await page.goto("/inbox")
      await expect(panel(page)).toBeVisible()
      await expect(panel(page).locator("[data-inbox-search] kbd")).toHaveText("/")
      await expect(panel(page).getByRole("button", { name: "Filter" })).toBeVisible()
      await expect(panel(page).getByRole("button", { name: "Select several" })).toBeVisible()
      await expect(panel(page).getByRole("button", { name: "View: sort" })).toHaveCount(0)
      await panel(page).getByRole("button", { name: "Filter" }).click()
      await expect(panel(page).getByRole("group", { name: "Filter inbox" }).getByRole("button", { name: /^Sort/ })).toBeVisible()
    })

    test("Overview: 13 px semibold tile titles, hints not clipped, 28 px card tiles", async ({ page }) => {
      await seed(page)
      // The Overview needs something to handle; an empty inbox shows its
      // empty state instead (the nightly seed has none).
      await serveFeed(page, { inbox: [WAITING] })
      await page.goto("/inbox")
      const overview = page.getByTestId("inbox-overview")
      await expect(overview).toBeVisible()
      const tile = overview.getByRole("button", { name: /^Decision: / })
      const title = await tile.locator("b").evaluate((el) => getComputedStyle(el).fontSize + "/" + getComputedStyle(el).fontWeight)
      expect(title).toBe("13px/600")
      const clipped = await tile.locator("small").evaluate((el) => el.scrollWidth > el.clientWidth)
      expect(clipped).toBe(false)
      expect(Math.round((await overview.locator("section .icon-tile").first().boundingBox())!.width)).toBe(28)
    })

    test("counts in the space switcher are sans tabular", async ({ page }) => {
      await seed(page)
      await page.goto("/inbox")
      const count = panel(page).getByRole("tab", { name: /To handle/ }).locator("span").last()
      const font = await count.evaluate((el) => getComputedStyle(el).fontFamily + "|" + getComputedStyle(el).fontVariantNumeric)
      expect(font).not.toMatch(/mono/i)
      expect(font).toContain("tabular-nums")
    })

    test("bells: Inbox lists every kind chip; Activity is a 480 px panel with the fleet line and its chips", async ({ page }) => {
      await seed(page)
      // The chip strip shows only when there is something to filter
      // (inbox-bell-chips.test.tsx); the nightly seed has an empty inbox.
      await serveFeed(page, { inbox: [WAITING] })
      await page.goto("/")
      await page.getByTestId("bell-trigger").click()
      for (const kind of ["all", "decision", "input", "review", "repair"]) await expect(page.getByTestId(`bell-chip-${kind}`)).toBeVisible()
      await page.keyboard.press("Escape")
      await page.getByTestId("activity-trigger").click()
      const pop = page.getByTestId("activity-popover")
      expect(Math.round((await pop.boundingBox())!.width)).toBe(480)
      await expect(page.getByRole("link", { name: /Open Activity/ })).toBeVisible()
    })
  })

  test.describe("tablet 1024", () => {
    test.use({ viewport: { width: 1024, height: 900 } })

    test("the Activity popover stays 480 px wide and inside the viewport", async ({ page }) => {
      await seed(page)
      await page.goto("/")
      await page.getByTestId("activity-trigger").click()
      const box = (await page.getByTestId("activity-popover").boundingBox())!
      expect(Math.round(box.width)).toBe(480)
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(1024)
    })
  })

  test.describe("phone 390", () => {
    test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

    test("the Activity popover is a sheet between the viewport's edges", async ({ page }) => {
      await seed(page)
      await page.goto("/")
      await page.getByTestId("activity-trigger").click()
      const box = (await page.getByTestId("activity-popover").boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(0)
      expect(box.x + box.width).toBeLessThanOrEqual(390)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
    })
  })
})

// ODCH-I03 / I04: a typed answer, a credential value and a trigger activation
// from the Inbox. The feed is served through page.route (the seed has none of
// these rows); the three write endpoints are intercepted so nothing is changed.
const ASK = {
  question: inboxRow({ id: "ib_e2e_q", kind: "escalation", state: "unread", source_id: "esc_e2e_q", title: "Merge now or fix lint first?", blocking: true, target_role: "MANAGER", payload: { escalation_type: "TEXT" } }),
  cred: inboxRow({ id: "ib_e2e_c", kind: "escalation", state: "unread", source_id: "esc_e2e_c", title: "Credential requested: Notion API key", blocking: true, target_role: "ADMIN", payload: { escalation_type: "CREDENTIAL", needs_credential_value: true } }),
  trigger: inboxRow({ id: "ib_e2e_t", kind: "escalation", state: "unread", source_id: "trg_e2e", title: "Routine trigger ready: competitor-change", blocking: true, target_role: "MANAGER", payload: { kind: "routine_trigger_activation", schedule_id: "sch_e2e", slug: "competitor-scan" } }),
  held: inboxRow({ id: "ib_e2e_h", kind: "escalation", state: "unread", source_id: "held_e2e", title: "Artifact creation held for approval: Q4 board", blocking: true, target_role: "MANAGER", payload: { kind: "page_create_held", requested_name: "Q4 board" } }),
}

for (const vp of [{ n: "1440", width: 1440, height: 900 }, { n: "1024", width: 1024, height: 800 }, { n: "390", width: 390, height: 844, mobile: true }]) {
  test.describe(`typed answers and activation at ${vp.n}`, () => {
    test.use({ viewport: { width: vp.width, height: vp.height }, ...(vp.mobile ? { hasTouch: true, isMobile: true } : {}) })

    test("a question takes a written answer", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: Object.values(ASK) })
      let sent: unknown = null
      await page.route(/\/api\/v1\/escalations\/esc_e2e_q\/resolve/, async (route) => { sent = route.request().postDataJSON(); await route.fulfill({ json: {} }) })
      await page.goto("/inbox?item=ib_e2e_q")
      const answer = page.getByRole("textbox", { name: "Your answer" })
      await answer.fill("Fix lint first")
      await page.getByRole("button", { name: "Send answer" }).click()
      await expect.poll(() => sent).toMatchObject({ action: "approve", resolution: "Fix lint first" })
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
    })

    test("a credential request takes the value in a masked field and empties it", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: Object.values(ASK) })
      let sent: { value?: string } | null = null
      // The answer is held until the field has been checked: on success the
      // dock gives way to the "Approved" record, so the field is only there
      // while the request is in flight. It must already be empty then.
      let release: () => void = () => {}
      const answered = new Promise<void>((r) => { release = r })
      await page.route(/\/api\/v1\/escalations\/esc_e2e_c\/supply/, async (route) => {
        sent = route.request().postDataJSON()
        await answered
        await route.fulfill({ json: { credential: null } })
      })
      await page.goto("/inbox?item=ib_e2e_c")
      const field = page.getByLabel("Value")
      await expect(field).toHaveAttribute("type", "password")
      await field.fill("secret_e2e_value")
      await page.getByRole("button", { name: "Supply credential" }).click()
      await expect.poll(() => sent?.value).toBe("secret_e2e_value")
      await expect(field).toHaveValue("")
      await expect(page.locator("body")).not.toContainText("secret_e2e_value")
      release()
      await expect(page.getByRole("main", { name: "Inbox detail" }).getByRole("status").getByRole("heading", { name: "Approved" })).toBeVisible()
      await expect(page.locator("body")).not.toContainText("secret_e2e_value")
    })

    test("a routine trigger is activated by a manager and has no Dismiss", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: Object.values(ASK) })
      let hit = false
      await page.route(/\/pipeline-schedules\/sch_e2e\/activate/, async (route) => { hit = true; await route.fulfill({ json: {} }) })
      await page.goto("/inbox?item=ib_e2e_t")
      await expect(page.getByRole("button", { name: /Dismiss|Keep off/ })).toHaveCount(0)
      const activate = page.getByRole("button", { name: "Activate trigger" })
      if (vp.mobile) expect((await activate.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      await activate.click()
      await expect.poll(() => hit).toBe(true)
    })

    test("Artifact creation held has no buttons", async ({ page }) => {
      await seed(page)
      await serveFeed(page, { inbox: Object.values(ASK) })
      await page.goto("/inbox?item=ib_e2e_h")
      await expect(page.getByRole("main", { name: "Inbox detail" })).toContainText("Artifact creation held")
      await expect(page.getByTestId("decision-dock").getByRole("button")).toHaveCount(0)
    })
  })
}
