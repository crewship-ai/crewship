import { test, expect, request as plwRequest, type Page } from "@playwright/test"

// Harbor 2 legacy links (#3405, part of #3366 / #3350). Old URLs that left the
// app — notification emails, Slack "Review" buttons, `crewship open …` — must
// keep landing somewhere real after a surface folds into another one.
//
// /approvals: approval requests are decided in the Inbox now; the route is a
// permanent redirect. Runs against seeded demo data (crew reset --yes).

const BASE_URL = process.env.PLAYWRIGHT_BASE_URL || "http://localhost:3001"
const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

test.describe("/approvals", () => {
  test("/approvals → /inbox?attention=approvals", async ({ page }) => {
    await seed(page)
    await page.goto("/approvals")
    await page.waitForURL(/\/inbox\?attention=approvals$/, { timeout: 10_000 })
    await expect(page.locator("[data-slot=secondary-panel][data-panel-id=inbox]")).toBeVisible()
    // None of the retired page is left on the way through.
    await expect(page.getByText(/Approvals is an admin surface|Inbox clear/)).toHaveCount(0)
  })

  test("/approvals?agent_id=ag_1 → Inbox decisions with the agent carried along", async ({ page }) => {
    await seed(page)
    await page.goto("/approvals?agent_id=ag_1")
    await page.waitForURL(/\/inbox\?attention=approvals&agent_id=ag_1$/, { timeout: 10_000 })
  })

  // The Inbox does not read agent_id yet (Agent facet is a follow-up on the
  // Inbox side and needs the server filter). Flip to test() when it lands.
  test.fixme("/approvals?agent_id=ag_1 → Inbox filtered to that agent", async ({ page }) => {
    await seed(page)
    await page.goto("/approvals?agent_id=ag_1")
    await expect(page.getByRole("button", { name: /^Remove filter Agent:/ })).toBeVisible()
  })

  test("/approvals?status=decided → Inbox History", async ({ page }) => {
    await seed(page)
    await page.goto("/approvals?status=decided")
    await page.waitForURL(/\/inbox\?view=history$/, { timeout: 10_000 })
  })

  test("without JavaScript the old link still reaches the Inbox", async ({ browser }) => {
    const context = await browser.newContext({ javaScriptEnabled: false })
    const page = await context.newPage()
    await page.goto("/approvals")
    await page.waitForURL(/\/inbox\?attention=approvals/, { timeout: 10_000 })
    await context.close()
  })
})

// Who may decide is unchanged: the approvals feed is loaded for the admin tier
// only and the decide endpoint stays roleManage. Logs in as the seeded RBAC
// baseline users (cmd/crewship/cmd_seed_data_users.go).
test.describe("/approvals by role", () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  async function loginAs(page: Page, email: string) {
    const ctx = await plwRequest.newContext({ baseURL: BASE_URL })
    const { csrfToken } = (await (await ctx.get("/api/auth/csrf")).json()) as { csrfToken: string }
    const res = await ctx.post("/api/auth/callback/credentials", {
      form: { csrfToken, email, password: process.env.E2E_RBAC_PASSWORD || "rbac-demo-1234", callbackUrl: "/", json: "true" },
    })
    test.skip(!res.ok(), `cannot log in as ${email} (seed without RBAC baseline users?)`)
    await page.context().addCookies((await ctx.storageState()).cookies)
    await ctx.dispose()
  }

  test("MEMBER lands in the Inbox and never loads the approvals queue", async ({ page }) => {
    await loginAs(page, "member-baseline@crewship.local")
    await seed(page)
    const approvalCalls: string[] = []
    page.on("request", (r) => {
      if (/\/api\/v1\/approvals(\?|$)/.test(r.url())) approvalCalls.push(r.url())
    })
    await page.goto("/approvals")
    await page.waitForURL(/\/inbox\?attention=approvals/, { timeout: 10_000 })
    await page.waitForLoadState("domcontentloaded")
    await page.waitForTimeout(1_000)
    expect(approvalCalls).toEqual([])
  })

  test("ADMIN lands in the Inbox with the approvals queue loaded", async ({ page }) => {
    await loginAs(page, "admin-baseline@crewship.local")
    await seed(page)
    const queue = page.waitForResponse((r) => /\/api\/v1\/approvals(\?|$)/.test(r.url()), { timeout: 15_000 })
    await page.goto("/approvals")
    await page.waitForURL(/\/inbox\?attention=approvals/, { timeout: 10_000 })
    // 404 is the "not configured" state, which the Inbox reports as a source
    // banner; a 403 would mean the admin tier lost its read.
    expect((await queue).status()).not.toBe(403)
  })
})
