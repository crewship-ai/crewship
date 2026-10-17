import { test, expect, request as plwRequest, type Browser, type BrowserContext } from "@playwright/test"

// F-02 acceptance: the Journal is merged into Activity. Every old /journal
// link lands on the equivalent Activity view with its filters, and who sees
// what does not change — the technical log, fleet runs and Spend stay OWNER /
// ADMIN only; a member is sent to the Activity list.
//
// Runs against a deployed environment with the RBAC demo seed
// (docs/development/demo-rbac.md):
//   E2E_EMAIL / E2E_PASSWORD             an OWNER or ADMIN (admin-baseline@crewship.local)
//   E2E_MEMBER_EMAIL / E2E_MEMBER_PASSWORD  a MEMBER (default member-baseline@crewship.local,
//                                         password defaults to E2E_PASSWORD)

const BASE_URL = process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:3000"
const ADMIN = { email: process.env.E2E_EMAIL, password: process.env.E2E_PASSWORD }
const MEMBER = {
  email: process.env.E2E_MEMBER_EMAIL ?? "member-baseline@crewship.local",
  password: process.env.E2E_MEMBER_PASSWORD ?? process.env.E2E_PASSWORD,
}
const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

test.use({ storageState: { cookies: [], origins: [] }, viewport: { width: 1440, height: 900 } })

async function contextFor(browser: Browser, who: { email?: string; password?: string }): Promise<BrowserContext> {
  const api = await plwRequest.newContext({ baseURL: BASE_URL })
  const { csrfToken } = (await (await api.get("/api/auth/csrf")).json()) as { csrfToken: string }
  const res = await api.post("/api/auth/callback/credentials", {
    form: { csrfToken, email: who.email!, password: who.password!, callbackUrl: "/", json: "true" },
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok() || (body && typeof body.error === "string")) throw new Error(`login failed for ${who.email}`)
  const state = await api.storageState()
  await api.dispose()
  if (!state.cookies.some((c) => c.name.includes("session-token"))) throw new Error(`no session for ${who.email}`)
  const ctx = await browser.newContext({ storageState: { cookies: state.cookies, origins: [] }, viewport: { width: 1440, height: 900 } })
  await ctx.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
  return ctx
}

/** The query of the URL the browser settled on, as a plain record. */
function queryOf(url: string): Record<string, string> {
  return Object.fromEntries(new URL(url).searchParams)
}

test.describe("ADMIN: the Journal's views inside Activity", () => {
  test.skip(!ADMIN.email || !ADMIN.password, "set E2E_EMAIL and E2E_PASSWORD (an OWNER or ADMIN)")
  let ctx: BrowserContext
  test.beforeAll(async ({ browser }) => {
    ctx = await contextFor(browser, ADMIN)
  })
  test.afterAll(async () => ctx?.close())

  const cases: [string, string, Record<string, string>][] = [
    ["/journal", "/activity", { section: "log" }],
    ["/journal?tab=timeline&trace_id=run_e2e&severity=error", "/activity", { section: "log", trace_id: "run_e2e", severity: "error" }],
    ["/journal?time=7d&crew_id=c1&agent_id=a1&mission_id=ENG-1&mute=container&q=boom", "/activity", { section: "log", time: "7d", crew_id: "c1", agent_id: "a1", mission_id: "ENG-1", mute: "container", q: "boom" }],
    ["/journal?crew=engineering&agent=robin", "/activity", { section: "log", crew: "engineering", agent: "robin" }],
    ["/journal?tab=runs&run_window=7d&run_status=FAILED&run_trigger=CRON&run_page=2", "/activity", { section: "runs", run_window: "7d", run_status: "FAILED", run_trigger: "CRON", run_page: "2" }],
    ["/journal?tab=spend", "/activity", { mode: "spend" }],
    ["/runs", "/activity", { section: "runs" }],
  ]
  for (const [from, path, query] of cases) {
    test(`${from} → ${path}?${new URLSearchParams(query)}`, async () => {
      const page = await ctx.newPage()
      await page.goto(from)
      await expect(page).toHaveURL((u) => u.pathname === path && JSON.stringify(queryOf(u.href)) === JSON.stringify(query))
      await page.close()
    })
  }

  test("ADMIN sees Technical log, Fleet runs and the Spend segment", async () => {
    const page = await ctx.newPage()
    await page.goto("/activity")
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=activity]")
    await expect(panel.getByRole("button", { name: /Technical log/ })).toBeVisible()
    await expect(panel.getByRole("button", { name: /Fleet runs/ })).toBeVisible()
    const seg = page.getByRole("navigation", { name: "Activity or spend" })
    await expect(seg.getByRole("link", { name: "Activity" })).toHaveAttribute("aria-current", "page")
    await seg.getByRole("link", { name: "Spend" }).click()
    await expect(page).toHaveURL(/\/activity\?mode=spend$/)
    await expect(page.getByRole("main", { name: "Spend" })).toBeVisible()
    await page.goto("/activity?section=log&trace_id=run_e2e")
    await expect(page.getByRole("tab", { name: /Technical log/ })).toHaveAttribute("aria-selected", "true")
    await page.getByRole("tab", { name: /Fleet runs/ }).click()
    await expect(page).toHaveURL(/section=runs/)
    await page.close()
  })
})

test.describe("MEMBER: sent to the Activity list, never to an admin view", () => {
  test.skip(!MEMBER.password, "set E2E_MEMBER_PASSWORD or E2E_PASSWORD for the member account")
  let ctx: BrowserContext
  test.beforeAll(async ({ browser }) => {
    ctx = await contextFor(browser, MEMBER)
  })
  test.afterAll(async () => ctx?.close())

  const cases: [string, Record<string, string>][] = [
    ["/journal", {}],
    ["/journal?tab=spend", {}],
    ["/journal?tab=timeline&trace_id=run_e2e&severity=error", { run: "run_e2e" }],
    ["/journal?time=30d&q=boom", { range: "30d", q: "boom" }],
    ["/journal?tab=runs&run_status=FAILED", { what: "work", range: "24h", view: "failed" }],
    ["/journal?tab=runs&run_window=7d&run_trigger=WEBHOOK", { what: "work", range: "7d", source: "webhook" }],
    ["/runs", { what: "work", range: "24h" }],
    ["/activity?section=log&trace_id=run_e2e", { run: "run_e2e" }],
    ["/activity?section=runs", { what: "work", range: "24h" }],
    ["/activity?mode=spend", {}],
  ]
  for (const [from, query] of cases) {
    test(`${from} → /activity${Object.keys(query).length ? `?${new URLSearchParams(query)}` : ""}`, async () => {
      const page = await ctx.newPage()
      await page.goto(from)
      await expect(page).toHaveURL((u) => u.pathname === "/activity" && JSON.stringify(queryOf(u.href)) === JSON.stringify(query))
      await expect(page.getByRole("main", { name: "Spend" })).toHaveCount(0)
      await expect(page.getByRole("tab", { name: /Technical log|Fleet runs/ })).toHaveCount(0)
      await page.close()
    })
  }

  test("MEMBER sees no Technical log, Fleet runs or Spend segment", async () => {
    const page = await ctx.newPage()
    await page.goto("/activity")
    const panel = page.locator("[data-slot=secondary-panel][data-panel-id=activity]")
    await expect(panel.getByRole("button", { name: /Everything/ })).toBeVisible()
    await expect(panel.getByRole("button", { name: /Technical log/ })).toHaveCount(0)
    await expect(panel.getByRole("button", { name: /Fleet runs/ })).toHaveCount(0)
    await expect(page.getByRole("navigation", { name: "Activity or spend" })).toHaveCount(0)
    await page.close()
  })
})
